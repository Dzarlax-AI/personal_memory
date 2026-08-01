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
    {"id": "fact-2", "vector": [0, 1], "payload": {"text": "string"}}
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
    "forbidden_ids": ["fact-2"]
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
	if dataset.Facts[1].ID.String() != "fact-2" || dataset.Facts[1].ID.IsNumeric() {
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
		{"unknown schema", `"schema_version": 1`, `"schema_version": 2`, "schema_version"},
		{"duplicate point ID", `"id": "fact-2"`, `"id": 42`, "duplicate facts point ID"},
		{"wrong vector dimension", `"vector": [0, 1]`, `"vector": [0]`, "vector length"},
		{"missing expectations", `"expected": [{"id": "42", "grade": 3}]`, `"expected": []`, "expected"},
		{"invalid mode", `"mode": "flat"`, `"mode": "magic"`, "mode"},
		{"missing expected ID", `"id": "42", "grade": 3`, `"id": "missing", "grade": 3`, "unknown expected ID"},
		{"non-finite vector", `"vector": [1, 0]`, `"vector": [1, 1e999]`, "finite"},
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

func TestLoadRejectsTrailingJSON(t *testing.T) {
	_, err := Load(strings.NewReader(validDataset + `{}`))
	if err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("Load() error = %v, want trailing JSON error", err)
	}
}
