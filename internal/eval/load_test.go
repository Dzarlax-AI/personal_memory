package eval

import (
	"strings"
	"testing"
)

const validDataset = `{
  "schema_version": 1,
  "dataset_version": "1.0.0",
  "embedding": {
    "provider": "synthetic",
    "model_id": "synthetic-eval-v1",
    "model_revision": "v1",
    "dtype": "float32",
    "pooling": "mean",
    "vector_size": 2
  },
  "configuration": {
    "name": "test",
    "fact_collection": "memory",
    "chunk_collection": "doc_chunks",
    "folder_collection": "doc_folders",
    "folder_top_k": 1,
    "folder_threshold": 0.5,
    "top_k": [1, 3]
  },
  "facts": [
    {"id": 42, "vector": [1, 0], "payload": {"text": "numeric"}},
    {"id": "11111111-1111-4111-8111-111111111111", "vector": [0, 1], "payload": {"text": "string"}}
  ],
  "chunks": [],
  "folders": [],
  "queries": [{
    "id": "q1",
    "target": "facts",
    "mode": "flat",
    "text": "numeric",
    "vector": [1, 0],
    "expected": [{"id": "42", "grade": 3}],
    "forbidden_ids": ["11111111-1111-4111-8111-111111111111"]
  }],
  "gates": {"forbid_invariant_violations": true}
}`

func TestLoadAcceptsNumericAndStringPointIDs(t *testing.T) {
	dataset, err := Load(strings.NewReader(validDataset))
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Facts[0].ID.String() != "42" || !dataset.Facts[0].ID.IsNumeric() {
		t.Fatalf("numeric ID = %#v", dataset.Facts[0].ID)
	}
	if dataset.Facts[1].ID.String() != "11111111-1111-4111-8111-111111111111" || dataset.Facts[1].ID.IsNumeric() {
		t.Fatalf("string ID = %#v", dataset.Facts[1].ID)
	}
}

func TestLoadRejectsMalformedDatasets(t *testing.T) {
	tests := []struct {
		name    string
		replace string
		with    string
		want    string
	}{
		{"unknown schema", `"schema_version": 1`, `"schema_version": 3`, "schema_version"},
		{"duplicate point ID", `"id": "11111111-1111-4111-8111-111111111111"`, `"id": 42`, "duplicate facts point ID"},
		{"invalid string point ID", `"id": "11111111-1111-4111-8111-111111111111"`, `"id": "fact-2"`, "UUID"},
		{"wrong vector dimension", `"vector": [0, 1]`, `"vector": [0]`, "vector length"},
		{"missing expectations", `"expected": [{"id": "42", "grade": 3}]`, `"expected": []`, "expected"},
		{"invalid mode", `"mode": "flat"`, `"mode": "magic"`, "mode"},
		{"non-finite vector", `"vector": [1, 0]`, `"vector": [1, 1e999]`, "finite"},
		{"invalid expected ID", `"id": "42", "grade": 3`, `"id": "not-qdrant", "grade": 3`, "Qdrant point ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(strings.NewReader(strings.Replace(validDataset, tt.replace, tt.with, 1)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateForSourceChecksFixtureReferencesOnly(t *testing.T) {
	dataset, err := Load(strings.NewReader(validDataset))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts = nil
	if err := dataset.ValidateForSource("live"); err != nil {
		t.Fatalf("live validation rejected query-only dataset: %v", err)
	}
	if err := dataset.ValidateForSource("fixture"); err == nil || !strings.Contains(err.Error(), "unknown expected ID") {
		t.Fatalf("fixture validation error = %v, want unknown expected ID", err)
	}
}

func TestValidateRejectsInvalidForbiddenIDs(t *testing.T) {
	tests := []struct {
		name      string
		forbidden []string
		want      string
	}{
		{"duplicate", []string{"11111111-1111-4111-8111-111111111111", "11111111-1111-4111-8111-111111111111"}, "duplicate forbidden ID"},
		{"expected overlap", []string{"42"}, "both expected and forbidden"},
		{"invalid format", []string{"not-qdrant"}, "Qdrant point ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataset, err := Load(strings.NewReader(validDataset))
			if err != nil {
				t.Fatal(err)
			}
			dataset.Queries[0].ForbiddenIDs = tt.forbidden
			if err := dataset.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tt.want)
			}
		})
	}

	dataset, err := Load(strings.NewReader(validDataset))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Queries[0].ForbiddenIDs = []string{"22222222-2222-4222-8222-222222222222"}
	if err := dataset.ValidateForSource("live"); err != nil {
		t.Fatalf("live validation rejected unknown forbidden ID: %v", err)
	}
	if err := dataset.ValidateForSource("fixture"); err == nil || !strings.Contains(err.Error(), "unknown forbidden ID") {
		t.Fatalf("fixture validation error = %v, want unknown forbidden ID", err)
	}
}

func TestLoadRejectsTrailingJSON(t *testing.T) {
	_, err := Load(strings.NewReader(validDataset + `{}`))
	if err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("Load() error = %v, want trailing JSON error", err)
	}
}
