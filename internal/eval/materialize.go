package eval

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
)

// MaterializeOptions changes only the embedding identity of the returned copy.
type MaterializeOptions struct {
	InputProfile *InputProfile
}

// MaterializationDiagnostics is a non-sensitive summary of embedded inputs.
type MaterializationDiagnostics struct {
	Facts        int
	Chunks       int
	Folders      int
	Queries      int
	InputProfile InputProfile
}

// MaterializationEmbeddingError identifies a failed purpose batch without
// requiring callers to expose the embedding provider's response body.
type MaterializationEmbeddingError struct {
	Batch string
	Err   error
}

func (err *MaterializationEmbeddingError) Error() string {
	return fmt.Sprintf("embed %s: %v", err.Batch, err.Err)
}

func (err *MaterializationEmbeddingError) Unwrap() error {
	return err.Err
}

// Materialize deep-copies a strict schema-v3 input and replaces all corpus and
// query vectors using the declared purpose-aware embedding profile.
func Materialize(
	ctx context.Context,
	source *Dataset,
	embedder PurposeEmbedder,
	options MaterializeOptions,
) (*Dataset, MaterializationDiagnostics, error) {
	if source == nil {
		return nil, MaterializationDiagnostics{}, fmt.Errorf("dataset is required")
	}
	if embedder == nil {
		return nil, MaterializationDiagnostics{}, fmt.Errorf("purpose-aware embedder is required")
	}
	materialized := cloneDataset(source)
	if err := materialized.ValidateForMaterialization(); err != nil {
		return nil, MaterializationDiagnostics{}, err
	}
	if options.InputProfile != nil {
		materialized.Embedding.InputProfile = *options.InputProfile
		materialized.Embedding.inputProfilePresent = true
	}
	if err := materialized.ValidateForMaterialization(); err != nil {
		return nil, MaterializationDiagnostics{}, err
	}

	type purposeBatch struct {
		name    string
		purpose embeddings.Purpose
		texts   []string
		assign  func([][]float32)
	}
	batches := make([]purposeBatch, 0, 4)
	for _, group := range []struct {
		name    string
		purpose embeddings.Purpose
		points  *[]FixturePoint
	}{
		{name: "facts", purpose: embeddings.FactPassage, points: &materialized.Facts},
		{name: "chunks", purpose: embeddings.ChunkPassage, points: &materialized.Chunks},
		{name: "folders", purpose: embeddings.FolderPassage, points: &materialized.Folders},
	} {
		points := group.points
		texts := make([]string, len(*points))
		for i := range *points {
			text, _ := corpusText((*points)[i].Payload)
			texts[i] = text
		}
		batches = append(batches, purposeBatch{
			name: group.name, purpose: group.purpose, texts: texts,
			assign: func(vectors [][]float32) {
				for i := range vectors {
					(*points)[i].Vector = append(Vector(nil), vectors[i]...)
				}
			},
		})
	}
	queryTexts := make([]string, len(materialized.Queries))
	for i := range materialized.Queries {
		queryTexts[i] = materialized.Queries[i].Text
	}
	batches = append(batches, purposeBatch{
		name: "queries", purpose: embeddings.RetrievalQuery, texts: queryTexts,
		assign: func(vectors [][]float32) {
			for i := range vectors {
				materialized.Queries[i].Vector = append(Vector(nil), vectors[i]...)
			}
		},
	})

	profile := embeddings.InputProfile(materialized.Embedding.InputProfile)
	for _, batch := range batches {
		vectors, err := embedder.EmbedBatchWithPurpose(
			ctx, batch.texts, batch.purpose, profile, materialized.Embedding.ModelID,
		)
		if err != nil {
			return nil, MaterializationDiagnostics{}, &MaterializationEmbeddingError{
				Batch: batch.name,
				Err:   err,
			}
		}
		if len(vectors) != len(batch.texts) {
			return nil, MaterializationDiagnostics{}, fmt.Errorf(
				"embed %s: result count mismatch", batch.name)
		}
		for i := range vectors {
			if err := validateVector(vectors[i], materialized.Embedding.VectorSize); err != nil {
				return nil, MaterializationDiagnostics{}, fmt.Errorf(
					"%s item %d: %w", batch.name, i, err)
			}
		}
		batch.assign(vectors)
	}
	if err := materialized.Validate(); err != nil {
		return nil, MaterializationDiagnostics{}, fmt.Errorf("validate materialized dataset: %w", err)
	}
	return &materialized, MaterializationDiagnostics{
		Facts: len(materialized.Facts), Chunks: len(materialized.Chunks),
		Folders: len(materialized.Folders), Queries: len(materialized.Queries),
		InputProfile: materialized.Embedding.InputProfile,
	}, nil
}

// RenderDatasetJSON emits canonical, strict JSON accepted by Load.
func RenderDatasetJSON(dataset *Dataset) ([]byte, error) {
	if dataset == nil {
		return nil, fmt.Errorf("dataset is required")
	}
	if err := dataset.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(dataset, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode dataset: %w", err)
	}
	return append(data, '\n'), nil
}
