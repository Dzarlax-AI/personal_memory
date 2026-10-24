package memory

import (
	"context"
	"fmt"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
)

type purposeRecorder struct{ calls []embeddings.Purpose }

func (p *purposeRecorder) Embed(context.Context, string) ([]float32, error) {
	return nil, fmt.Errorf("legacy Embed called")
}
func (p *purposeRecorder) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, fmt.Errorf("legacy EmbedBatch called")
}
func (p *purposeRecorder) EmbedPurpose(_ context.Context, _ string, purpose embeddings.Purpose) ([]float32, error) {
	p.calls = append(p.calls, purpose)
	return []float32{1, 0}, nil
}
func (p *purposeRecorder) EmbedBatchPurpose(_ context.Context, texts []string, purpose embeddings.Purpose) ([][]float32, error) {
	p.calls = append(p.calls, purpose)
	v := make([][]float32, len(texts))
	for i := range v {
		v[i] = []float32{1, 0}
	}
	return v, nil
}

func TestMemoryRuntimeUsesFactAndQueryPurposes(t *testing.T) {
	db := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/points/") {
			w.WriteHeader(404)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/search") {
			w.Write([]byte(`{"result":[]}`))
			return
		}
		w.Write([]byte(`{"result":{"status":"completed"}}`))
	}))
	defer db.Close()
	srv := NewServer(qdrant.NewClient(db.URL, "memory"), nil, NewCache(time.Minute), "test", .97, .60, .90)
	recorder := &purposeRecorder{}
	srv.embed = recorder
	ctx := context.Background()
	srv.Start(ctx)
	defer srv.Shutdown(ctx)
	result, err := srv.storeFact(ctx, toolRequest(map[string]any{"fact": "synthetic fact", "namespace": "projects"}))
	if err != nil || result.IsError {
		t.Fatalf("store: %v %#v", err, result)
	}
	result, err = srv.recallFacts(ctx, toolRequest(map[string]any{"query": "synthetic query", "namespace": "projects"}))
	if err != nil || result.IsError {
		t.Fatalf("recall: %v %#v", err, result)
	}
	result, err = srv.findRelated(ctx, toolRequest(map[string]any{"query": "synthetic related", "namespace": "projects"}))
	if err != nil || result.IsError {
		t.Fatalf("related: %v %#v", err, result)
	}
	if !reflect.DeepEqual(recorder.calls, []embeddings.Purpose{embeddings.FactPassage, embeddings.RetrievalQuery, embeddings.RetrievalQuery}) {
		t.Fatalf("purposes=%v", recorder.calls)
	}
}
