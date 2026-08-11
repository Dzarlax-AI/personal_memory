package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/mark3labs/mcp-go/server"
)

func TestRecallFactsToolDeclaresLifecycleContract(t *testing.T) {
	memoryServer := &Server{}
	mcpServer := server.NewMCPServer("test", "1.0")
	memoryServer.RegisterTools(mcpServer)
	registered := mcpServer.GetTool("recall_facts")
	if registered == nil {
		t.Fatal("recall_facts was not registered")
	}
	tool := registered.Tool
	if tool.OutputSchema.Type != "object" {
		t.Fatalf("output schema type = %q, want object", tool.OutputSchema.Type)
	}
	for _, name := range []string{"lifecycle_mode", "as_of"} {
		property, ok := tool.InputSchema.Properties[name].(map[string]interface{})
		if !ok || property["type"] != "string" || property["description"] == "" {
			t.Fatalf("%s schema = %#v", name, tool.InputSchema.Properties[name])
		}
	}
	description := strings.ToLower(tool.Description)
	for _, phrase := range []string{"explicit", "default", "current", "history", "as_of", "include_all", "disputed", "uncertain", "expiry", "historical intervals"} {
		if !strings.Contains(description, phrase) {
			t.Errorf("tool description missing %q: %q", phrase, tool.Description)
		}
	}
}

func TestRecallFactsLifecycleModesControlFiltersAndResults(t *testing.T) {
	tests := []struct {
		name          string
		args          map[string]interface{}
		wantMode      RecallLifecycleMode
		wantAsOf      string
		wantTexts     []string
		wantLifecycle bool
	}{
		{name: "omitted defaults current", args: map[string]interface{}{}, wantMode: RecallLifecycleCurrent, wantTexts: []string{"current"}, wantLifecycle: true},
		{name: "history", args: map[string]interface{}{"lifecycle_mode": "history"}, wantMode: RecallLifecycleHistory, wantTexts: []string{"current", "disputed", "historical", "superseded"}},
		{name: "as of", args: map[string]interface{}{"lifecycle_mode": "as_of", "as_of": "2025-01-01"}, wantMode: RecallLifecycleAsOf, wantAsOf: "2025-01-01", wantTexts: []string{"current", "expired-now", "disputed", "historical", "superseded"}},
		{name: "include all", args: map[string]interface{}{"lifecycle_mode": "include_all"}, wantMode: RecallLifecycleIncludeAll, wantTexts: []string{"current", "disputed", "historical", "superseded"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var searchFilter map[string]interface{}
			srv := newLifecycleRecallResponseServer(t, func(body map[string]interface{}) {
				searchFilter, _ = body["filter"].(map[string]interface{})
			})
			args := map[string]interface{}{"query": "fact", "namespace": "projects", "limit": float64(10)}
			for key, value := range tt.args {
				args[key] = value
			}
			result, err := srv.recallFacts(context.Background(), toolRequest(args))
			if err != nil || result.IsError {
				t.Fatalf("recall failed: result=%#v err=%v", result, err)
			}
			structured, ok := result.StructuredContent.(RecallFactsResult)
			if !ok {
				t.Fatalf("structured type = %T", result.StructuredContent)
			}
			if structured.LifecycleMode != tt.wantMode || structured.AsOf != tt.wantAsOf {
				t.Fatalf("options = %#v", structured)
			}
			gotTexts := make([]string, len(structured.Facts))
			for index, fact := range structured.Facts {
				gotTexts[index] = fact.Text
			}
			if !reflect.DeepEqual(gotTexts, tt.wantTexts) {
				t.Fatalf("texts = %v, want %v", gotTexts, tt.wantTexts)
			}
			_, hasLifecycleFilter := searchFilter["should"]
			if hasLifecycleFilter != tt.wantLifecycle {
				t.Fatalf("filter = %#v, lifecycle should present=%v", searchFilter, tt.wantLifecycle)
			}
		})
	}
}

func TestRecallFactsStructuredContentIsNormalizedAndFallbackHidesPointIDs(t *testing.T) {
	srv := newLifecycleRecallResponseServer(t, nil)
	result, err := srv.recallFacts(context.Background(), toolRequest(map[string]interface{}{
		"query": "fact", "lifecycle_mode": "include_all", "limit": float64(10),
	}))
	if err != nil || result.IsError {
		t.Fatalf("recall failed: result=%#v err=%v", result, err)
	}
	structured := result.StructuredContent.(RecallFactsResult)
	if structured.Count != len(structured.Facts) || structured.Facts == nil {
		t.Fatalf("count/facts = %#v", structured)
	}
	if got := structured.Facts[0]; got.PointID != "11111111-1111-1111-1111-111111111111" || got.SemanticScore != 0.91 || got.SemanticRank != 1 || got.FinalRank != 1 || got.Tags == nil || got.ReasonCodes == nil {
		t.Fatalf("current fact = %#v", got)
	}
	var disputed RecallFact
	for _, fact := range structured.Facts {
		if fact.Text == "disputed" {
			disputed = fact
			break
		}
	}
	if disputed.PointID != "42" || disputed.Decision != LifecycleDecisionUncertain || !reflect.DeepEqual(disputed.ReasonCodes, []LifecycleReasonCode{LifecycleReasonDisputed}) {
		t.Fatalf("disputed fact = %#v", disputed)
	}
	if disputed.Lifecycle.State != lifecycle.Disputed || disputed.Lifecycle.Provenance == nil || disputed.Lifecycle.Provenance.Source != "user" || disputed.Lifecycle.Supersedes == nil || disputed.Lifecycle.SupersededBy == nil {
		t.Fatalf("normalized lifecycle = %#v", disputed.Lifecycle)
	}
	for _, fact := range structured.Facts {
		if fact.Tags == nil || fact.ReasonCodes == nil || fact.Lifecycle.Supersedes == nil || fact.Lifecycle.SupersededBy == nil {
			t.Fatalf("nil closed array in %#v", fact)
		}
	}
	fallback := toolResultText(t, result)
	for _, id := range []string{"11111111-1111-1111-1111-111111111111", "42"} {
		if strings.Contains(fallback, id) {
			t.Fatalf("point ID %q leaked into fallback: %s", id, fallback)
		}
	}
	for _, marker := range []string{"[0.910]", "['memory']", "ns:projects", "recalls:1", "state:disputed", "source:user", "disputed"} {
		if !strings.Contains(fallback, marker) {
			t.Errorf("fallback missing %q: %s", marker, fallback)
		}
	}
}

func TestRecallFactsCacheIsolatedByLifecycleIdentityAndHitParity(t *testing.T) {
	var mu sync.Mutex
	searches := 0
	srv := newLifecycleRecallResponseServer(t, func(map[string]interface{}) {
		mu.Lock()
		searches++
		mu.Unlock()
	})
	call := func(args map[string]interface{}) (*RecallFactsResult, string) {
		t.Helper()
		args["query"] = "fact"
		args["limit"] = float64(1)
		result, err := srv.recallFacts(context.Background(), toolRequest(args))
		if err != nil || result.IsError {
			t.Fatalf("recall failed: %#v %v", result, err)
		}
		structured := result.StructuredContent.(RecallFactsResult)
		return &structured, toolResultText(t, result)
	}
	first, firstText := call(map[string]interface{}{})
	second, secondText := call(map[string]interface{}{})
	if second.Facts[0].RecallCount != first.Facts[0].RecallCount+1 || firstText == secondText {
		t.Fatalf("cache hit did not advance visible recall count: first=%#v second=%#v", first, second)
	}
	if formatRecallFactsResult(*second) != secondText {
		t.Fatalf("structured/text drift: %#v text=%q", second, secondText)
	}
	call(map[string]interface{}{"lifecycle_mode": "history"})
	call(map[string]interface{}{"lifecycle_mode": "as_of", "as_of": "2025-01-01"})
	call(map[string]interface{}{"lifecycle_mode": "as_of", "as_of": "2025-01-02"})
	mu.Lock()
	gotSearches := searches
	mu.Unlock()
	if gotSearches != 4 {
		t.Fatalf("searches = %d, want 4 isolated cache entries", gotSearches)
	}
}

func TestRecallFactsCacheKeyUsesCanonicalTypedIdentity(t *testing.T) {
	options := LifecycleRecallOptions{Mode: RecallLifecycleCurrent}
	if recallFactsCacheKey("a|b", "c", nil, 5, options) == recallFactsCacheKey("a", "b|c", nil, 5, options) {
		t.Fatal("query and namespace delimiter collision")
	}
	if recallFactsCacheKey("query", "projects", []string{"a b"}, 5, options) == recallFactsCacheKey("query", "projects", []string{"a", "b"}, 5, options) {
		t.Fatal("single spaced tag collided with two tags")
	}
	tags := []string{"b", "a"}
	first := recallFactsCacheKey("query", "projects", tags, 5, options)
	second := recallFactsCacheKey("query", "projects", []string{"a", "b"}, 5, options)
	if first != second {
		t.Fatalf("reordered tag set has different identity:\n%s\n%s", first, second)
	}
	if first != recallFactsCacheKey("query", "projects", []string{"a", "b", "a"}, 5, options) {
		t.Fatal("duplicate member changed tag-set cache identity")
	}
	if !reflect.DeepEqual(tags, []string{"b", "a"}) {
		t.Fatalf("cache-key canonicalization mutated caller tags: %v", tags)
	}
}

func TestRecallFactsCacheReturnsMutationSafeCopies(t *testing.T) {
	cache := NewCache(time.Minute)
	key := "recall"
	original := RecallFactsResult{
		Count:         1,
		LifecycleMode: RecallLifecycleHistory,
		Facts: []RecallFact{{
			PointID:     "42",
			Tags:        []string{"memory"},
			ReasonCodes: []LifecycleReasonCode{LifecycleReasonHistoricalContext},
			Lifecycle: lifecycle.View{
				State:        lifecycle.Historical,
				Provenance:   &lifecycle.Provenance{Source: "user"},
				Supersedes:   []string{"1"},
				SupersededBy: []string{},
				Valid:        true,
			},
		}},
	}
	cache.SetRecall(key, original)
	first, ok := cache.GetRecall(key)
	if !ok {
		t.Fatal("expected cache hit")
	}
	first.Facts[0].Tags[0] = "changed"
	first.Facts[0].ReasonCodes[0] = LifecycleReasonDisputed
	first.Facts[0].Lifecycle.Provenance.Source = "changed"
	first.Facts[0].Lifecycle.Supersedes[0] = "changed"
	second, ok := cache.GetRecall(key)
	if !ok {
		t.Fatal("expected second cache hit")
	}
	if second.Facts[0].Tags[0] != "memory" || second.Facts[0].ReasonCodes[0] != LifecycleReasonHistoricalContext || second.Facts[0].Lifecycle.Provenance.Source != "user" || second.Facts[0].Lifecycle.Supersedes[0] != "1" {
		t.Fatalf("cached result was mutated through caller copy: %#v", second)
	}
}

func TestRecallFactsCacheAtomicUpdatePreservesTimestampAndFailureState(t *testing.T) {
	cache := NewCache(time.Minute)
	key := "recall"
	cache.SetRecall(key, RecallFactsResult{Facts: []RecallFact{{RecallCount: 4}}})
	insertedAt := cache.recalls[key].timestamp
	updated, flight, err := cache.AcquireRecall(context.Background(), key, func(result *RecallFactsResult) error {
		result.Facts[0].RecallCount++
		return nil
	})
	if err != nil || flight != nil || updated.Facts[0].RecallCount != 5 {
		t.Fatalf("atomic update = %#v flight=%p err=%v", updated, flight, err)
	}
	if got := cache.recalls[key].timestamp; !got.Equal(insertedAt) {
		t.Fatalf("cache hit slid insertion timestamp: got %s want %s", got, insertedAt)
	}

	cache.SetRecall(key, RecallFactsResult{Facts: []RecallFact{{RecallCount: 9}}})
	wantErr := errors.New("enqueue failed")
	_, flight, err = cache.AcquireRecall(context.Background(), key, func(result *RecallFactsResult) error {
		result.Facts[0].RecallCount++
		return wantErr
	})
	if flight != nil || !errors.Is(err, wantErr) {
		t.Fatalf("failed update = flight=%p err=%v", flight, err)
	}
	unchanged, ok := cache.GetRecall(key)
	if !ok || unchanged.Facts[0].RecallCount != 9 {
		t.Fatalf("failed callback advanced cache: %#v ok=%v", unchanged, ok)
	}
}

func TestRecallFactsRejectsLifecycleOptionsBeforeEmbeddingOrSearch(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer server.Close()
	srv := NewServer(qdrant.NewClient(server.URL, "memory"), embeddings.NewClient(server.URL), NewCache(time.Minute), "test", .97, .60, .90)
	for _, args := range []map[string]interface{}{
		{"query": "fact", "lifecycle_mode": "wrong"},
		{"query": "fact", "lifecycle_mode": "as_of"},
		{"query": "fact", "lifecycle_mode": "current", "as_of": "2025-01-01"},
	} {
		result, err := srv.recallFacts(context.Background(), toolRequest(args))
		if err != nil || !result.IsError {
			t.Fatalf("expected validation error: %#v %v", result, err)
		}
	}
	if requests != 0 {
		t.Fatalf("validation issued %d network requests", requests)
	}
}

func newLifecycleRecallResponseServer(t *testing.T, observeSearch func(map[string]interface{})) *Server {
	t.Helper()
	embedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[[0.1,0.2]]`))
	}))
	t.Cleanup(embedServer.Close)

	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/points/search"):
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode search: %v", err)
				return
			}
			if observeSearch != nil {
				observeSearch(body)
			}
			_, _ = w.Write([]byte(`{"result":[
				{"id":"11111111-1111-1111-1111-111111111111","score":0.91,"payload":{"text":"current","namespace":"projects","tags":["memory"],"primary_tag":"memory","recall_count":0,"lifecycle_state":"current","canonical":true}},
				{"id":"22222222-2222-2222-2222-222222222222","score":0.90,"payload":{"text":"historical","namespace":"projects","lifecycle_state":"historical"}},
				{"id":"33333333-3333-3333-3333-333333333333","score":0.89,"payload":{"text":"superseded","namespace":"projects","lifecycle_state":"superseded","superseded_by":["11111111-1111-1111-1111-111111111111"]}},
				{"id":42,"score":0.88,"payload":{"text":"disputed","namespace":"projects","lifecycle_state":"disputed","provenance":{"source":"user"}}},
				{"id":"55555555-5555-5555-5555-555555555555","score":0.87,"payload":{"text":"expired-now","namespace":"projects","valid_until":"2025-06-01"}}
			]}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/points/"):
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			_, _ = fmt.Fprintf(w, `{"result":{"id":%q,"payload":{"recall_count":0}}}`, id)
		case strings.HasSuffix(r.URL.Path, "/points/payload"):
			_, _ = w.Write([]byte(`{"result":{"status":"completed"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	t.Cleanup(qdrantServer.Close)

	srv := NewServer(qdrant.NewClient(qdrantServer.URL, "memory"), embeddings.NewClient(embedServer.URL), NewCache(time.Minute), "test", .97, .60, .90)
	srv.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return srv
}
