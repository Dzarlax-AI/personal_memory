package embeddingidentity

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

const testRevision = "614241f622f53c4eeff9890bdc4f31cfecc418b3"

type fakeModel struct {
	info     embeddings.ModelInfo
	vector   []float32
	infoErr  error
	embedErr error
	probe    string
}

func newFakeModel() *fakeModel {
	model := &fakeModel{vector: []float32{1, 2, 3}}
	model.info.ModelID = "intfloat/multilingual-e5-small"
	model.info.ModelSHA = testRevision
	model.info.ModelDType = "float32"
	model.info.ModelType.Embedding.Pooling = "mean"
	model.info.Version = "1.9.3"
	return model
}

func (m *fakeModel) Info(context.Context) (embeddings.ModelInfo, error) {
	return m.info, m.infoErr
}

func (m *fakeModel) Embed(_ context.Context, text string) ([]float32, error) {
	m.probe = text
	return m.vector, m.embedErr
}

type fakeCollection struct {
	name          string
	info          qdrant.CollectionInfo
	infoErr       error
	exactPoints   uint64
	countErr      error
	createErr     error
	updateErr     error
	created       int
	updated       int
	metadata      map[string]any
	vectorSize    int
	discardWrites bool
}

func (c *fakeCollection) CollectionName() string { return c.name }
func (c *fakeCollection) CollectionInfo(context.Context) (qdrant.CollectionInfo, error) {
	return c.info, c.infoErr
}
func (c *fakeCollection) ExactCount(context.Context) (uint64, error) {
	return c.exactPoints, c.countErr
}
func (c *fakeCollection) CreateCollection(_ context.Context, size int, metadata map[string]any) error {
	c.created++
	c.vectorSize = size
	c.metadata = metadata
	if !c.discardWrites {
		c.info = qdrant.CollectionInfo{Exists: true, VectorSize: size, Metadata: metadata}
	}
	return c.createErr
}
func (c *fakeCollection) UpdateCollectionMetadata(_ context.Context, metadata map[string]any) error {
	c.updated++
	c.metadata = metadata
	if !c.discardWrites {
		c.info.Metadata = metadata
	}
	return c.updateErr
}

func expectedModel() Expected {
	return Expected{ModelID: "intfloat/multilingual-e5-small", ModelRevision: testRevision}
}

func testRecord() Record {
	return Record{SchemaVersion: 1, Provider: "tei", ModelID: expectedModel().ModelID, ModelRevision: testRevision, ModelDType: "float32", Pooling: "mean", VectorSize: 3}
}

func existingCollection(name string, points uint64, metadata map[string]any) *fakeCollection {
	return &fakeCollection{name: name, exactPoints: points, info: qdrant.CollectionInfo{Exists: true, Points: points, VectorSize: 3, Metadata: metadata}}
}

func TestEnsureCreatesMissingCollectionWithIdentity(t *testing.T) {
	model := newFakeModel()
	collection := &fakeCollection{name: "memory"}
	record, err := ensure(context.Background(), model, []collectionClient{collection}, expectedModel(), false)
	if err != nil {
		t.Fatal(err)
	}
	if record != testRecord() || collection.created != 1 || collection.updated != 0 || collection.vectorSize != 3 {
		t.Fatalf("record=%#v collection=%#v", record, collection)
	}
	if !reflect.DeepEqual(collection.metadata[MetadataKey], record) {
		t.Fatalf("metadata = %#v, want record %#v", collection.metadata, record)
	}
	if model.probe != identityProbeText {
		t.Fatalf("probe text = %q", model.probe)
	}
}

func TestEnsureBindsEmptyLegacyCollectionAndPreservesMetadata(t *testing.T) {
	collection := existingCollection("memory", 0, map[string]any{"operator.note": "keep"})
	_, err := ensure(context.Background(), newFakeModel(), []collectionClient{collection}, expectedModel(), false)
	if err != nil {
		t.Fatal(err)
	}
	if collection.updated != 1 || collection.metadata["operator.note"] != "keep" {
		t.Fatalf("collection = %#v", collection)
	}
}

func TestEnsureRequiresExplicitAdoptionForNonEmptyLegacyCollection(t *testing.T) {
	collection := existingCollection("memory", 556, nil)
	_, err := ensure(context.Background(), newFakeModel(), []collectionClient{collection}, expectedModel(), false)
	if err == nil || !strings.Contains(err.Error(), "ADOPT_EXISTING_EMBEDDING_IDENTITY=true") {
		t.Fatalf("error = %v", err)
	}
	if collection.created != 0 || collection.updated != 0 {
		t.Fatal("legacy collection was modified without adoption")
	}

	if _, err := ensure(context.Background(), newFakeModel(), []collectionClient{collection}, expectedModel(), true); err != nil {
		t.Fatal(err)
	}
	if collection.updated != 1 {
		t.Fatalf("updates = %d, want 1", collection.updated)
	}
}

func TestEnsureUsesExactCountForLegacyAdoptionDecision(t *testing.T) {
	collection := existingCollection("memory", 9, nil)
	collection.info.Points = 0
	_, err := ensure(context.Background(), newFakeModel(), []collectionClient{collection}, expectedModel(), false)
	if err == nil || !strings.Contains(err.Error(), "contains 9 points") {
		t.Fatalf("error = %v", err)
	}
	if collection.created != 0 || collection.updated != 0 {
		t.Fatal("legacy collection was modified based on approximate point count")
	}
}

func TestEnsureAcceptsExactStoredIdentity(t *testing.T) {
	collection := existingCollection("memory", 556, identityMetadata(testRecord()))
	if _, err := ensure(context.Background(), newFakeModel(), []collectionClient{collection}, expectedModel(), false); err != nil {
		t.Fatal(err)
	}
	if collection.updated != 0 || collection.created != 0 {
		t.Fatal("exact identity caused a write")
	}
}

func TestEnsureRejectsEveryIdentityMismatchEvenWithAdoption(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "schema", mutate: func(r *Record) { r.SchemaVersion = 2 }},
		{name: "provider", mutate: func(r *Record) { r.Provider = "other" }},
		{name: "model", mutate: func(r *Record) { r.ModelID = "other/model" }},
		{name: "revision", mutate: func(r *Record) { r.ModelRevision = strings.Repeat("a", 40) }},
		{name: "dtype", mutate: func(r *Record) { r.ModelDType = "float16" }},
		{name: "pooling", mutate: func(r *Record) { r.Pooling = "cls" }},
		{name: "size", mutate: func(r *Record) { r.VectorSize = 4 }},
	}
	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			stored := testRecord()
			tt.mutate(&stored)
			collection := existingCollection("memory", 1, identityMetadata(stored))
			_, err := ensure(context.Background(), newFakeModel(), []collectionClient{collection}, expectedModel(), true)
			if err == nil || !strings.Contains(err.Error(), "mismatch") && !strings.Contains(err.Error(), "invalid") {
				t.Fatalf("error = %v", err)
			}
			if collection.updated != 0 || collection.created != 0 {
				t.Fatal("mismatch caused a write")
			}
		})
	}
}

func TestEnsureRejectsCollectionVectorSizeMismatch(t *testing.T) {
	collection := existingCollection("memory", 0, nil)
	collection.info.VectorSize = 384
	_, err := ensure(context.Background(), newFakeModel(), []collectionClient{collection}, expectedModel(), true)
	if err == nil || !strings.Contains(err.Error(), "vector size") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnsurePreflightFailurePerformsNoEarlierWrites(t *testing.T) {
	first := existingCollection("memory", 0, nil)
	second := existingCollection("doc_chunks", 1, identityMetadata(Record{SchemaVersion: 2}))
	_, err := ensure(context.Background(), newFakeModel(), []collectionClient{first, second}, expectedModel(), true)
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if first.updated != 0 || first.created != 0 {
		t.Fatal("first collection was modified before full preflight succeeded")
	}
}

func TestEnsureRejectsActiveTEIMismatchAndDependencyErrors(t *testing.T) {
	t.Run("configured identity", func(t *testing.T) {
		model := newFakeModel()
		model.info.ModelSHA = "main"
		_, err := ensure(context.Background(), model, nil, expectedModel(), false)
		if err == nil || !strings.Contains(err.Error(), "TEI model identity mismatch") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("info", func(t *testing.T) {
		model := newFakeModel()
		model.infoErr = errors.New("offline")
		_, err := ensure(context.Background(), model, nil, expectedModel(), false)
		if err == nil || !strings.Contains(err.Error(), "read active") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("embed", func(t *testing.T) {
		model := newFakeModel()
		model.embedErr = errors.New("offline")
		_, err := ensure(context.Background(), model, nil, expectedModel(), false)
		if err == nil || !strings.Contains(err.Error(), "identity probe") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestEnsureWrapsCollectionAndMutationErrors(t *testing.T) {
	t.Run("inspect", func(t *testing.T) {
		collection := &fakeCollection{name: "memory", infoErr: errors.New("offline")}
		_, err := ensure(context.Background(), newFakeModel(), []collectionClient{collection}, expectedModel(), false)
		if err == nil || !strings.Contains(err.Error(), "inspect collection") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("exact count", func(t *testing.T) {
		collection := existingCollection("memory", 0, nil)
		collection.countErr = errors.New("offline")
		_, err := ensure(context.Background(), newFakeModel(), []collectionClient{collection}, expectedModel(), false)
		if err == nil || !strings.Contains(err.Error(), "count collection") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("create", func(t *testing.T) {
		collection := &fakeCollection{name: "memory", createErr: errors.New("denied")}
		_, err := ensure(context.Background(), newFakeModel(), []collectionClient{collection}, expectedModel(), false)
		if err == nil || !strings.Contains(err.Error(), "create collection") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("update", func(t *testing.T) {
		collection := existingCollection("memory", 0, nil)
		collection.updateErr = errors.New("denied")
		_, err := ensure(context.Background(), newFakeModel(), []collectionClient{collection}, expectedModel(), false)
		if err == nil || !strings.Contains(err.Error(), "bind embedding identity") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("write verification", func(t *testing.T) {
		collection := existingCollection("memory", 0, nil)
		collection.discardWrites = true
		_, err := ensure(context.Background(), newFakeModel(), []collectionClient{collection}, expectedModel(), false)
		if err == nil || !strings.Contains(err.Error(), "was not persisted") {
			t.Fatalf("error = %v", err)
		}
	})
}
