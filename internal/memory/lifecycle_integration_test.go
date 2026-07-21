package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func TestCurrentLifecycleFiltersPreserveExistingConditions(t *testing.T) {
	base := map[string]interface{}{"must": []map[string]interface{}{
		{"key": "namespace", "match": map[string]interface{}{"value": "projects"}},
		{"key": "tags", "match": map[string]interface{}{"value": "memory"}},
	}}
	got := currentLifecycleFilters(base)
	if !reflect.DeepEqual(got["must"], base["must"]) {
		t.Fatalf("must conditions changed: %#v", got["must"])
	}
	wantShould := []map[string]interface{}{
		{"key": "lifecycle_state", "match": map[string]interface{}{"value": "current"}},
		{"is_empty": map[string]interface{}{"key": "lifecycle_state"}},
	}
	if !reflect.DeepEqual(got["should"], wantShould) {
		t.Fatalf("should = %#v, want %#v", got["should"], wantShould)
	}
	if _, mutated := base["should"]; mutated {
		t.Fatal("base filter was mutated")
	}

	baseWithShould := map[string]interface{}{"should": []map[string]interface{}{{"key": "existing", "match": map[string]interface{}{"value": "kept"}}}}
	composed := currentLifecycleFilters(baseWithShould)
	wantComposed := map[string]interface{}{"must": []interface{}{
		baseWithShould,
		map[string]interface{}{"should": wantShould},
	}}
	if !reflect.DeepEqual(composed, wantComposed) {
		t.Fatalf("preexisting should was not AND-composed: got %#v want %#v", composed, wantComposed)
	}
}

func TestLifecycleCandidateLimitAddsBoundedHeadroom(t *testing.T) {
	tests := map[int]int{1: 20, 5: 20, 10: 40, 25: 100, 50: 200, 100: 400}
	for limit, want := range tests {
		if got := lifecycleCandidateLimit(limit); got != want {
			t.Fatalf("lifecycleCandidateLimit(%d) = %d, want %d", limit, got, want)
		}
	}
}

func TestRecallFactsAppliesLifecycleVisibilityRankingAndFilter(t *testing.T) {
	embedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[[0.1,0.2]]`))
	}))
	defer embedServer.Close()

	var mu sync.Mutex
	var searchFilter map[string]interface{}
	searchLimit := 0
	writes := 0
	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/points/search"):
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode search: %v", err)
				return
			}
			mu.Lock()
			searchFilter, _ = body["filter"].(map[string]interface{})
			searchLimit = int(body["limit"].(float64))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"result":[
				{"id":"33333333-3333-3333-3333-333333333333","score":0.99,"payload":{"text":"superseded","namespace":"projects","lifecycle_state":"superseded","superseded_by":["11111111-1111-1111-1111-111111111111"]}},
				{"id":"22222222-2222-2222-2222-222222222222","score":0.90,"payload":{"text":"ordinary","namespace":"projects","lifecycle_state":"current"}},
				{"id":"66666666-6666-6666-6666-666666666666","score":0.80,"payload":{"text":"legacy","namespace":"projects"}},
				{"id":"11111111-1111-1111-1111-111111111111","score":0.70,"payload":{"text":"canonical","namespace":"projects","lifecycle_state":"current","canonical":true,"provenance":{"source":"user","reference":"decision-7"}}},
				{"id":"44444444-4444-4444-4444-444444444444","score":0.95,"payload":{"text":"malformed","namespace":"projects","lifecycle_state":"current","canonical":"yes"}},
				{"id":"55555555-5555-5555-5555-555555555555","score":0.96,"payload":{"text":"expired","namespace":"projects","lifecycle_state":"current","canonical":true,"valid_until":"2020-01-01"}}
			]}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/points/"):
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			_, _ = fmt.Fprintf(w, `{"result":{"id":%q,"payload":{"recall_count":0}}}`, id)
		case strings.HasSuffix(r.URL.Path, "/points/payload"):
			var body struct {
				Payload map[string]interface{} `json:"payload"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode recall payload: %v", err)
				return
			}
			if len(body.Payload) != 2 || body.Payload["recall_count"] == nil || body.Payload["last_recalled_at"] == nil {
				t.Errorf("read path wrote unexpected payload fields: %#v", body.Payload)
			}
			mu.Lock()
			writes++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"result":{"status":"completed"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer qdrantServer.Close()

	srv := NewServer(qdrant.NewClient(qdrantServer.URL, "memory"), embeddings.NewClient(embedServer.URL), NewCache(time.Minute), "test", .97, .60, .90)
	srv.Start(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()

	result, err := srv.recallFacts(context.Background(), toolRequest(map[string]interface{}{
		"query": "architecture", "namespace": "projects", "tags": "memory", "limit": float64(5),
	}))
	if err != nil || result.IsError {
		t.Fatalf("recall failed: result=%#v err=%v", result, err)
	}
	text := toolResultText(t, result)
	for _, excluded := range []string{"superseded\n", "malformed", "expired"} {
		if strings.Contains(text, excluded) {
			t.Fatalf("default recall exposed %q: %s", excluded, text)
		}
	}
	canonicalAt := strings.Index(text, "canonical")
	ordinaryAt := strings.Index(text, "ordinary")
	legacyAt := strings.Index(text, "legacy")
	if canonicalAt < 0 || ordinaryAt < 0 || legacyAt < 0 || canonicalAt >= ordinaryAt || ordinaryAt >= legacyAt {
		t.Fatalf("unexpected lifecycle order: %s", text)
	}
	for _, marker := range []string{"[0.700]", "[0.900]", "[0.800]", "state:current", "canonical", "legacy", "source:user", "reference:decision-7"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("missing %q in recall output: %s", marker, text)
		}
	}

	mu.Lock()
	filter := searchFilter
	gotSearchLimit := searchLimit
	mu.Unlock()
	wantFilter := map[string]interface{}{
		"must": []interface{}{
			map[string]interface{}{"key": "namespace", "match": map[string]interface{}{"value": "projects"}},
			map[string]interface{}{"key": "tags", "match": map[string]interface{}{"value": "memory"}},
		},
		"should": []interface{}{
			map[string]interface{}{"key": "lifecycle_state", "match": map[string]interface{}{"value": "current"}},
			map[string]interface{}{"is_empty": map[string]interface{}{"key": "lifecycle_state"}},
		},
	}
	if !reflect.DeepEqual(filter, wantFilter) {
		t.Fatalf("serialized lifecycle filter = %#v, want %#v", filter, wantFilter)
	}
	if gotSearchLimit != 20 {
		t.Fatalf("search candidate limit = %d, want 20", gotSearchLimit)
	}
	// Recall-count payload writes are expected; no lifecycle metadata is written.
	deadline := time.Now().Add(time.Second)
	gotWrites := 0
	for {
		mu.Lock()
		gotWrites = writes
		mu.Unlock()
		if gotWrites >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if gotWrites != 3 {
		t.Fatalf("recall counter writes = %d, want 3", gotWrites)
	}
}

func TestFindRelatedKeepsHistoryInspectableAndLifecycleOrdered(t *testing.T) {
	embedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[[0.1,0.2]]`))
	}))
	defer embedServer.Close()
	writes := 0
	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/points/search") {
			writes++
			http.Error(w, "unexpected write", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"result":[
			{"id":"4","score":0.95,"payload":{"text":"invalid related","namespace":"projects","lifecycle_state":"wrong"}},
			{"id":"2","score":0.90,"payload":{"text":"historical related","namespace":"projects","lifecycle_state":"historical","verified_at":"2026-07-21T08:30:00Z"}},
			{"id":"3","score":0.80,"payload":{"text":"superseded related","namespace":"projects","lifecycle_state":"superseded","superseded_by":["1"]}},
			{"id":"1","score":0.70,"payload":{"text":"current related","namespace":"projects","lifecycle_state":"current","canonical":true}}
		]}`))
	}))
	defer qdrantServer.Close()
	srv := NewServer(qdrant.NewClient(qdrantServer.URL, "memory"), embeddings.NewClient(embedServer.URL), NewCache(time.Minute), "test", .97, .60, .90)

	result, err := srv.findRelated(context.Background(), toolRequest(map[string]interface{}{"query": "related", "namespace": "projects", "limit": float64(5)}))
	if err != nil || result.IsError {
		t.Fatalf("find related failed: %#v %v", result, err)
	}
	text := toolResultText(t, result)
	currentAt := strings.Index(text, "current related")
	historicalAt := strings.Index(text, "historical related")
	supersededAt := strings.Index(text, "superseded related")
	invalidAt := strings.Index(text, "invalid related")
	if currentAt < 0 || historicalAt < 0 || supersededAt < 0 || invalidAt < 0 || currentAt >= historicalAt || historicalAt >= supersededAt || supersededAt >= invalidAt {
		t.Fatalf("unexpected inspection order: %s", text)
	}
	for _, marker := range []string{"canonical", "state:historical", "verified:2026-07-21T08:30:00Z", "state:superseded", "superseded-by:1", "invalid:lifecycle_state"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("find_related missing %q: %s", marker, text)
		}
	}
	if writes != 0 {
		t.Fatalf("find_related performed %d writes", writes)
	}
}

func TestOperationalContextExcludesNonCurrentEvenWhenPermanent(t *testing.T) {
	var filter map[string]interface{}
	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/points/scroll") {
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		filter, _ = body["filter"].(map[string]interface{})
		_, _ = w.Write([]byte(`{"result":{"points":[
			{"id":"1","payload":{"text":"legacy current","namespace":"projects","permanent":true}},
			{"id":"2","payload":{"text":"canonical current","namespace":"projects","permanent":true,"lifecycle_state":"current","canonical":true}},
			{"id":"3","payload":{"text":"permanent historical","namespace":"projects","permanent":true,"lifecycle_state":"historical"}},
			{"id":"4","payload":{"text":"expired canonical","namespace":"projects","permanent":true,"lifecycle_state":"current","canonical":true,"valid_until":"2020-01-01"}},
			{"id":"5","payload":{"text":"malformed current","namespace":"projects","permanent":true,"lifecycle_state":"current","canonical":"yes"}},
			{"id":"6","payload":{"text":"canonical low recall","namespace":"projects","lifecycle_state":"current","canonical":true,"recall_count":1}},
			{"id":"7","payload":{"text":"ordinary high recall","namespace":"projects","lifecycle_state":"current","recall_count":99}}
		],"next_page_offset":null}}`))
	}))
	defer qdrantServer.Close()
	srv := &Server{qdrant: qdrant.NewClient(qdrantServer.URL, "memory"), cache: NewCache(time.Minute)}

	result, err := srv.getOperationalContext(context.Background(), toolRequest(map[string]interface{}{"namespace": "projects", "top_recalled": float64(1)}))
	if err != nil || result.IsError {
		t.Fatalf("operational context failed: result=%#v err=%v", result, err)
	}
	text := toolResultText(t, result)
	if !strings.Contains(text, "canonical current") || !strings.Contains(text, "legacy current") || !strings.Contains(text, "canonical low recall") || !strings.Contains(text, "canonical") || !strings.Contains(text, "legacy") {
		t.Fatalf("current lifecycle labels missing: %s", text)
	}
	for _, excluded := range []string{"permanent historical", "expired canonical", "malformed current", "ordinary high recall"} {
		if strings.Contains(text, excluded) {
			t.Fatalf("operational context exposed %q: %s", excluded, text)
		}
	}
	canonicalLowAt := strings.Index(text, "canonical low recall")
	canonicalPermanentAt := strings.Index(text, "canonical current")
	legacyAt := strings.Index(text, "legacy current")
	if canonicalLowAt >= canonicalPermanentAt || canonicalPermanentAt >= legacyAt {
		t.Fatalf("operational context is not ordered by authority then recall count: %s", text)
	}
	if filter == nil || filter["should"] == nil {
		t.Fatalf("operational filter missing lifecycle should: %#v", filter)
	}
}

func TestOperationalContextSkipsNeverRecalledCanonicalWithoutStoppingSelection(t *testing.T) {
	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/points/scroll") {
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"result":{"points":[
			{"id":"1","payload":{"text":"never recalled canonical","namespace":"projects","lifecycle_state":"current","canonical":true,"recall_count":0}},
			{"id":"2","payload":{"text":"recalled ordinary","namespace":"projects","lifecycle_state":"current","recall_count":99}}
		],"next_page_offset":null}}`))
	}))
	defer qdrantServer.Close()
	srv := &Server{qdrant: qdrant.NewClient(qdrantServer.URL, "memory"), cache: NewCache(time.Minute)}

	result, err := srv.getOperationalContext(context.Background(), toolRequest(map[string]interface{}{"namespace": "projects", "top_recalled": float64(1)}))
	if err != nil || result.IsError {
		t.Fatalf("operational context failed: result=%#v err=%v", result, err)
	}
	text := toolResultText(t, result)
	if strings.Contains(text, "never recalled canonical") {
		t.Fatalf("operational context included never-recalled non-permanent fact: %s", text)
	}
	if !strings.Contains(text, "recalled ordinary") {
		t.Fatalf("zero-recall canonical stopped selection before recalled fact: %s", text)
	}
}

func TestHistoryViewsExposeLifecycleAndStatsRemainDeterministic(t *testing.T) {
	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/points/scroll") {
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"result":{"points":[
			{"id":"1","payload":{"text":"legacy","namespace":"projects","created_at":"2026-01-01T00:00:00Z"}},
			{"id":"2","payload":{"text":"current","namespace":"projects","lifecycle_state":"current"}},
			{"id":"3","payload":{"text":"historical","namespace":"projects","lifecycle_state":"historical"}},
			{"id":"4","payload":{"text":"superseded","namespace":"projects","lifecycle_state":"superseded","superseded_by":["2"]}},
			{"id":"5","payload":{"text":"disputed","namespace":"projects","lifecycle_state":"disputed"}},
			{"id":"6","payload":{"text":"invalid","namespace":"projects","lifecycle_state":"wrong"}}
		],"next_page_offset":null}}`))
	}))
	defer qdrantServer.Close()
	srv := &Server{qdrant: qdrant.NewClient(qdrantServer.URL, "memory"), cache: NewCache(time.Minute)}

	listed, err := srv.listFacts(context.Background(), toolRequest(map[string]interface{}{"namespace": "projects"}))
	if err != nil || listed.IsError {
		t.Fatalf("list failed: %#v %v", listed, err)
	}
	listText := toolResultText(t, listed)
	for _, marker := range []string{"state:current legacy", "state:historical", "state:superseded", "superseded-by:2", "state:disputed", "invalid:lifecycle_state"} {
		if !strings.Contains(listText, marker) {
			t.Fatalf("list missing %q: %s", marker, listText)
		}
	}

	stats, err := srv.getStats(context.Background(), toolRequest(nil))
	if err != nil || stats.IsError {
		t.Fatalf("stats failed: %#v %v", stats, err)
	}
	statsText := toolResultText(t, stats)
	for _, line := range []string{"  current: 2", "  historical: 1", "  superseded: 1", "  disputed: 1", "  legacy (no lifecycle fields): 1", "  invalid explicit metadata: 1"} {
		if !strings.Contains(statsText, line) {
			t.Fatalf("stats missing %q: %s", line, statsText)
		}
	}

	exported, err := srv.exportFacts(context.Background(), toolRequest(map[string]interface{}{"namespace": "projects"}))
	if err != nil || exported.IsError {
		t.Fatalf("export failed: %#v %v", exported, err)
	}
	var facts []map[string]interface{}
	if err := json.Unmarshal([]byte(toolResultText(t, exported)), &facts); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if len(facts) != 6 {
		t.Fatalf("exported %d facts, want 6", len(facts))
	}
	if _, rewritten := facts[0]["lifecycle_state"]; rewritten {
		t.Fatalf("legacy export was rewritten: %#v", facts[0])
	}
	if facts[3]["lifecycle_state"] != "superseded" || !reflect.DeepEqual(facts[3]["superseded_by"], []interface{}{"2"}) {
		t.Fatalf("explicit lifecycle export changed: %#v", facts[3])
	}
	if facts[5]["lifecycle_state"] != "wrong" {
		t.Fatalf("invalid explicit state was rewritten: %#v", facts[5])
	}
}
