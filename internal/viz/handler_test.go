package viz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/go-chi/chi/v5"
)

func TestGraphAndDuplicatesRejectInvalidBoundsBeforeLoadingPoints(t *testing.T) {
	h := NewHandler(nil, 0.65)
	tests := []string{
		"/api/graph?threshold=NaN",
		"/api/graph?threshold=-0.1",
		"/api/graph?max_edges=0",
		"/api/graph?max_edges=5001",
		"/api/graph?max_nodes=0",
		"/api/duplicates?threshold=Infinity",
		"/api/duplicates?max_nodes=5001",
		"/api/duplicates?max_pairs=-1",
	}
	for _, target := range tests {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("GET %s: got %d (%s), want 400", target, rr.Code, rr.Body.String())
		}
	}
}

func TestGraphAndDuplicatesRejectDatasetsOverMaxNodes(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[`)
		for i := 0; i < 3; i++ {
			if i > 0 {
				mustWriteTestResponse(t, w, ",")
			}
			mustWriteTestResponse(t, w, `{"id":"%d","vector":[1,0],"payload":{"text":"fact %d"}}`, i, i)
		}
		mustWriteTestResponse(t, w, `],"next_page_offset":null}}`)
	}))
	defer backend.Close()

	h := NewHandler(qdrant.NewClient(backend.URL, "memory"), 0.65)
	for _, target := range []string{"/api/graph?max_nodes=2", "/api/duplicates?max_nodes=2"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, req)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("GET %s: got %d (%s), want 422", target, rr.Code, rr.Body.String())
		}
	}
}

func mustWriteTestResponse(t *testing.T, w http.ResponseWriter, format string, args ...interface{}) {
	t.Helper()
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		t.Fatalf("write test response: %v", err)
	}
}

func TestScrollGraphPointsPagesFiltersAndStopsAtBound(t *testing.T) {
	calls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			mustWriteTestResponse(t, w, `{"result":{"points":[{"id":"ignored","vector":[1,0],"payload":{"namespace":"work"}},{"id":"1","vector":[1,0],"payload":{"namespace":"projects"}}],"next_page_offset":"page-2"}}`)
		case 2:
			mustWriteTestResponse(t, w, `{"result":{"points":[{"id":"2","vector":[1,0],"payload":{"namespace":"projects"}},{"id":"3","vector":[1,0],"payload":{"namespace":"projects"}}],"next_page_offset":"page-3"}}`)
		default:
			t.Fatal("scroll continued after finding max_nodes+1 matching points")
		}
	}))
	defer backend.Close()

	h := NewHandler(qdrant.NewClient(backend.URL, "memory"), 0.65)
	points, tooMany, err := h.scrollGraphPoints(context.Background(), 2, "projects", "", "", "")
	if err != nil {
		t.Fatalf("scrollGraphPoints: %v", err)
	}
	if !tooMany {
		t.Fatal("tooMany = false, want true")
	}
	if calls != 2 || len(points) != 2 || points[0].ID != "1" || points[1].ID != "2" {
		t.Fatalf("calls=%d points=%#v, want two pages and matching points 1,2", calls, points)
	}
}

func TestScrollGraphPointsStopsSparseScanAtHardBound(t *testing.T) {
	calls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[{"id":"%d-a","vector":[1,0],"payload":{"text":"present"}},{"id":"%d-b","vector":[1,0],"payload":{"text":"present"}}],"next_page_offset":"page-%d"}}`, calls, calls, calls+1)
	}))
	defer backend.Close()

	h := NewHandler(qdrant.NewClient(backend.URL, "memory"), 0.65)
	_, _, err := h.scrollGraphPointsWithLimit(context.Background(), 2, 3, "", "", "", "missing")
	if !errors.Is(err, errGraphScanLimit) {
		t.Fatalf("scrollGraphPointsWithLimit error = %v, want errGraphScanLimit", err)
	}
	if calls != 2 {
		t.Fatalf("scroll calls = %d, want early stop after 2 bounded pages", calls)
	}
}

func TestScrollGraphPointsPushesRepresentableFiltersToQdrant(t *testing.T) {
	var requestBody map[string]interface{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[],"next_page_offset":null}}`)
	}))
	defer backend.Close()

	h := NewHandler(qdrant.NewClient(backend.URL, "memory"), 0.65)
	if _, _, err := h.scrollGraphPoints(context.Background(), 10, "projects", "memory", "personal-memory", "missing"); err != nil {
		t.Fatalf("scrollGraphPoints: %v", err)
	}
	filter, ok := requestBody["filter"].(map[string]interface{})
	if !ok {
		t.Fatalf("request filter = %#v, want object", requestBody["filter"])
	}
	must, ok := filter["must"].([]interface{})
	if !ok || len(must) != 3 {
		t.Fatalf("filter.must = %#v, want namespace/tag/primary_tag conditions", filter["must"])
	}
	encoded, err := json.Marshal(filter)
	if err != nil {
		t.Fatalf("marshal filter: %v", err)
	}
	for _, want := range []string{`"namespace"`, `"tags"`, `"primary_tag"`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("Qdrant filter %s does not contain %s", encoded, want)
		}
	}
	if strings.Contains(string(encoded), `"text"`) {
		t.Fatalf("local text-state filter leaked into Qdrant filter: %s", encoded)
	}
}

func TestQdrantGraphFiltersKeepsMissingNamespaceLocal(t *testing.T) {
	filter := qdrantGraphFilters("__missing__", "memory", "")
	encoded, err := json.Marshal(filter)
	if err != nil {
		t.Fatalf("marshal filter: %v", err)
	}
	if strings.Contains(string(encoded), `"namespace"`) {
		t.Fatalf("missing-namespace sentinel must stay a local filter: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"tags"`) {
		t.Fatalf("representable tag filter was not pushed to Qdrant: %s", encoded)
	}
}

func TestGraphComputationErrorsHaveBoundedStatuses(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want int
	}{
		{err: errGraphScanLimit, want: http.StatusUnprocessableEntity},
		{err: context.DeadlineExceeded, want: http.StatusRequestTimeout},
	} {
		rr := httptest.NewRecorder()
		writeGraphComputationError(rr, tt.err)
		if rr.Code != tt.want {
			t.Errorf("error %v: status = %d, want %d", tt.err, rr.Code, tt.want)
		}
	}
}

func TestSimilarityScansHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	points := []qdrant.ScrollPoint{{ID: "1", Vector: []float32{1, 0}}, {ID: "2", Vector: []float32{1, 0}}}
	if _, err := strongestGraphEdges(ctx, points, 0.5, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("strongestGraphEdges error = %v, want context.Canceled", err)
	}
	if _, err := strongestDuplicates(ctx, points, 0.5, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("strongestDuplicates error = %v, want context.Canceled", err)
	}
}

func TestDuplicatesUIHandlesErrorsAndOffersBoundedRetry(t *testing.T) {
	js, err := staticFS.ReadFile("static/assets/js/duplicates.js")
	if err != nil {
		t.Fatalf("read duplicates.js: %v", err)
	}
	source := string(js)
	for _, want := range []string{"if (!res.ok)", "await res.text()", "loadDuplicates(5000)", "Retry"} {
		if !strings.Contains(source, want) {
			t.Errorf("duplicates.js does not contain %q", want)
		}
	}
}

func TestStrongestEdgeHeapIsBoundedAndDeterministic(t *testing.T) {
	h := &edgeHeap{}
	for _, edge := range []graphEdge{
		{From: "b", To: "c", Similarity: 0.8},
		{From: "a", To: "b", Similarity: 0.9},
		{From: "a", To: "c", Similarity: 0.9},
	} {
		keepStrongestEdge(h, edge, 2)
	}
	if len(*h) != 2 {
		t.Fatalf("heap len = %d, want 2", len(*h))
	}
	for _, edge := range *h {
		if edge.Similarity != 0.9 {
			t.Fatalf("kept weak edge: %#v", edge)
		}
	}
}

func TestUpdateTagsRejectsUnknownOversizedAndInvalidPrimary(t *testing.T) {
	h := NewHandler(nil, 0.65)
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "unknown field", body: `{"tags":["health"],"extra":true}`, want: http.StatusBadRequest},
		{name: "oversized primary", body: `{"tags":["health"],"primary_tag":"` + strings.Repeat("x", maxFactTagLength+1) + `"}`, want: http.StatusBadRequest},
		{name: "too many tags", body: `{"tags":["` + strings.Repeat(`x","`, maxFactTags) + `x"]}`, want: http.StatusBadRequest},
		{name: "oversized", body: `{"tags":["` + strings.Repeat("x", maxTagsBodyBytes) + `"]}`, want: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/api/facts/id/tags", strings.NewReader(tt.body))
			req.Header.Set("X-Viz-Action", "update-tags")
			rr := httptest.NewRecorder()
			h.Router().ServeHTTP(rr, req)
			if rr.Code != tt.want {
				t.Fatalf("got %d (%s), want %d", rr.Code, rr.Body.String(), tt.want)
			}
		})
	}
}

func TestBuildShellHTML_Succeeds(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	if len(html) == 0 {
		t.Fatal("composed HTML is empty")
	}
}

func TestBuildShellHTML_PlaceholderReplaced(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	if strings.Contains(string(html), "<!-- VIEWS -->") {
		t.Error("placeholder <!-- VIEWS --> remains in composed HTML")
	}
}

func TestBuildShellHTML_AllViewContainersPresent(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	s := string(html)
	for _, name := range viewNames {
		marker := `id="` + name + `-view"`
		if !strings.Contains(s, marker) {
			t.Errorf("composed HTML is missing view container %q", marker)
		}
	}
}

func TestBuildShellHTML_AllTabsPresent(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	s := string(html)
	for _, name := range viewNames {
		marker := `data-view="` + name + `"`
		if !strings.Contains(s, marker) {
			t.Errorf("composed HTML is missing tab button %q", marker)
		}
	}
}

func TestBuildShellHTML_AssetsReferenced(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	s := string(html)
	for _, js := range []string{"shared.js", "overview.js", "duplicates.js", "forgotten.js", "timeline.js", "graph.js", "documents.js", "init.js"} {
		if !strings.Contains(s, "/viz/assets/js/"+js) {
			t.Errorf("shell does not reference %s", js)
		}
	}
	if !strings.Contains(s, "/viz/assets/styles.css") {
		t.Error("shell does not reference styles.css")
	}
	if !strings.Contains(s, "/viz/assets/vendor/dzarlax.css") {
		t.Error("shell does not reference the design-system bundle")
	}
	for _, asset := range []string{
		"/viz/assets/vendor/vis-network.min.js",
		"/viz/assets/vendor/vis-timeline-graph2d.min.js",
		"/viz/assets/vendor/vis-timeline-graph2d.min.css",
	} {
		if !strings.Contains(s, asset) {
			t.Errorf("shell does not reference %s", asset)
		}
	}
}

func TestBuildShellHTML_NoRuntimeCDNReferences(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	s := string(html)
	for _, blocked := range []string{"https://unpkg.com/", "https://cdn.jsdelivr.net/", "https://statically.io/"} {
		if strings.Contains(s, blocked) {
			t.Errorf("shell contains runtime CDN reference %q", blocked)
		}
	}
}

func TestBuildShellHTML_DarkModeDefault(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	if !strings.Contains(string(html), `dark-mode`) {
		t.Error("shell should opt into the design-system dark theme via the dark-mode attribute")
	}
}

func TestNewHandler_ComposesHTMLAtConstruction(t *testing.T) {
	h := NewHandler(nil, 0.65)
	if len(h.composedHTML) == 0 {
		t.Fatal("NewHandler must compose HTML eagerly")
	}
}

// Regression: assets 404'd for two different reasons.
//  1. StripPrefix("/assets/") with a trailing slash made FileServer receive
//     a path without a leading "/" → 404.
//  2. chi.Mount does not rewrite r.URL.Path, only RoutePath, so any
//     StripPrefix call that assumes the URL is already stripped of the
//     mount prefix silently fails.
//
// This test mounts the router at /viz like production does, so both
// regressions would reproduce here.
func TestAssetRouter_ServesEmbeddedFiles(t *testing.T) {
	h := NewHandler(nil, 0.65)
	main := chi.NewRouter()
	main.Mount("/viz", h.Router())

	for _, asset := range []string{"/viz/assets/styles.css", "/viz/assets/js/init.js", "/viz/assets/vendor/dzarlax.css"} {
		req := httptest.NewRequest(http.MethodGet, asset, nil)
		rr := httptest.NewRecorder()
		main.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s: got %d, want 200", asset, rr.Code)
		}
	}
}

func TestFilterGraphPoints_AppliesNamespaceAndTagBeforeEdgeLimit(t *testing.T) {
	points := []qdrant.ScrollPoint{
		{ID: "1", Payload: map[string]interface{}{"namespace": "projects", "tags": []interface{}{"personal-memory"}}},
		{ID: "2", Payload: map[string]interface{}{"namespace": "projects", "tags": []interface{}{"health"}}},
		{ID: "3", Payload: map[string]interface{}{"namespace": "work", "tags": []interface{}{"personal-memory"}}},
	}

	filtered := filterGraphPoints(points, "projects", "personal-memory", "", "")
	if len(filtered) != 1 {
		t.Fatalf("len(filtered) = %d, want 1", len(filtered))
	}
	if filtered[0].ID != "1" {
		t.Fatalf("filtered[0].ID = %q, want %q", filtered[0].ID, "1")
	}
}

func TestFilterGraphPoints_MatchesMissingNamespaceSentinel(t *testing.T) {
	points := []qdrant.ScrollPoint{
		{ID: "1", Payload: map[string]interface{}{"namespace": nil}},
		{ID: "2", Payload: map[string]interface{}{"namespace": ""}},
		{ID: "3", Payload: map[string]interface{}{"namespace": "null"}},
		{ID: "4", Payload: map[string]interface{}{"namespace": "projects"}},
	}

	filtered := filterGraphPoints(points, "__missing__", "", "", "")
	if len(filtered) != 3 {
		t.Fatalf("len(filtered) = %d, want 3", len(filtered))
	}
	for i, want := range []string{"1", "2", "3"} {
		if filtered[i].ID != want {
			t.Fatalf("filtered[%d].ID = %q, want %q", i, filtered[i].ID, want)
		}
	}
}

func TestFilterGraphPoints_AppliesTextState(t *testing.T) {
	points := []qdrant.ScrollPoint{
		{ID: "1", Payload: map[string]interface{}{"text": "stored fact"}},
		{ID: "2", Payload: map[string]interface{}{"recall_count": 3, "recovery_status": "lost_text"}},
	}

	missing := filterGraphPoints(points, "", "", "", "missing")
	if len(missing) != 1 || missing[0].ID != "2" {
		t.Fatalf("missing filter = %#v, want only point 2", missing)
	}

	present := filterGraphPoints(points, "", "", "", "present")
	if len(present) != 1 || present[0].ID != "1" {
		t.Fatalf("present filter = %#v, want only point 1", present)
	}
}

func TestFilterGraphPoints_FiltersPrimaryTagSeparatelyFromTags(t *testing.T) {
	points := []qdrant.ScrollPoint{
		{ID: "1", Payload: map[string]interface{}{"tags": []interface{}{"personal-memory", "architecture"}, "primary_tag": "personal-memory"}},
		{ID: "2", Payload: map[string]interface{}{"tags": []interface{}{"personal-memory", "architecture"}, "primary_tag": "architecture"}},
	}

	filtered := filterGraphPoints(points, "", "", "personal-memory", "")
	if len(filtered) != 1 {
		t.Fatalf("len(filtered) = %d, want 1", len(filtered))
	}
	if filtered[0].ID != "1" {
		t.Fatalf("filtered[0].ID = %q, want %q", filtered[0].ID, "1")
	}
}

func TestPointToNode_UsesLegacyTextFallbacks(t *testing.T) {
	node := pointToNode(qdrant.ScrollPoint{
		ID: "1",
		Payload: map[string]interface{}{
			"fact":       "legacy fact text",
			"created":    "2026-05-21T10:00:00Z",
			"namespace":  "projects",
			"recall_cnt": 12,
		},
	})

	if node["text"] != "legacy fact text" {
		t.Fatalf("text = %q, want legacy fallback", node["text"])
	}
	if node["created_at"] != "2026-05-21T10:00:00Z" {
		t.Fatalf("created_at = %q, want created fallback", node["created_at"])
	}
}

func TestPointToNode_ExposesPrimaryTag(t *testing.T) {
	node := pointToNode(qdrant.ScrollPoint{
		ID: "1",
		Payload: map[string]interface{}{
			"text":        "fact text",
			"tags":        []interface{}{"health", "decision"},
			"primary_tag": "health",
		},
	})

	if node["primary_tag"] != "health" {
		t.Fatalf("primary_tag = %q, want health", node["primary_tag"])
	}
}

func TestNormalizeFactTags_AddsValidatedPrimaryTag(t *testing.T) {
	tags, primary, err := normalizeFactTags([]string{"decision"}, "health")
	if err != nil {
		t.Fatalf("normalizeFactTags: %v", err)
	}
	if primary != "health" {
		t.Fatalf("primary = %q, want health", primary)
	}
	if len(tags) != 2 || tags[0] != "decision" || tags[1] != "health" {
		t.Fatalf("tags = %#v, want sorted [decision health]", tags)
	}
}

func TestPointToNode_ExposesPayloadKeysForTextlessPoints(t *testing.T) {
	node := pointToNode(qdrant.ScrollPoint{
		ID:      "1",
		Payload: map[string]interface{}{"recall_count": 27},
	})

	if node["text"] != "" {
		t.Fatalf("text = %q, want empty text", node["text"])
	}
	keys, ok := node["payload_keys"].([]string)
	if !ok {
		t.Fatalf("payload_keys has type %T, want []string", node["payload_keys"])
	}
	if len(keys) != 1 || keys[0] != "recall_count" {
		t.Fatalf("payload_keys = %#v, want [recall_count]", keys)
	}
}

func TestPointToNode_DoesNotTreatRecoveryDiagnosticsAsFactText(t *testing.T) {
	node := pointToNode(qdrant.ScrollPoint{
		ID: "1",
		Payload: map[string]interface{}{
			"recovery_status": "lost_text",
			"nearest_text":    "nearest neighbor is not the lost fact text",
			"nearest_score":   0.91,
		},
	})

	if node["text"] != "" {
		t.Fatalf("text = %q, want empty text for recovery diagnostics", node["text"])
	}
	if node["text_missing"] != true {
		t.Fatalf("text_missing = %#v, want true", node["text_missing"])
	}
}
