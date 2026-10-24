package embeddingidentity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func TestBindingsPreflightAllBeforeAnyWrite(t *testing.T) {
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info":
			json.NewEncoder(w).Encode(map[string]any{"model_id": "intfloat/multilingual-e5-small", "model_sha": testRevision, "model_dtype": "float32", "model_type": map[string]any{"embedding": map[string]any{"pooling": "mean"}}})
		case "/embed":
			v := make([]float32, 384)
			v[0] = 1
			json.NewEncoder(w).Encode([][]float32{v})
		case "/collections/new_memory":
			if r.Method == "GET" {
				w.WriteHeader(404)
			} else {
				writes++
				json.NewEncoder(w).Encode(map[string]any{"result": true})
			}
		case "/collections/old_chunks":
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"points_count": 1, "config": map[string]any{"params": map[string]any{"vectors": map[string]any{"size": 384}}, "metadata": map[string]any{MetadataKey: map[string]any{"wrong": "identity"}}}}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	cfg := embeddings.BackendConfig{Provider: "tei", Model: "intfloat/multilingual-e5-small", InputProfile: embeddings.LegacyRawV1, Dimensions: 384, URL: server.URL, Revision: testRevision}
	backend, err := embeddings.NewBackend(cfg)
	if err != nil {
		t.Fatal(err)
	}
	err = EnsureBindings(context.Background(), []Binding{
		{Backend: backend, Config: cfg, Collections: []*qdrant.Client{qdrant.NewClient(server.URL, "new_memory")}},
		{Backend: backend, Config: cfg, Collections: []*qdrant.Client{qdrant.NewClient(server.URL, "old_chunks")}},
	}, true)
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("err=%v", err)
	}
	if writes != 0 {
		t.Fatalf("wrote metadata before full preflight: %d", writes)
	}
}
func TestProviderIdentityIncludesDeclaredWeightsAndNormalization(t *testing.T) {
	first := embeddings.Descriptor{SchemaVersion: 2, Provider: "llamacpp", Model: "embeddinggemma-2", Dimensions: 768, InputProfile: embeddings.EmbeddingGemmaSearchV1, Normalization: "l2", WeightsSHA256: strings.Repeat("a", 64), WeightsProvenance: "declared"}
	changed := first
	changed.WeightsSHA256 = strings.Repeat("b", 64)
	if equalBindingRecord(first, changed) {
		t.Fatal("ignored weights")
	}
	changed = first
	changed.Normalization = "provider"
	if equalBindingRecord(first, changed) {
		t.Fatal("ignored normalization")
	}
	if !equalBindingRecord(first, first) {
		t.Fatal("unstable identity")
	}
}
