package eval

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
)

func vectorlessV3Dataset(t *testing.T) string {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(validV3Dataset()), &document); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"facts", "chunks", "folders"} {
		for _, rawPoint := range document[field].([]any) {
			delete(rawPoint.(map[string]any), "vector")
		}
	}
	for _, rawQuery := range document["queries"].([]any) {
		rawQuery.(map[string]any)["vector"] = []any{}
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type materializeCall struct {
	purpose embeddings.Purpose
	profile embeddings.InputProfile
	modelID string
	texts   []string
}

type materializeEmbedder struct {
	calls       []materializeCall
	failPurpose embeddings.Purpose
	vectorSize  int
}

func (embedder *materializeEmbedder) EmbedWithPurpose(
	context.Context,
	string,
	embeddings.Purpose,
	embeddings.InputProfile,
	string,
) ([]float32, error) {
	return nil, errors.New("materialization must use purpose batches")
}

func (embedder *materializeEmbedder) EmbedBatchWithPurpose(
	_ context.Context,
	texts []string,
	purpose embeddings.Purpose,
	profile embeddings.InputProfile,
	modelID string,
) ([][]float32, error) {
	embedder.calls = append(embedder.calls, materializeCall{
		purpose: purpose,
		profile: profile,
		modelID: modelID,
		texts:   append([]string(nil), texts...),
	})
	if purpose == embedder.failPurpose {
		return nil, errors.New("injected embedding failure")
	}
	size := embedder.vectorSize
	if size == 0 {
		size = 2
	}
	vectors := make([][]float32, len(texts))
	for i := range texts {
		vectors[i] = make([]float32, size)
		vectors[i][i%size] = 1
	}
	return vectors, nil
}

func TestLoadForMaterializationIsTheOnlyVectorlessDecodePath(t *testing.T) {
	input := vectorlessV3Dataset(t)
	if _, err := Load(strings.NewReader(input)); err == nil ||
		!strings.Contains(err.Error(), `field "vector" is required`) {
		t.Fatalf("Load() error = %v, want ordinary strict decode rejection", err)
	}
	dataset, err := LoadForMaterialization(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.Facts) == 0 || len(dataset.Facts[0].Vector) != 0 ||
		len(dataset.Queries) == 0 || len(dataset.Queries[0].Vector) != 0 {
		t.Fatalf("vectorless dataset decoded unexpectedly: %#v", dataset)
	}
}

func TestLoadForMaterializationRetainsStrictV3Contract(t *testing.T) {
	tests := []struct {
		name   string
		input  func(t *testing.T) string
		needle string
	}{
		{
			name: "v1",
			input: func(t *testing.T) string {
				return validDataset
			},
			needle: "schema_version 3",
		},
		{
			name: "v2",
			input: func(t *testing.T) string {
				return validV2Dataset()
			},
			needle: "schema_version 3",
		},
		{
			name: "unknown field",
			input: func(t *testing.T) string {
				var document map[string]any
				if err := json.Unmarshal([]byte(vectorlessV3Dataset(t)), &document); err != nil {
					t.Fatal(err)
				}
				document["surprise"] = true
				data, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			},
			needle: "unknown field",
		},
		{
			name: "null vector",
			input: func(t *testing.T) string {
				var document map[string]any
				if err := json.Unmarshal([]byte(vectorlessV3Dataset(t)), &document); err != nil {
					t.Fatal(err)
				}
				document["facts"].([]any)[0].(map[string]any)["vector"] = nil
				data, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			},
			needle: "must not be null",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadForMaterialization(strings.NewReader(test.input(t)))
			if err == nil || !strings.Contains(err.Error(), test.needle) {
				t.Fatalf("error = %v, want containing %q", err, test.needle)
			}
		})
	}
}

func TestMaterializeUsesStablePurposeBatchesAndDoesNotMutateSource(t *testing.T) {
	dataset, err := LoadForMaterialization(strings.NewReader(vectorlessV3Dataset(t)))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Chunks = cloneFixturePoints(dataset.Facts[:1])
	dataset.Chunks[0].Payload["text"] = "chunk text"
	dataset.Folders = cloneFixturePoints(dataset.Facts[:1])
	dataset.Folders[0].Payload["text"] = "folder text"
	dataset.Facts[0].Payload["nested"] = map[string]any{"items": []any{map[string]any{"value": "source"}}}
	dataset.Embedding.ModelID = "intfloat/multilingual-e5-small"
	dataset.Configuration.TopK = []int{3, 1}
	before, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	profile := MultilingualE5V1
	embedder := &materializeEmbedder{}

	got, diagnostics, err := Materialize(context.Background(), dataset, embedder, MaterializeOptions{
		InputProfile: &profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPurposes := []embeddings.Purpose{
		embeddings.FactPassage,
		embeddings.ChunkPassage,
		embeddings.FolderPassage,
		embeddings.RetrievalQuery,
	}
	if len(embedder.calls) != len(wantPurposes) {
		t.Fatalf("calls = %#v", embedder.calls)
	}
	for i, purpose := range wantPurposes {
		if embedder.calls[i].purpose != purpose ||
			embedder.calls[i].profile != embeddings.MultilingualE5V1 ||
			embedder.calls[i].modelID != dataset.Embedding.ModelID {
			t.Fatalf("call %d = %#v", i, embedder.calls[i])
		}
	}
	if !reflect.DeepEqual(embedder.calls[0].texts, []string{"numeric", "string"}) ||
		!reflect.DeepEqual(embedder.calls[1].texts, []string{"chunk text"}) ||
		!reflect.DeepEqual(embedder.calls[2].texts, []string{"folder text"}) ||
		!reflect.DeepEqual(embedder.calls[3].texts, []string{"numeric"}) {
		t.Fatalf("batch inputs = %#v", embedder.calls)
	}
	if diagnostics.Facts != 2 || diagnostics.Chunks != 1 || diagnostics.Folders != 1 ||
		diagnostics.Queries != 1 || diagnostics.InputProfile != profile {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	if got.Embedding.InputProfile != profile ||
		len(got.Facts[0].Vector) != dataset.Embedding.VectorSize ||
		len(got.Queries[0].Vector) != dataset.Embedding.VectorSize {
		t.Fatalf("materialized identity/vectors = %#v", got)
	}
	after, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("Materialize mutated its source dataset")
	}
	got.Facts[0].Payload["nested"].(map[string]any)["items"].([]any)[0].(map[string]any)["value"] = "changed"
	if dataset.Facts[0].Payload["nested"].(map[string]any)["items"].([]any)[0].(map[string]any)["value"] != "source" {
		t.Fatal("materialized payload shares nested state with source")
	}
}

func TestMaterializeRejectsMissingCorpusTextBeforeEmbedding(t *testing.T) {
	dataset, err := LoadForMaterialization(strings.NewReader(vectorlessV3Dataset(t)))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts[0].Payload["text"] = " "
	embedder := &materializeEmbedder{}
	_, _, err = Materialize(context.Background(), dataset, embedder, MaterializeOptions{})
	if err == nil || !strings.Contains(err.Error(), `facts point "42"`) {
		t.Fatalf("error = %v", err)
	}
	if len(embedder.calls) != 0 {
		t.Fatalf("embedding started before text validation: %#v", embedder.calls)
	}
}

func TestMaterializeRejectsInvalidOutputWithoutReturningPartialDataset(t *testing.T) {
	dataset, err := LoadForMaterialization(strings.NewReader(vectorlessV3Dataset(t)))
	if err != nil {
		t.Fatal(err)
	}
	embedder := &materializeEmbedder{vectorSize: 1}
	got, _, err := Materialize(context.Background(), dataset, embedder, MaterializeOptions{})
	if err == nil || !strings.Contains(err.Error(), "vector length") {
		t.Fatalf("error = %v", err)
	}
	if got != nil {
		t.Fatalf("partial dataset returned: %#v", got)
	}
	if len(dataset.Facts[0].Vector) != 0 {
		t.Fatal("failed materialization mutated source vectors")
	}
}

func TestMaterializeFailureStopsBeforeLaterPurposeBatches(t *testing.T) {
	dataset, err := LoadForMaterialization(strings.NewReader(vectorlessV3Dataset(t)))
	if err != nil {
		t.Fatal(err)
	}
	embedder := &materializeEmbedder{failPurpose: embeddings.FolderPassage}
	got, _, err := Materialize(context.Background(), dataset, embedder, MaterializeOptions{})
	if err == nil || !strings.Contains(err.Error(), "embed folders") {
		t.Fatalf("error = %v", err)
	}
	if got != nil {
		t.Fatalf("partial dataset returned: %#v", got)
	}
	if len(embedder.calls) != 3 {
		t.Fatalf("calls = %#v, want stop at folder failure", embedder.calls)
	}
}

func TestMaterializedDatasetRendersAndRoundTripsOrdinaryStrictDecode(t *testing.T) {
	dataset, err := LoadForMaterialization(strings.NewReader(vectorlessV3Dataset(t)))
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := Materialize(context.Background(), dataset, &materializeEmbedder{}, MaterializeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := RenderDatasetJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Load(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("ordinary strict round-trip failed: %v\n%s", err, data)
	}
	if err := decoded.ValidateForSource("fixture"); err != nil {
		t.Fatalf("materialized output is not a deterministic fixture: %v", err)
	}
}
