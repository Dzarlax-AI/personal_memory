package eval

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLoadV3RejectsMissingAndNullRequiredFields(t *testing.T) {
	tests := []struct {
		name   string
		path   []any
		field  string
		remove bool
	}{
		{"dataset schema missing", nil, "schema_version", true},
		{"dataset schema null", nil, "schema_version", false},
		{"dataset version missing", nil, "dataset_version", true},
		{"dataset version null", nil, "dataset_version", false},
		{"dataset queries missing", nil, "queries", true},
		{"dataset queries null", nil, "queries", false},
		{"embedding vector size missing", []any{"embedding"}, "vector_size", true},
		{"embedding vector size null", []any{"embedding"}, "vector_size", false},
		{"configuration dense limit missing", []any{"configuration"}, "dense_candidate_limit", true},
		{"configuration dense limit null", []any{"configuration"}, "dense_candidate_limit", false},
		{"gates invariant boolean missing", []any{"gates"}, "forbid_invariant_violations", true},
		{"gates invariant boolean null", []any{"gates"}, "forbid_invariant_violations", false},
		{"gates lifecycle boolean missing", []any{"gates"}, "forbid_lifecycle_violations", true},
		{"gates lifecycle boolean null", []any{"gates"}, "forbid_lifecycle_violations", false},
		{"fixture payload missing", []any{"facts", 0}, "payload", true},
		{"fixture payload null", []any{"facts", 0}, "payload", false},
		{"fixture vector missing", []any{"facts", 0}, "vector", true},
		{"fixture vector null", []any{"facts", 0}, "vector", false},
		{"query id missing", []any{"queries", 0}, "id", true},
		{"query id null", []any{"queries", 0}, "id", false},
		{"query expected missing", []any{"queries", 0}, "expected", true},
		{"query expected null", []any{"queries", 0}, "expected", false},
		{"query cohorts missing", []any{"queries", 0}, "cohorts", true},
		{"query cohorts null", []any{"queries", 0}, "cohorts", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document := decodeTestDocument(t, []byte(validV3Dataset()))
			object := nestedTestObject(t, document, tt.path...)
			if tt.remove {
				delete(object, tt.field)
			} else {
				object[tt.field] = nil
			}
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Load(strings.NewReader(string(encoded))); err == nil ||
				!strings.Contains(err.Error(), tt.field) {
				t.Fatalf("Load() error = %v, want field %q rejection", err, tt.field)
			}
		})
	}
}

func TestDecodeReportV3RejectsMissingAndNullRequiredFields(t *testing.T) {
	tests := []struct {
		name   string
		path   []any
		field  string
		remove bool
	}{
		{"report schema missing", nil, "schema_version", true},
		{"report schema null", nil, "schema_version", false},
		{"report mode missing", nil, "mode", true},
		{"report mode null", nil, "mode", false},
		{"report top k missing", nil, "top_k", true},
		{"report top k null", nil, "top_k", false},
		{"report gates passed missing", nil, "gates_passed", true},
		{"report gates passed null", nil, "gates_passed", false},
		{"query results missing", []any{"queries", 0}, "results", true},
		{"query results null", []any{"queries", 0}, "results", false},
		{"retrieved score missing", []any{"queries", 0, "results", 0}, "score", true},
		{"retrieved score null", []any{"queries", 0, "results", 0}, "score", false},
		{"retrieved id missing", []any{"queries", 0, "results", 0}, "id", true},
		{"retrieved id null", []any{"queries", 0, "results", 0}, "id", false},
		{"query metric mrr missing", []any{"queries", 0, "metrics"}, "mrr", true},
		{"query metric mrr null", []any{"queries", 0, "metrics"}, "mrr", false},
		{"query metric hit map missing", []any{"queries", 0, "metrics"}, "hit_at", true},
		{"query metric hit map null", []any{"queries", 0, "metrics"}, "hit_at", false},
		{"query metric nDCG map missing", []any{"queries", 0, "metrics"}, "ndcg_at", true},
		{"query metric nDCG map null", []any{"queries", 0, "metrics"}, "ndcg_at", false},
		{"aggregate mrr missing", []any{"aggregate"}, "mrr", true},
		{"aggregate mrr null", []any{"aggregate"}, "mrr", false},
		{"aggregate hit map missing", []any{"aggregate"}, "hit_at", true},
		{"aggregate hit map null", []any{"aggregate"}, "hit_at", false},
		{"aggregate invariant count missing", []any{"aggregate"}, "invariant_violations", true},
		{"aggregate invariant count null", []any{"aggregate"}, "invariant_violations", false},
		{"cohort query count missing", []any{"cohorts", 0}, "query_count", true},
		{"cohort query count null", []any{"cohorts", 0}, "query_count", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := validV3ComparisonReport()
			report.Queries[0].Results = []RetrievedItem{{ID: "1", Score: 0}}
			data, err := RenderJSON(report)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeTestDocument(t, data)
			object := nestedTestObject(t, document, tt.path...)
			if tt.remove {
				delete(object, tt.field)
			} else {
				object[tt.field] = nil
			}
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeReport(encoded); err == nil ||
				!strings.Contains(err.Error(), tt.field) {
				t.Fatalf("DecodeReport() error = %v, want field %q rejection", err, tt.field)
			}
		})
	}
}

func decodeTestDocument(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func nestedTestObject(t *testing.T, root map[string]any, path ...any) map[string]any {
	t.Helper()
	var current any = root
	for _, segment := range path {
		switch value := segment.(type) {
		case string:
			current = current.(map[string]any)[value]
		case int:
			current = current.([]any)[value]
		default:
			t.Fatalf("unsupported path segment %#v", segment)
		}
	}
	object, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("path %#v resolved to %T", path, current)
	}
	return object
}
