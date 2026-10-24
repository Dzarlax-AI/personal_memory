package rag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
)

type passageRecorder struct{ calls []embeddings.Purpose }

func (p *passageRecorder) Embed(context.Context, string) ([]float32, error) {
	return nil, fmt.Errorf("legacy Embed called")
}
func (p *passageRecorder) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, fmt.Errorf("legacy EmbedBatch called")
}
func (p *passageRecorder) EmbedPurpose(_ context.Context, _ string, purpose embeddings.Purpose) ([]float32, error) {
	p.calls = append(p.calls, purpose)
	return []float32{1, 0}, nil
}
func (p *passageRecorder) EmbedBatchPurpose(_ context.Context, texts []string, purpose embeddings.Purpose) ([][]float32, error) {
	p.calls = append(p.calls, purpose)
	v := make([][]float32, len(texts))
	for i := range v {
		v[i] = []float32{1, 0}
	}
	return v, nil
}
func TestIndexerUsesChunkAndFolderPurposes(t *testing.T) {
	h := newGenerationHarness(t, 1500)
	p := &passageRecorder{}
	h.idx.embed = p
	if err := os.WriteFile(filepath.Join(h.idx.docsDir, "synthetic.md"), []byte("# Heading\n\nSynthetic document."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.idx.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.calls, []embeddings.Purpose{embeddings.ChunkPassage, embeddings.FolderPassage}) {
		t.Fatalf("purposes=%v", p.calls)
	}
}
