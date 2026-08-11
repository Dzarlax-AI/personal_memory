package rerank

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestApplyFailOpenPreservesExactOrderOnMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"index":0,"score":1}]`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "model/revision", time.Second, 2)
	if err != nil {
		t.Fatal(err)
	}
	input := []Candidate{{ID: "b", Text: "B"}, {ID: "a", Text: "A"}}
	got, reason := ApplyFailOpen(context.Background(), client, "query", input)
	if reason != "reranker_fallback" || !reflect.DeepEqual(got, input) {
		t.Fatalf("got %#v, %q", got, reason)
	}
}

func TestClientSortsScoresAndDeterministicTies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"index":0,"score":0.5},{"index":1,"score":0.5},{"index":2,"score":0.9}]`))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "model/revision", time.Second, 3)
	input := []Candidate{{ID: "b", Text: "B"}, {ID: "a", Text: "A"}, {ID: "c", Text: "C"}}
	got, reason := ApplyFailOpen(context.Background(), client, "query", input)
	if reason != "reranker_applied" || !reflect.DeepEqual(got, []Candidate{input[2], input[1], input[0]}) {
		t.Fatalf("got %#v, %q", got, reason)
	}
}

func TestNewClientRejectsUnsafeOrUnboundedConfiguration(t *testing.T) {
	for _, tc := range []struct {
		url, model string
		timeout    time.Duration
		cap        int
	}{
		{"http://user:secret@example.test", "m", time.Second, 1},
		{"http://example.test", "", time.Second, 1},
		{"http://example.test", "m", 0, 1},
		{"http://example.test", "m", time.Second, MaxCandidates + 1},
	} {
		if _, err := NewClient(tc.url, tc.model, tc.timeout, tc.cap); err == nil {
			t.Fatalf("accepted %#v", tc)
		}
	}
}
