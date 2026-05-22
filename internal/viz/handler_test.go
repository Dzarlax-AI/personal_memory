package viz

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/go-chi/chi/v5"
)

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

func TestNormalizeFactTags_AddsPrimaryTag(t *testing.T) {
	tags, primary := normalizeFactTags([]string{"decision"}, "health")
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
