package embeddingidentity

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

// Binding ties an active backend to collections. All bindings are inspected
// before the first collection mutation. Network failures during apply can still
// leave empty, correctly bound collections; startup never starts workers then.
type Binding struct {
	Backend     *embeddings.Backend
	Config      embeddings.BackendConfig
	Collections []*qdrant.Client
}

func BindingRecord(ctx context.Context, backend *embeddings.Backend, cfg embeddings.BackendConfig) (any, int, error) {
	if cfg.Provider == "tei" {
		record, err := activeRecord(ctx, embeddings.NewClient(cfg.URL), Expected{ModelID: cfg.Model, ModelRevision: cfg.Revision, InputProfile: cfg.InputProfile})
		if err != nil {
			return nil, 0, errors.New("TEI model identity verification failed")
		}
		if record.VectorSize != cfg.Dimensions {
			return nil, 0, errors.New("TEI configured dimensions mismatch")
		}
		return record, record.VectorSize, nil
	}
	descriptor, err := backend.Describe(ctx)
	if err != nil {
		return nil, 0, err
	}
	return descriptor, cfg.Dimensions, nil
}

type bindingAction struct {
	collection *qdrant.Client
	record     any
	dimensions int
	create     bool
	metadata   map[string]any
}

func EnsureBindings(ctx context.Context, bindings []Binding, adoptLegacy bool) error {
	var actions []bindingAction
	seen := map[string]bool{}
	expectedRecords := map[string]any{}
	expectedSizes := map[string]int{}
	for _, binding := range bindings {
		record, dimensions, err := BindingRecord(ctx, binding.Backend, binding.Config)
		if err != nil {
			return err
		}
		for _, collection := range binding.Collections {
			if collection == nil || seen[collection.CollectionName()] {
				return errors.New("duplicate or missing embedding collection")
			}
			seen[collection.CollectionName()] = true
			expectedRecords[collection.CollectionName()] = record
			expectedSizes[collection.CollectionName()] = dimensions
			info, err := collection.CollectionInfo(ctx)
			if err != nil {
				return err
			}
			if !info.Exists {
				actions = append(actions, bindingAction{collection, record, dimensions, true, map[string]any{MetadataKey: record}})
				continue
			}
			if info.VectorSize != dimensions {
				return errors.New("collection embedding dimension mismatch; migrate into a new collection")
			}
			stored, present := info.Metadata[MetadataKey]
			if present {
				if !equalBindingRecord(stored, record) {
					return errors.New("collection embedding identity mismatch; adoption cannot override identity")
				}
			} else {
				count, err := collection.ExactCount(ctx)
				if err != nil {
					return err
				}
				if count > 0 && (binding.Config.Provider != "tei" || binding.Config.InputProfile != embeddings.LegacyRawV1 || !adoptLegacy) {
					return errors.New("cannot adopt nonempty collection without verified legacy identity")
				}
				metadata := cloneMetadata(info.Metadata)
				metadata[MetadataKey] = record
				actions = append(actions, bindingAction{collection, record, dimensions, false, metadata})
			}
		}
	}
	for _, action := range actions {
		var err error
		if action.create {
			err = action.collection.CreateCollection(ctx, action.dimensions, action.metadata)
		} else {
			err = action.collection.UpdateCollectionMetadata(ctx, action.metadata)
		}
		if err != nil {
			return err
		}
	}
	for _, binding := range bindings {
		for _, collection := range binding.Collections {
			info, err := collection.CollectionInfo(ctx)
			if err != nil {
				return err
			}
			expected := expectedRecords[collection.CollectionName()]
			if !info.Exists || info.VectorSize != expectedSizes[collection.CollectionName()] || !equalBindingRecord(info.Metadata[MetadataKey], expected) {
				return errors.New("embedding identity changed or was not persisted")
			}
		}
	}
	return nil
}

func equalBindingRecord(stored, expected any) bool {
	// Schema 1 retains its historical absent-input-profile normalization.
	if legacy, ok := expected.(Record); ok {
		decoded, err := decodeRecord(stored)
		return err == nil && reflect.DeepEqual(decoded, legacy)
	}
	a, err := json.Marshal(stored)
	if err != nil {
		return false
	}
	b, err := json.Marshal(expected)
	if err != nil {
		return false
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// ValidateStoredIdentity checks a migration source without contacting its old
// provider. The source vectors are never reinterpreted as the target identity.
func ValidateStoredIdentity(raw any, dimensions int) error {
	b, err := json.Marshal(raw)
	if err != nil {
		return errors.New("invalid source embedding identity")
	}
	var version struct {
		SchemaVersion int `json:"schema_version"`
	}
	if json.Unmarshal(b, &version) != nil {
		return errors.New("invalid source embedding identity")
	}
	if version.SchemaVersion == 1 {
		record, err := decodeRecord(raw)
		if err != nil || record.VectorSize != dimensions {
			return errors.New("invalid legacy source embedding identity")
		}
		return nil
	}
	if version.SchemaVersion != 2 {
		return errors.New("unsupported source embedding identity")
	}
	var record embeddings.Descriptor
	if json.Unmarshal(b, &record) != nil || record.Model == "" || record.InputProfile == "" || record.Dimensions != dimensions || record.Normalization != "l2" {
		return errors.New("invalid source embedding identity")
	}
	cfg := embeddings.BackendConfig{Provider: record.Provider, Model: record.Model, InputProfile: record.InputProfile, Dimensions: record.Dimensions, Revision: record.Revision, WeightsSHA256: record.WeightsSHA256}
	switch record.Provider {
	case "llamacpp":
		if record.WeightsProvenance != "declared" || record.ModelProvenance != "" {
			return errors.New("invalid source weight provenance")
		}
		cfg.URL = "http://identity-validation.invalid"
	case "openai", "google":
		if record.ModelProvenance != "provider-model-id" || record.WeightsProvenance != "" {
			return errors.New("invalid cloud source provenance")
		}
		cfg.APIKeyEnv = "IDENTITY_VALIDATION_KEY"
	default:
		return errors.New("unsupported source embedding provider")
	}
	if record.TEI != nil || embeddings.ValidateBackendConfig(cfg) != nil {
		return errors.New("invalid source embedding contract")
	}

	return nil
}
