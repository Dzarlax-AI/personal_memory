package memory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/mark3labs/mcp-go/mcp"
)

func toolRequest(args map[string]interface{}) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}
}

func toolResultText(t *testing.T, result *mcp.CallToolResult) string {
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

func newMutationTestServer(t *testing.T, searchResult string) (*Server, *int) {
	t.Helper()
	embedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[[0.1,0.2]]`))
	}))
	t.Cleanup(embedServer.Close)

	writes := 0
	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points/search") {
			_, _ = w.Write([]byte(searchResult))
			return
		}
		writes++
		_, _ = w.Write([]byte(`{"result":{"status":"completed"}}`))
	}))
	t.Cleanup(qdrantServer.Close)

	return NewServer(
		qdrant.NewClient(qdrantServer.URL, "memory"),
		embeddings.NewClient(embedServer.URL),
		NewCache(time.Minute),
		"test", 0.97, 0.60, 0.90,
	), &writes
}

func TestUpdateFactRefusesAmbiguousMatch(t *testing.T) {
	srv, writes := newMutationTestServer(t, `{"result":[
		{"id":"11111111-1111-1111-1111-111111111111","score":0.950,"payload":{"text":"first","namespace":"personal"}},
		{"id":"22222222-2222-2222-2222-222222222222","score":0.945,"payload":{"text":"second","namespace":"personal"}}
	]}`)

	result, err := srv.updateFact(context.Background(), toolRequest(map[string]interface{}{
		"old_query": "similar fact", "new_fact": "replacement", "namespace": "personal",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(toolResultText(t, result), "ambiguous matches") {
		t.Fatalf("expected ambiguity refusal, got %#v", result)
	}
	if *writes != 0 {
		t.Fatalf("ambiguous update performed %d writes", *writes)
	}
}

func TestDeleteFactRefusesLowScore(t *testing.T) {
	srv, writes := newMutationTestServer(t, `{"result":[
		{"id":"11111111-1111-1111-1111-111111111111","score":0.72,"payload":{"text":"weak match","namespace":"personal"}}
	]}`)

	result, err := srv.deleteFact(context.Background(), toolRequest(map[string]interface{}{
		"query": "unrelated", "namespace": "personal",
	}))
	if err != nil {
		t.Fatal(err)
	}
	text := toolResultText(t, result)
	if !result.IsError || !strings.Contains(text, "below threshold") || !strings.Contains(text, "11111111-1111-1111-1111-111111111111") {
		t.Fatalf("expected low-score refusal with candidate, got %q", text)
	}
	if *writes != 0 {
		t.Fatalf("low-score delete performed %d writes", *writes)
	}
}

func TestDeleteFactByExactPointIDChecksNamespaceAndDeletes(t *testing.T) {
	deleted := false
	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"result":{"id":"11111111-1111-1111-1111-111111111111","payload":{"text":"target","namespace":"personal"}}}`))
		case strings.HasSuffix(r.URL.Path, "/points/delete"):
			deleted = true
			_, _ = w.Write([]byte(`{"result":{"status":"completed"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer qdrantServer.Close()
	srv := &Server{qdrant: qdrant.NewClient(qdrantServer.URL, "memory"), cache: NewCache(time.Minute), mutationMatchThreshold: 0.90}

	result, err := srv.deleteFact(context.Background(), toolRequest(map[string]interface{}{
		"point_id": "11111111-1111-1111-1111-111111111111", "namespace": "personal",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !deleted {
		t.Fatalf("exact delete failed: deleted=%v result=%#v", deleted, result)
	}
}

func TestDeleteFactByExactPointIDRefusesNamespaceMismatch(t *testing.T) {
	qdrantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"id":"11111111-1111-1111-1111-111111111111","payload":{"text":"target","namespace":"work"}}}`))
	}))
	defer qdrantServer.Close()
	srv := &Server{qdrant: qdrant.NewClient(qdrantServer.URL, "memory"), cache: NewCache(time.Minute), mutationMatchThreshold: 0.90}

	result, err := srv.deleteFact(context.Background(), toolRequest(map[string]interface{}{
		"point_id": "11111111-1111-1111-1111-111111111111", "namespace": "personal",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(toolResultText(t, result), "does not belong") {
		t.Fatalf("expected namespace refusal, got %#v", result)
	}
}

func TestMemoryInputValidationRejectsUnsafeNumbers(t *testing.T) {
	srv := &Server{}
	tests := []struct {
		name string
		call func() (*mcp.CallToolResult, error)
		want string
	}{
		{name: "forget zero days", call: func() (*mcp.CallToolResult, error) {
			return srv.forgetOld(context.Background(), toolRequest(map[string]interface{}{"days": float64(0)}))
		}, want: "days must be greater than zero"},
		{name: "recall zero limit", call: func() (*mcp.CallToolResult, error) {
			return srv.recallFacts(context.Background(), toolRequest(map[string]interface{}{"query": "x", "limit": float64(0)}))
		}, want: "limit must be greater than zero"},
		{name: "related oversized limit", call: func() (*mcp.CallToolResult, error) {
			return srv.findRelated(context.Background(), toolRequest(map[string]interface{}{"query": "x", "limit": float64(101)}))
		}, want: "limit must be at most 100"},
		{name: "recall fractional limit", call: func() (*mcp.CallToolResult, error) {
			return srv.recallFacts(context.Background(), toolRequest(map[string]interface{}{"query": "x", "limit": 1.5}))
		}, want: "limit must be an integer"},
		{name: "forget fractional days", call: func() (*mcp.CallToolResult, error) {
			return srv.forgetOld(context.Background(), toolRequest(map[string]interface{}{"days": 1.5}))
		}, want: "days must be an integer"},
		{name: "operational fractional count", call: func() (*mcp.CallToolResult, error) {
			return srv.getOperationalContext(context.Background(), toolRequest(map[string]interface{}{"top_recalled": 1.5}))
		}, want: "top_recalled must be an integer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.call()
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || toolResultText(t, result) != tt.want {
				t.Fatalf("got %#v, want %q", result, tt.want)
			}
		})
	}
}

func TestMemoryInputValidationRejectsOversizedValuesBeforeDependencies(t *testing.T) {
	srv := &Server{}
	tests := []struct {
		name string
		call func() (*mcp.CallToolResult, error)
		want string
	}{
		{name: "fact", call: func() (*mcp.CallToolResult, error) {
			return srv.storeFact(context.Background(), toolRequest(map[string]interface{}{"fact": strings.Repeat("x", maxFactBytes+1)}))
		}, want: "fact must be at most"},
		{name: "query", call: func() (*mcp.CallToolResult, error) {
			return srv.recallFacts(context.Background(), toolRequest(map[string]interface{}{"query": strings.Repeat("x", maxQueryBytes+1)}))
		}, want: "query must be at most"},
		{name: "new fact", call: func() (*mcp.CallToolResult, error) {
			return srv.updateFact(context.Background(), toolRequest(map[string]interface{}{"point_id": "1", "new_fact": strings.Repeat("x", maxFactBytes+1)}))
		}, want: "new_fact must be at most"},
		{name: "old query", call: func() (*mcp.CallToolResult, error) {
			return srv.updateFact(context.Background(), toolRequest(map[string]interface{}{"old_query": strings.Repeat("x", maxQueryBytes+1), "new_fact": "replacement"}))
		}, want: "old_query must be at most"},
		{name: "namespace", call: func() (*mcp.CallToolResult, error) {
			return srv.listTags(context.Background(), toolRequest(map[string]interface{}{"namespace": strings.Repeat("x", maxNamespaceBytes+1)}))
		}, want: "namespace must be at most"},
		{name: "tag count", call: func() (*mcp.CallToolResult, error) {
			return srv.storeFact(context.Background(), toolRequest(map[string]interface{}{"fact": "bounded", "tags": strings.Repeat("x,", maxTags) + "x"}))
		}, want: "tags must contain at most"},
		{name: "tag length", call: func() (*mcp.CallToolResult, error) {
			return srv.storeFact(context.Background(), toolRequest(map[string]interface{}{"fact": "bounded", "tags": strings.Repeat("x", maxTagBytes+1)}))
		}, want: "tags[0] must be at most"},
		{name: "import bytes", call: func() (*mcp.CallToolResult, error) {
			return srv.importFacts(context.Background(), toolRequest(map[string]interface{}{"facts": strings.Repeat(" ", maxImportBytes+1)}))
		}, want: "facts must be at most"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.call()
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || !strings.Contains(toolResultText(t, result), tt.want) {
				t.Fatalf("got %#v, want error containing %q", result, tt.want)
			}
		})
	}
}

func TestImportFactsRejectsOversizedBatchBeforeDependencies(t *testing.T) {
	items := make([]string, maxImportFacts+1)
	for i := range items {
		items[i] = `{"text":"fact"}`
	}
	srv := &Server{}
	result, err := srv.importFacts(context.Background(), toolRequest(map[string]interface{}{
		"facts": "[" + strings.Join(items, ",") + "]",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(toolResultText(t, result), "facts must contain at most") {
		t.Fatalf("expected batch-size refusal, got %#v", result)
	}
}

func TestPointID_Deterministic(t *testing.T) {
	id1 := PointID("personal", "hello world")
	id2 := PointID("personal", "hello world")
	if id1 != id2 {
		t.Errorf("expected same ID for same text, got %s and %s", id1, id2)
	}
}

func TestPointID_Different(t *testing.T) {
	id1 := PointID("personal", "hello")
	id2 := PointID("personal", "world")
	if id1 == id2 {
		t.Error("expected different IDs for different text")
	}
}

func TestPointID_Format(t *testing.T) {
	id := PointID("personal", "test")
	// Should be in UUID-like format: 8-4-4-4-12
	if len(id) != 36 {
		t.Errorf("expected 36 char UUID-like format, got %d: %s", len(id), id)
	}
	if id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Errorf("unexpected format: %s", id)
	}
}

func TestPointIDSeparatesNamespaces(t *testing.T) {
	personal := PointID("personal", "same text")
	work := PointID("work", "same text")
	if personal == work {
		t.Fatalf("same text in different namespaces received ID %s", personal)
	}
}

func TestPointIDNormalizesDefaultNamespace(t *testing.T) {
	if PointID("", "fact") != PointID(" default ", "fact") {
		t.Fatal("empty and explicit default namespaces must produce the same ID")
	}
}

func TestIsExpired_NoField(t *testing.T) {
	payload := map[string]interface{}{"text": "hello"}
	if isExpired(payload) {
		t.Error("expected not expired when valid_until is missing")
	}
}

func TestIsExpired_FutureDate(t *testing.T) {
	payload := map[string]interface{}{"valid_until": "2099-12-31"}
	if isExpired(payload) {
		t.Error("expected not expired for future date")
	}
}

func TestIsExpired_PastDate(t *testing.T) {
	payload := map[string]interface{}{"valid_until": "2020-01-01"}
	if !isExpired(payload) {
		t.Error("expected expired for past date")
	}
}

func TestFormatTagsList(t *testing.T) {
	tests := []struct {
		input    interface{}
		expected string
	}{
		{nil, "[]"},
		{[]interface{}{"a", "b"}, "['a', 'b']"},
		{[]string{"x"}, "['x']"},
	}
	for _, tt := range tests {
		got := formatTagsList(tt.input)
		if got != tt.expected {
			t.Errorf("formatTagsList(%v) = %s, want %s", tt.input, got, tt.expected)
		}
	}
}

func TestNormalizeFactTags_UsesSingleTagAsPrimary(t *testing.T) {
	tags, primary := normalizeFactTags([]string{" health "}, "")
	if primary != "health" {
		t.Fatalf("primary = %q, want health", primary)
	}
	if len(tags) != 1 || tags[0] != "health" {
		t.Fatalf("tags = %#v, want [health]", tags)
	}
}

func TestNormalizeFactTags_AddsPrimaryToTags(t *testing.T) {
	tags, primary := normalizeFactTags([]string{"decision"}, "health")
	if primary != "health" {
		t.Fatalf("primary = %q, want health", primary)
	}
	if len(tags) != 2 || tags[0] != "decision" || tags[1] != "health" {
		t.Fatalf("tags = %#v, want [decision health]", tags)
	}
}

func TestNormalizeFactTags_LeavesMultiTagPrimaryEmpty(t *testing.T) {
	tags, primary := normalizeFactTags([]string{"health", "decision"}, "")
	if primary != "" {
		t.Fatalf("primary = %q, want empty", primary)
	}
	if len(tags) != 2 {
		t.Fatalf("tags = %#v, want two tags", tags)
	}
}
