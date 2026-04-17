package viz

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

// Regression: StripPrefix("/assets/") with a trailing slash caused the embedded
// FileServer to 404 every asset because after stripping the prefix the request
// path had no leading "/".
func TestAssetRouter_ServesEmbeddedFiles(t *testing.T) {
	h := NewHandler(nil, 0.65)
	router := h.Router()

	for _, asset := range []string{"/assets/styles.css", "/assets/js/init.js"} {
		req := httptest.NewRequest(http.MethodGet, asset, nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s: got %d, want 200 (StripPrefix likely has a trailing slash again)", asset, rr.Code)
		}
	}
}
