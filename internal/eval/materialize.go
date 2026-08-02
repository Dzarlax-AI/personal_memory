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

type purposeBatch struct {
	name    string
	purpose embeddings.Purpose
	texts   []string
	assign  func([][]float32)
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
	materialized, err := cloneDataset(source)
	if err != nil {
		return nil, MaterializationDiagnostics{}, fmt.Errorf("clone dataset: %w", err)
	}
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

	diagnostics, err := materializeCorpusPurposeBatches(ctx, &materialized, embedder)
	if err != nil {
		return nil, MaterializationDiagnostics{}, err
	}
	queryTexts := make([]string, len(materialized.Queries))
	for i := range materialized.Queries {
		queryTexts[i] = materialized.Queries[i].Text
	}
	queryVectors, err := embedQueryPurpose(
		ctx, queryTexts, materialized.Embedding, embedder, true,
	)
	if err != nil {
		return nil, MaterializationDiagnostics{}, &MaterializationEmbeddingError{
			Batch: "queries",
			Err:   err,
		}
	}
	for i := range queryVectors {
		materialized.Queries[i].Vector = append(Vector(nil), queryVectors[i]...)
	}
	if err := materialized.Validate(); err != nil {
		return nil, MaterializationDiagnostics{}, fmt.Errorf("validate materialized dataset: %w", err)
	}
	diagnostics.Queries = len(materialized.Queries)
	diagnostics.InputProfile = materialized.Embedding.InputProfile
	return &materialized, diagnostics, nil
}

func materializeCorpusPurposeBatches(
	ctx context.Context,
	dataset *Dataset,
	embedder PurposeEmbedder,
) (MaterializationDiagnostics, error) {
	batches := make([]purposeBatch, 0, 3)
	for _, group := range []struct {
		name    string
		purpose embeddings.Purpose
		points  *[]FixturePoint
	}{
		{name: "facts", purpose: embeddings.FactPassage, points: &dataset.Facts},
		{name: "chunks", purpose: embeddings.ChunkPassage, points: &dataset.Chunks},
		{name: "folders", purpose: embeddings.FolderPassage, points: &dataset.Folders},
	} {
		points := group.points
		texts := make([]string, len(*points))
		for i := range *points {
			text, ok := corpusText((*points)[i].Payload, group.name)
			if !ok {
				return MaterializationDiagnostics{}, fmt.Errorf(
					"%s point %q has no usable corpus text",
					group.name, (*points)[i].ID.String(),
				)
			}
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

	profile := embeddings.InputProfile(dataset.Embedding.InputProfile)
	for _, batch := range batches {
		vectors, err := embedder.EmbedBatchWithPurpose(
			ctx, batch.texts, batch.purpose, profile, dataset.Embedding.ModelID,
		)
		if err != nil {
			return MaterializationDiagnostics{}, &MaterializationEmbeddingError{
				Batch: batch.name,
				Err:   err,
			}
		}
		if len(vectors) != len(batch.texts) {
			return MaterializationDiagnostics{}, fmt.Errorf(
				"embed %s: result count mismatch", batch.name)
		}
		for i := range vectors {
			if err := validateVector(vectors[i], dataset.Embedding.VectorSize); err != nil {
				return MaterializationDiagnostics{}, fmt.Errorf(
					"%s item %d: %w", batch.name, i, err)
			}
		}
		batch.assign(vectors)
	}
	return MaterializationDiagnostics{
		Facts:   len(dataset.Facts),
		Chunks:  len(dataset.Chunks),
		Folders: len(dataset.Folders),
	}, nil
}

func embedQueryPurpose(
	ctx context.Context,
	texts []string,
	identity EmbeddingIdentity,
	embedder PurposeEmbedder,
	batch bool,
) ([][]float32, error) {
	profile := embeddings.InputProfile(identity.InputProfile)
	var (
		vectors [][]float32
		err     error
	)
	if batch {
		vectors, err = embedder.EmbedBatchWithPurpose(
			ctx, texts, embeddings.RetrievalQuery, profile, identity.ModelID,
		)
	} else {
		if len(texts) != 1 {
			return nil, fmt.Errorf("online query embedding requires exactly one input")
		}
		var vector []float32
		vector, err = embedder.EmbedWithPurpose(
			ctx, texts[0], embeddings.RetrievalQuery, profile, identity.ModelID,
		)
		if err == nil {
			vectors = [][]float32{vector}
		}
	}
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("query embedding result count mismatch")
	}
	for i := range vectors {
		if err := validateVector(vectors[i], identity.VectorSize); err != nil {
			return nil, fmt.Errorf("query item %d: %w", i, err)
		}
	}
	return vectors, nil
}

// RenderDatasetJSON emits canonical, strict JSON accepted by Load.
func RenderDatasetJSON(dataset *Dataset) ([]byte, error) {
	if dataset == nil {
		return nil, fmt.Errorf("dataset is required")
	}
	rendered := *dataset
	rendered.Configuration = dataset.Configuration
	rendered.Configuration.TopK = append([]int(nil), dataset.Configuration.TopK...)
	rendered.Gates = dataset.Gates
	if rendered.SchemaVersion == CurrentDatasetSchemaVersion {
		rendered.Gates.forceLifecycleViolationsRender = true
	}
	if err := rendered.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(&rendered, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode dataset: %w", err)
	}
	return append(data, '\n'), nil
}
