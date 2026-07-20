// Package embeddingidentity binds persistent Qdrant collections to the exact
// embedding model contract that produced their vectors.
package embeddingidentity

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

const (
	MetadataKey       = "personal_memory.embedding"
	identityVersion   = 1
	identityProvider  = "tei"
	identityProbeText = "personal-memory:embedding-identity:v1"
)

// Expected is the immutable model selected by deployment configuration.
type Expected struct {
	ModelID       string
	ModelRevision string
}

// Record is the canonical vector-space identity persisted in collection
// metadata. Every field participates in strict equality.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	Provider      string `json:"provider"`
	ModelID       string `json:"model_id"`
	ModelRevision string `json:"model_revision"`
	ModelDType    string `json:"model_dtype"`
	Pooling       string `json:"pooling"`
	VectorSize    int    `json:"vector_size"`
}

type modelClient interface {
	Info(context.Context) (embeddings.ModelInfo, error)
	Embed(context.Context, string) ([]float32, error)
}

type collectionClient interface {
	CollectionName() string
	CollectionInfo(context.Context) (qdrant.CollectionInfo, error)
	ExactCount(context.Context) (uint64, error)
	CreateCollection(context.Context, int, map[string]any) error
	UpdateCollectionMetadata(context.Context, map[string]any) error
}

// Ensure verifies TEI and every requested collection before creating or
// updating any collection metadata. The adoption flag applies only to
// non-empty collections that have no identity yet; it never overrides a
// mismatch or malformed identity.
func Ensure(ctx context.Context, embed *embeddings.Client, collections []*qdrant.Client, expected Expected, adoptExisting bool) (Record, error) {
	targets := make([]collectionClient, len(collections))
	for i, collection := range collections {
		if collection == nil {
			return Record{}, fmt.Errorf("embedding identity collection %d is nil", i)
		}
		targets[i] = collection
	}
	return ensure(ctx, embed, targets, expected, adoptExisting)
}

type pendingAction struct {
	collection collectionClient
	create     bool
	metadata   map[string]any
}

func ensure(ctx context.Context, embed modelClient, collections []collectionClient, expected Expected, adoptExisting bool) (Record, error) {
	record, err := activeRecord(ctx, embed, expected)
	if err != nil {
		return Record{}, err
	}

	actions := make([]pendingAction, 0, len(collections))
	seen := make(map[string]struct{}, len(collections))
	for _, collection := range collections {
		if collection == nil {
			return Record{}, fmt.Errorf("embedding identity collection is nil")
		}
		name := strings.TrimSpace(collection.CollectionName())
		if name == "" {
			return Record{}, fmt.Errorf("embedding identity collection name cannot be empty")
		}
		if _, duplicate := seen[name]; duplicate {
			return Record{}, fmt.Errorf("embedding identity collection %q is configured more than once", name)
		}
		seen[name] = struct{}{}

		info, err := collection.CollectionInfo(ctx)
		if err != nil {
			return Record{}, fmt.Errorf("inspect collection %q for embedding identity: %w", name, err)
		}
		if !info.Exists {
			actions = append(actions, pendingAction{
				collection: collection,
				create:     true,
				metadata:   identityMetadata(record),
			})
			continue
		}
		if info.VectorSize != record.VectorSize {
			return Record{}, fmt.Errorf("embedding identity mismatch for collection %q: vector size is %d, active model produces %d; restore the previous model or re-embed into a new collection", name, info.VectorSize, record.VectorSize)
		}

		raw, present := info.Metadata[MetadataKey]
		if !present {
			exactPoints, err := collection.ExactCount(ctx)
			if err != nil {
				return Record{}, fmt.Errorf("count collection %q before embedding identity adoption: %w", name, err)
			}
			if exactPoints > 0 && !adoptExisting {
				return Record{}, fmt.Errorf("collection %q contains %d points but has no embedding identity; after a verified snapshot and model check, set ADOPT_EXISTING_EMBEDDING_IDENTITY=true for one startup", name, exactPoints)
			}
			metadata := cloneMetadata(info.Metadata)
			metadata[MetadataKey] = record
			actions = append(actions, pendingAction{collection: collection, metadata: metadata})
			continue
		}

		stored, err := decodeRecord(raw)
		if err != nil {
			return Record{}, fmt.Errorf("collection %q has invalid embedding identity metadata: %w", name, err)
		}
		if !reflect.DeepEqual(stored, record) {
			return Record{}, fmt.Errorf("embedding identity mismatch for collection %q: stored=%s active=%s; restore the configured model revision or re-embed into new collections; ADOPT_EXISTING_EMBEDDING_IDENTITY cannot override a mismatch", name, formatRecord(stored), formatRecord(record))
		}
	}

	// The complete read-only preflight above must succeed before the first
	// metadata write, preventing a deterministic mismatch from partially
	// adopting an earlier collection.
	for _, action := range actions {
		name := action.collection.CollectionName()
		if action.create {
			if err := action.collection.CreateCollection(ctx, record.VectorSize, action.metadata); err != nil {
				return Record{}, fmt.Errorf("create collection %q with embedding identity: %w", name, err)
			}
			continue
		}
		if err := action.collection.UpdateCollectionMetadata(ctx, action.metadata); err != nil {
			return Record{}, fmt.Errorf("bind embedding identity to collection %q: %w", name, err)
		}
	}
	for _, collection := range collections {
		name := collection.CollectionName()
		info, err := collection.CollectionInfo(ctx)
		if err != nil {
			return Record{}, fmt.Errorf("verify embedding identity for collection %q: %w", name, err)
		}
		if !info.Exists || info.VectorSize != record.VectorSize {
			return Record{}, fmt.Errorf("verify embedding identity for collection %q: collection or vector configuration changed during startup", name)
		}
		stored, err := decodeRecord(info.Metadata[MetadataKey])
		if err != nil || !reflect.DeepEqual(stored, record) {
			return Record{}, fmt.Errorf("verify embedding identity for collection %q: metadata update was not persisted", name)
		}
	}

	return record, nil
}

func activeRecord(ctx context.Context, embed modelClient, expected Expected) (Record, error) {
	if embed == nil {
		return Record{}, fmt.Errorf("embedding identity client is nil")
	}
	expected.ModelID = strings.TrimSpace(expected.ModelID)
	expected.ModelRevision = strings.ToLower(strings.TrimSpace(expected.ModelRevision))

	info, err := embed.Info(ctx)
	if err != nil {
		return Record{}, fmt.Errorf("read active embedding model identity: %w", err)
	}
	activeRevision := strings.ToLower(strings.TrimSpace(info.ModelSHA))
	if strings.TrimSpace(info.ModelID) != expected.ModelID || activeRevision != expected.ModelRevision {
		return Record{}, fmt.Errorf("TEI model identity mismatch: configured model=%q revision=%q, active model=%q revision=%q", expected.ModelID, expected.ModelRevision, info.ModelID, activeRevision)
	}

	probe, err := embed.Embed(ctx, identityProbeText)
	if err != nil {
		return Record{}, fmt.Errorf("embed identity probe: %w", err)
	}
	if len(probe) == 0 {
		return Record{}, fmt.Errorf("embed identity probe returned an empty vector")
	}
	return Record{
		SchemaVersion: identityVersion,
		Provider:      identityProvider,
		ModelID:       expected.ModelID,
		ModelRevision: expected.ModelRevision,
		ModelDType:    strings.TrimSpace(info.ModelDType),
		Pooling:       strings.TrimSpace(info.ModelType.Embedding.Pooling),
		VectorSize:    len(probe),
	}, nil
}

func identityMetadata(record Record) map[string]any {
	return map[string]any{MetadataKey: record}
}

func cloneMetadata(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source)+1)
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func decodeRecord(raw any) (Record, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := json.Unmarshal(encoded, &record); err != nil {
		return Record{}, err
	}
	if record.SchemaVersion != identityVersion || record.Provider != identityProvider || record.ModelID == "" || record.ModelRevision == "" || record.ModelDType == "" || record.Pooling == "" || record.VectorSize < 1 {
		return Record{}, fmt.Errorf("incomplete or unsupported identity record")
	}
	return record, nil
}

func formatRecord(record Record) string {
	parts := []string{
		fmt.Sprintf("schema=%d", record.SchemaVersion),
		"provider=" + record.Provider,
		"model=" + record.ModelID,
		"revision=" + record.ModelRevision,
		"dtype=" + record.ModelDType,
		"pooling=" + record.Pooling,
		fmt.Sprintf("size=%d", record.VectorSize),
	}
	sort.Strings(parts)
	return "{" + strings.Join(parts, ",") + "}"
}
