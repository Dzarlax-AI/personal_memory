package embeddings

import "context"

// Embedder preserves the legacy client contract for callers and test doubles.
// Runtime helpers select an explicit purpose when the backend supports it.
type Embedder interface {
	Embed(context.Context, string) ([]float32, error)
	EmbedBatch(context.Context, []string) ([][]float32, error)
}

type purposeEmbedder interface {
	EmbedPurpose(context.Context, string, Purpose) ([]float32, error)
	EmbedBatchPurpose(context.Context, []string, Purpose) ([][]float32, error)
}

func Embed(ctx context.Context, client Embedder, text string, purpose Purpose) ([]float32, error) {
	if err := validatePurpose(purpose); err != nil {
		return nil, err
	}
	if p, ok := client.(purposeEmbedder); ok {
		return p.EmbedPurpose(ctx, text, purpose)
	}
	return client.Embed(ctx, text)
}

func EmbedBatch(ctx context.Context, client Embedder, texts []string, purpose Purpose) ([][]float32, error) {
	if err := validatePurpose(purpose); err != nil {
		return nil, err
	}
	if p, ok := client.(purposeEmbedder); ok {
		return p.EmbedBatchPurpose(ctx, texts, purpose)
	}
	return client.EmbedBatch(ctx, texts)
}
