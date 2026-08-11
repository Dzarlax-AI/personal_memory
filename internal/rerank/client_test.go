package rerank

import (
	"context"
	"fmt"
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
	client, err := NewClient(server.URL, "model/revision", time.Second, 3)
	if err != nil {
		t.Fatal(err)
	}
	input := []Candidate{{ID: "b", Text: "B"}, {ID: "a", Text: "A"}, {ID: "c", Text: "C"}}
	got, reason := ApplyFailOpen(context.Background(), client, "query", input)
	if reason != "reranker_applied" || !reflect.DeepEqual(got, []Candidate{input[2], input[1], input[0]}) {
		t.Fatalf("got %#v, %q", got, reason)
	}
}

type malformedReranker struct {
	ranked []Ranked
	err    error
}

func (r malformedReranker) Rerank(context.Context, string, []Candidate) ([]Ranked, error) {
	return r.ranked, r.err
}

func TestApplyFailOpenRejectsMalformedInjectedRerankerOutput(t *testing.T) {
	input := []Candidate{{ID: "a", Text: "A"}, {ID: "b", Text: "B"}}
	for _, test := range []struct {
		name   string
		ranked []Ranked
	}{
		{name: "short", ranked: []Ranked{{Index: 0}}},
		{name: "out of range", ranked: []Ranked{{Index: 0}, {Index: 2}}},
		{name: "duplicate", ranked: []Ranked{{Index: 0}, {Index: 0}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, reason := ApplyFailOpen(context.Background(), malformedReranker{ranked: test.ranked}, "query", input)
			if reason != ReasonFallback || !reflect.DeepEqual(got, input) {
				t.Fatalf("got %#v, reason %q", got, reason)
			}
		})
	}
	got, reason := ApplyFailOpen(context.Background(), malformedReranker{err: fmt.Errorf("offline")}, "query", input)
	if reason != ReasonFallback || !reflect.DeepEqual(got, input) {
		t.Fatalf("error fallback = %#v, reason %q", got, reason)
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
