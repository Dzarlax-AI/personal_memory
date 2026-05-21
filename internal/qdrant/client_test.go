package qdrant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQdrantPointID_NumericString(t *testing.T) {
	got := qdrantPointID("12345")
	if got != int64(12345) {
		t.Fatalf("qdrantPointID numeric = %#v, want int64(12345)", got)
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
		_, _ = w.Write([]byte(`{"status":"ok"}`))
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
