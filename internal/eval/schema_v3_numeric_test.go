package eval

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLoadV3RejectsInvalidGateThresholdNumbers(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value any
	}{
		{"minimum mrr null", "minimum_mrr", nil},
		{"minimum mrr string", "minimum_mrr", "SECRET_INVALID"},
		{"minimum mrr boolean", "minimum_mrr", true},
		{"minimum mrr non-finite", "minimum_mrr", json.Number("1e999")},
		{"minimum hit leaf null", "minimum_hit_at", map[string]any{"1": nil}},
		{"minimum hit leaf string", "minimum_hit_at", map[string]any{"1": "SECRET_INVALID"}},
		{"minimum nDCG leaf boolean", "minimum_ndcg_at", map[string]any{"1": false}},
		{"minimum nDCG leaf non-finite", "minimum_ndcg_at", map[string]any{"1": json.Number("1e999")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document := decodeTestDocument(t, []byte(validV3Dataset()))
			document["gates"].(map[string]any)[tt.field] = tt.value
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Load(strings.NewReader(string(encoded)))
			if err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("Load() error = %v, want %q numeric rejection", err, tt.field)
			}
			if strings.Contains(err.Error(), "SECRET_INVALID") {
				t.Fatalf("Load() error echoed rejected content: %v", err)
			}
		})
	}
}

func TestDecodeReportV3RejectsInvalidMetricNumbers(t *testing.T) {
	tests := []struct {
		name  string
		path  []any
		field string
		value any
	}{
		{"true zero query HitAt rejects null", []any{"queries", 0, "metrics", "hit_at", "1"}, "hit_at", nil},
		{"query HitAt string", []any{"queries", 0, "metrics", "hit_at", "1"}, "hit_at", "SECRET_INVALID"},
		{"query nDCG boolean", []any{"queries", 0, "metrics", "ndcg_at", "1"}, "ndcg_at", true},
		{"query MRR non-finite", []any{"queries", 0, "metrics", "mrr"}, "mrr", json.Number("1e999")},
		{"aggregate HitAt null", []any{"aggregate", "hit_at", "1"}, "hit_at", nil},
		{"aggregate nDCG string", []any{"aggregate", "ndcg_at", "1"}, "ndcg_at", "SECRET_INVALID"},
		{"cohort HitAt boolean", []any{"cohorts", 0, "hit_at", "1"}, "hit_at", false},
		{"cohort nDCG non-finite", []any{"cohorts", 0, "ndcg_at", "1"}, "ndcg_at", json.Number("1e999")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := RenderJSON(validV3ComparisonReport())
			if err != nil {
				t.Fatal(err)
			}
			document := decodeTestDocument(t, data)
			setNestedTestValue(t, document, tt.value, tt.path...)
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			_, err = DecodeReport(encoded)
			if err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("DecodeReport() error = %v, want %q numeric rejection", err, tt.field)
			}
			if strings.Contains(err.Error(), "SECRET_INVALID") {
				t.Fatalf("DecodeReport() error echoed rejected content: %v", err)
			}
		})
	}
}

func TestDecodeReportV3RejectsInvalidCorpusDiagnosticNumbers(t *testing.T) {
	for _, field := range []string{"embedding_duration_us", "embedding_count"} {
		t.Run(field, func(t *testing.T) {
			report := validV3ComparisonReport()
			queryCount := len(report.Queries)
			report.Diagnostics = &Diagnostics{
				Query: QueryDiagnostics{
					Total:  DurationSummary{Count: queryCount},
					Embed:  DurationSummary{Count: queryCount},
					Search: DurationSummary{Count: queryCount},
				},
				Corpus: &CorpusDiagnostics{EmbeddingDurationUS: 1, EmbeddingCount: 1},
			}
			data, err := RenderJSON(report)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeTestDocument(t, data)
			setNestedTestValue(
				t, document, json.Number("1e999"), "diagnostics", "corpus", field,
			)
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeReport(encoded); err == nil ||
				!strings.Contains(err.Error(), field) {
				t.Fatalf("DecodeReport() error = %v, want %q numeric rejection", err, field)
			}
		})
	}
}

func setNestedTestValue(t *testing.T, root map[string]any, value any, path ...any) {
	t.Helper()
	if len(path) == 0 {
		t.Fatal("path is required")
	}
	var current any = root
	for _, segment := range path[:len(path)-1] {
		switch key := segment.(type) {
		case string:
			current = current.(map[string]any)[key]
		case int:
			current = current.([]any)[key]
		default:
			t.Fatalf("unsupported path segment %#v", segment)
		}
	}
	key, ok := path[len(path)-1].(string)
	if !ok {
		t.Fatalf("final path segment must be a field: %#v", path)
	}
	current.(map[string]any)[key] = value
}
