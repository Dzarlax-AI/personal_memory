package rag

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/config"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestParseSearchDocumentsArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{name: "defaults", args: map[string]any{"query": "memory"}},
		{name: "flat", args: map[string]any{"query": "memory", "limit": float64(100), "mode": "flat"}},
		{name: "blended", args: map[string]any{"query": "memory", "mode": "blended"}},
		{name: "blank query", args: map[string]any{"query": "  "}, wantErr: true},
		{name: "zero limit", args: map[string]any{"query": "memory", "limit": float64(0)}, wantErr: true},
		{name: "huge limit", args: map[string]any{"query": "memory", "limit": float64(101)}, wantErr: true},
		{name: "fractional limit", args: map[string]any{"query": "memory", "limit": 1.5}, wantErr: true},
		{name: "unknown mode", args: map[string]any{"query": "memory", "mode": "magic"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, gotErr := parseSearchDocumentsArgs(tt.args)
			if (gotErr != "") != tt.wantErr {
				t.Fatalf("error=%q, wantErr=%v", gotErr, tt.wantErr)
			}
		})
	}
}

func TestParseSearchDocumentsArgsDefaultsRemainHierarchical(t *testing.T) {
	query, limit, mode, validationErr := parseSearchDocumentsArgs(map[string]any{"query": " memory "})
	if validationErr != "" {
		t.Fatal(validationErr)
	}
	if query != "memory" || limit != 5 || mode != "hierarchical" {
		t.Fatalf("defaults = query:%q limit:%d mode:%q", query, limit, mode)
	}
}

func TestRelPathNeverReturnsAbsoluteOrEscapingPaths(t *testing.T) {
	tests := []struct {
		name string
		base string
		path string
		want string
	}{
		{name: "child", base: "/documents", path: "/documents/project/note.md", want: "project/note.md"},
		{name: "outside", base: "/documents", path: "/private/secret.md", want: ""},
		{name: "empty base", path: "/private/secret.md", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := relPath(tt.base, tt.path); got != tt.want {
				t.Fatalf("relPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSearchDocumentsBlendedRescuesFlatTopAndReturnsPrivacySafeRouting(t *testing.T) {
	const documentsRoot = "/documents"
	var calls []searchCall
	folders := fakePointSearcher{name: "folders", calls: &calls, search: func(_ map[string]any) []qdrant.Point {
		return []qdrant.Point{{ID: "folder", Score: .8, Payload: map[string]any{"folder_path": "/documents/project"}}}
	}}
	chunks := fakePointSearcher{name: "chunks", calls: &calls, search: func(filter map[string]any) []qdrant.Point {
		if filter != nil {
			return []qdrant.Point{
				{ID: "a", Score: .91, Payload: map[string]any{"text": "filtered a", "file_path": "/documents/project/a.md", "heading": "A", "chunk_index": 0}},
				{ID: "b", Score: .90, Payload: map[string]any{"text": "filtered b", "file_path": "/documents/project/b.md", "heading": "B", "chunk_index": 0}},
			}
		}
		return []qdrant.Point{
			{ID: "flat-top", Score: .99, Payload: map[string]any{"text": "strong flat", "file_path": "/private/secret.md", "heading": "Secret", "chunk_index": 0, "unrestricted": "/do/not/expose"}},
			{ID: "a", Score: .89, Payload: map[string]any{"text": "flat a", "file_path": "/documents/project/a.md", "heading": "A", "chunk_index": 0}},
			{ID: "b", Score: .88, Payload: map[string]any{"text": "flat b", "file_path": "/documents/project/b.md", "heading": "B", "chunk_index": 0}},
		}
	}}
	srv := &Server{
		queryEmbed: fakeQueryEmbedder{}, searchChunks: chunks, searchFolders: folders,
		cfg: &config.Config{RAGDocumentsDir: documentsRoot, RAGFolderTopK: 3, RAGFolderThreshold: .5},
	}
	result, err := srv.handleSearchDocuments(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{"query": "needle", "limit": float64(2), "mode": "blended"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool error: %s", ragToolResultText(t, result))
	}

	var results []struct {
		Score    float64 `json:"score"`
		Text     string  `json:"text"`
		FilePath string  `json:"file_path"`
		Routing  struct {
			Strategy string `json:"strategy"`
			Sources  []struct {
				Source string `json:"source"`
				Rank   int    `json:"rank"`
			} `json:"sources"`
			ReasonCodes         []string `json:"reason_codes"`
			SelectedFolderPaths []string `json:"selected_folder_paths"`
		} `json:"routing"`
	}
	responseText := ragToolResultText(t, result)
	if err := json.Unmarshal([]byte(responseText), &results); err != nil {
		t.Fatalf("decode response: %v\n%s", err, responseText)
	}
	if len(results) != 2 || results[0].Text != "filtered a" || results[1].Text != "strong flat" {
		t.Fatalf("unexpected blended results: %#v", results)
	}
	if results[1].Score != .99 {
		t.Fatalf("rescued cosine score = %v, want .99", results[1].Score)
	}
	if results[1].FilePath != "" {
		t.Fatalf("outside file path leaked as %q", results[1].FilePath)
	}
	if results[1].Routing.Strategy != "blended_rrf" ||
		!reflect.DeepEqual(results[1].Routing.ReasonCodes, []string{"flat_match", "flat_rescue"}) ||
		!reflect.DeepEqual(results[1].Routing.SelectedFolderPaths, []string{"project"}) {
		t.Fatalf("rescued routing = %#v", results[1].Routing)
	}
	if len(results[1].Routing.Sources) != 1 || results[1].Routing.Sources[0].Source != "flat" || results[1].Routing.Sources[0].Rank != 1 {
		t.Fatalf("rescued source explanation = %#v", results[1].Routing.Sources)
	}
	if strings.Contains(responseText, "/private/") || strings.Contains(responseText, "unrestricted") || strings.Contains(responseText, "/do/not/expose") {
		t.Fatalf("response leaked path or unrestricted payload: %s", responseText)
	}
	if want := []searchCall{
		{Collection: "folders", Limit: 3},
		{Collection: "chunks", Limit: 4, Filtered: true},
		{Collection: "chunks", Limit: 4},
	}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("bounded search calls = %#v, want %#v", calls, want)
	}
}

func TestSearchDocumentsDefaultDoesNotBecomeBlended(t *testing.T) {
	chunkSearches := 0
	srv := &Server{
		queryEmbed: fakeQueryEmbedder{},
		searchFolders: fakePointSearcher{search: func(_ map[string]any) []qdrant.Point {
			return []qdrant.Point{{ID: "folder", Score: .8, Payload: map[string]any{"folder_path": "/documents/project"}}}
		}},
		searchChunks: fakePointSearcher{search: func(_ map[string]any) []qdrant.Point {
			chunkSearches++
			return []qdrant.Point{{ID: "a", Score: .9, Payload: map[string]any{"text": "a", "file_path": "/documents/project/a.md"}}}
		}},
		cfg: &config.Config{RAGDocumentsDir: "/documents", RAGFolderTopK: 3, RAGFolderThreshold: .5},
	}
	result, err := srv.handleSearchDocuments(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{"query": "needle", "limit": float64(2)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := ragToolResultText(t, result)
	if chunkSearches != 1 {
		t.Fatalf("default chunk searches = %d, want hierarchical-only 1", chunkSearches)
	}
	if strings.Contains(text, `"routing"`) {
		t.Fatalf("default response unexpectedly gained blended routing: %s", text)
	}
}

func ragToolResultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("unexpected content: %#v", result.Content)
	}
	content, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("unexpected content type: %T", result.Content[0])
	}
	return content.Text
}

type searchCall struct {
	Collection string
	Limit      int
	Filtered   bool
}

type fakePointSearcher struct {
	name   string
	calls  *[]searchCall
	search func(filter map[string]any) []qdrant.Point
}

func (f fakePointSearcher) Search(_ context.Context, _ []float32, limit int, filter map[string]interface{}, _ *float64) ([]qdrant.Point, error) {
	if f.calls != nil {
		*f.calls = append(*f.calls, searchCall{Collection: f.name, Limit: limit, Filtered: filter != nil})
	}
	return f.search(filter), nil
}

type fakeQueryEmbedder struct{}

func (fakeQueryEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{.1, .2}, nil
}
