package qdrant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMutationsWaitForCompletionAndValidateStatus(t *testing.T) {
	tests := []struct {
		name       string
		response   string
		statusCode int
		wantError  bool
	}{
		{name: "completed", response: `{"status":"ok","result":{"status":"completed"}}`, statusCode: http.StatusOK},
		{name: "acknowledged is not durable completion", response: `{"status":"ok","result":{"status":"acknowledged"}}`, statusCode: http.StatusOK, wantError: true},
		{name: "application failure", response: `{"status":"failed","result":{"status":"completed"}}`, statusCode: http.StatusOK, wantError: true},
		{name: "missing operation completion", response: `{"status":"ok"}`, statusCode: http.StatusOK, wantError: true},
		{name: "HTTP failure", response: `{"status":"ok"}`, statusCode: http.StatusBadGateway, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("wait") != "true" {
					t.Fatalf("wait query = %q, want true", r.URL.Query().Get("wait"))
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()
			err := NewClient(server.URL, "memory").Upsert(context.Background(), Point{ID: "id", Vector: []float32{1}})
			if (err != nil) != tt.wantError {
				t.Fatalf("Upsert error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestNewClientHasBoundedHTTPTimeout(t *testing.T) {
	client := NewClient("http://example.test", "memory")
	if client.httpClient.Timeout != defaultHTTPTimeout || client.httpClient.Timeout <= 0 {
		t.Fatalf("HTTP timeout = %s, want %s", client.httpClient.Timeout, defaultHTTPTimeout)
	}
}

func TestReadLimitedBodyRejectsOversizedResponse(t *testing.T) {
	_, err := readLimitedBody(strings.NewReader("too large"), 3)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized response error, got %v", err)
	}
}

func TestAllPointMutationsUseWaitTrue(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, fmt.Sprintf("%s %s wait=%s", r.Method, r.URL.Path, r.URL.Query().Get("wait")))
		_, _ = w.Write([]byte(`{"status":"ok","result":{"status":"completed"}}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "memory")
	mutations := []func() error{
		func() error { return client.Upsert(context.Background(), Point{ID: "id", Vector: []float32{1}}) },
		func() error { return client.Delete(context.Background(), []string{"id"}) },
		func() error {
			return client.DeleteByFilter(context.Background(), map[string]interface{}{"must": []interface{}{}})
		},
		func() error { return client.SetPayload(context.Background(), "id", map[string]interface{}{"x": 1}) },
		func() error { return client.CreateFieldIndex(context.Background(), "namespace", "keyword") },
	}
	for _, mutation := range mutations {
		if err := mutation(); err != nil {
			t.Fatal(err)
		}
	}
	for _, request := range requests {
		if !strings.HasSuffix(request, "wait=true") {
			t.Fatalf("mutation request missing wait=true: %s", request)
		}
	}
}

func TestQdrantPointID_NumericString(t *testing.T) {
	got := qdrantPointID("12345")
	if got != uint64(12345) {
		t.Fatalf("qdrantPointID numeric = %#v, want uint64(12345)", got)
	}
}

func TestQdrantPointID_MaxUint64(t *testing.T) {
	const id = "18446744073709551615"
	got := qdrantPointID(id)
	if got != uint64(18446744073709551615) {
		t.Fatalf("qdrantPointID numeric = %#v, want max uint64", got)
	}

	encoded, err := json.Marshal(map[string]interface{}{"id": got})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"id":18446744073709551615}` {
		t.Fatalf("encoded ID = %s", encoded)
	}
}

func TestQdrantPointID_UUIDString(t *testing.T) {
	id := "4f08ef2a-42c0-45df-a6c3-5ca86db4ddf8"
	got := qdrantPointID(id)
	if got != id {
		t.Fatalf("qdrantPointID uuid = %#v, want %q", got, id)
	}
}

func TestSetPayloadUsesPartialUpdateEndpoint(t *testing.T) {
	var method string
	var path string
	var body struct {
		Payload map[string]interface{} `json:"payload"`
		Points  []interface{}          `json:"points"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","result":{"status":"completed"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "memory")
	if err := client.SetPayload(context.Background(), "12345", map[string]interface{}{"recall_count": 2}); err != nil {
		t.Fatalf("SetPayload returned error: %v", err)
	}

	if method != http.MethodPost {
		t.Fatalf("SetPayload method = %s, want POST partial update", method)
	}
	if path != "/collections/memory/points/payload" {
		t.Fatalf("SetPayload path = %s", path)
	}
	if got := body.Payload["recall_count"]; got != float64(2) {
		t.Fatalf("payload recall_count = %#v, want 2", got)
	}
	if len(body.Points) != 1 || body.Points[0] != float64(12345) {
		t.Fatalf("points = %#v, want [12345]", body.Points)
	}
}

func TestGetRetrievesExactPoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/collections/memory/points/exact-id" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("with_vector"); got != "true" {
			t.Fatalf("with_vector = %q, want true", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"id":"exact-id","vector":[0.1],"payload":{"text":"target"}}}`))
	}))
	defer server.Close()

	point, found, err := NewClient(server.URL, "memory").Get(context.Background(), "exact-id")
	if err != nil {
		t.Fatal(err)
	}
	if !found || point.ID != "exact-id" || point.Payload["text"] != "target" {
		t.Fatalf("unexpected point: found=%v point=%#v", found, point)
	}
}

func TestGetPreservesLargeNumericPointID(t *testing.T) {
	const id = "9007199254740993"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"id":9007199254740993,"vector":[0.1],"payload":{"recall_count":2}}}`))
	}))
	defer server.Close()

	point, found, err := NewClient(server.URL, "memory").Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !found || point.ID != id {
		t.Fatalf("found=%v ID=%q, want %q", found, point.ID, id)
	}
	if got, ok := point.Payload["recall_count"].(float64); !ok || got != 2 {
		t.Fatalf("payload number = %#v, want established float64 type", point.Payload["recall_count"])
	}
}

func TestScrollPreservesLargeNumericIDsAndOffsets(t *testing.T) {
	const id = "18446744073709551615"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]interface{}
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&body); err != nil {
			t.Fatal(err)
		}
		if requests == 1 {
			_, _ = w.Write([]byte(`{"result":{"points":[{"id":18446744073709551615,"payload":{"chunk_index":1}}],"next_page_offset":18446744073709551615}}`))
			return
		}
		if got, ok := body["offset"].(json.Number); !ok || got.String() != id {
			t.Fatalf("offset = %#v, want exact %s", body["offset"], id)
		}
		_, _ = w.Write([]byte(`{"result":{"points":[],"next_page_offset":null}}`))
	}))
	defer server.Close()

	points, err := NewClient(server.URL, "memory").ScrollAll(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].ID != id {
		t.Fatalf("points = %#v, want exact ID %s", points, id)
	}
	if got, ok := points[0].Payload["chunk_index"].(float64); !ok || got != 1 {
		t.Fatalf("payload number = %#v, want established float64 type", points[0].Payload["chunk_index"])
	}
}

func TestGetReturnsNotFound(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	_, found, err := NewClient(server.URL, "memory").Get(context.Background(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected missing point")
	}
}
