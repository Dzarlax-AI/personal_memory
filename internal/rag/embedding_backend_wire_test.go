package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/config"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/mark3labs/mcp-go/server"
)

func TestGemmaBackendMCPWire(t *testing.T) {
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tokenize":
			var input struct {
				Content string `json:"content"`
			}
			json.NewDecoder(r.Body).Decode(&input)
			if input.Content != "task: search result | query: synthetic query" {
				t.Errorf("wrong query purpose")
			}
			w.Write([]byte(`{"tokens":[1,2,3]}`))
		case "/v1/embeddings":
			calls++
			v := make([]float32, 768)
			v[0] = 1
			json.NewEncoder(w).Encode(map[string]any{"model": "embeddinggemma-2", "data": []any{map[string]any{"index": 0, "embedding": v}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer model.Close()
	backend, err := embeddings.NewBackend(embeddings.BackendConfig{Provider: "llamacpp", Model: "embeddinggemma-2", InputProfile: embeddings.EmbeddingGemmaSearchV1, Dimensions: 768, URL: model.URL, WeightsSHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}

	point := qdrant.Point{ID: "a", Score: .7, Payload: searchPayload("Published synthetic document", "/docs/a.md", "", "g")}
	s := &Server{cfg: &config.Config{RAGDocumentsDir: "/docs", RAGSearchMode: "flat"}, queryEmbed: backend, validateChunks: sealedSearchValidator{"/docs/a.md\x00g": point}, searchChunks: backendWireSearcher{points: []qdrant.Point{point}}}
	m := server.NewMCPServer("synthetic-gemma-routing", "1")
	s.RegisterTools(m)
	h := httptest.NewServer(server.NewStreamableHTTPServer(m, server.WithStateLess(true)))
	defer h.Close()
	call := func(body string) map[string]json.RawMessage {
		t.Helper()
		req, err := http.NewRequest("POST", h.URL, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		r, err := h.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		// Streamable HTTP may use an SSE envelope for a JSON-RPC response.
		if strings.Contains(string(raw), "data:") {
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(line, "data:") {
					raw = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
					break
				}
			}
		}
		var v map[string]json.RawMessage
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("invalid wire response: %s", raw)
		}
		if v["error"] != nil {
			t.Fatalf("RPC error: %s", raw)
		}
		return v
	}
	call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	listed := call(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if !strings.Contains(string(listed["result"]), "search_documents") {
		t.Fatal("search absent from tools/list schema")
	}
	result := call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_documents","arguments":{"query":"synthetic query","limit":1}}}`)
	if !strings.Contains(string(result["result"]), "Published synthetic document") || strings.Contains(string(result["result"]), `"isError":true`) {
		t.Fatalf("unexpected tool response %s", result["result"])
	}
	if calls != 1 {
		t.Fatalf("model calls=%d", calls)
	}
}

type backendWireSearcher struct{ points []qdrant.Point }

func (s backendWireSearcher) Search(context.Context, []float32, int, map[string]interface{}, *float64) ([]qdrant.Point, error) {
	return s.points, nil
}
