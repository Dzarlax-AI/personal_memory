package viz

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestWorkspaceShellKeepsSharedFactDetailAndSectionViews(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatal(err)
	}
	source := string(html)
	for _, expected := range []string{
		`data-section="memory"`, `data-section="ai-review"`, `data-section="quality"`, `id="docs-tab"`,
		`id="memory-view"`, `id="quality-view"`, `id="detail-panel"`, `id="history-status"`,
		`href="/viz/overview"`, `href="/viz/graph"`, `href="/viz/timeline"`,
	} {
		if !strings.Contains(source, expected) {
			t.Errorf("composed workspace is missing %q", expected)
		}
	}
	if strings.Contains(string(mustReadStatic(t, "views/graph.html")), `id="detail-panel"`) {
		t.Fatal("fact detail must be shared by the shell rather than owned by Graph")
	}
}

func TestWorkspaceTypeScriptAssetUsesBrowserJavaScriptMIME(t *testing.T) {
	h := NewHandler(nil, 0.65)
	main := chi.NewRouter()
	main.Mount("/viz", h.Router())
	req := httptest.NewRequest(http.MethodGet, "/viz/assets/js/workspace.ts", nil)
	response := httptest.NewRecorder()
	main.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("GET workspace.ts: got %d, want 200", response.Code)
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/javascript") {
		t.Fatalf("workspace.ts Content-Type = %q, want application/javascript", got)
	}
	if !strings.Contains(response.Body.String(), "no TypeScript-only syntax") {
		t.Fatal("workspace.ts does not document the no-transpilation browser syntax constraint")
	}
}

func mustReadStatic(t *testing.T, name string) []byte {
	t.Helper()
	content, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
