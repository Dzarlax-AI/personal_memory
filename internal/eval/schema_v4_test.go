package eval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/rerank"
)

type testRerankerIdentity struct{ modelID string }

func (r testRerankerIdentity) ModelID() string { return r.modelID }
func (testRerankerIdentity) Rerank(context.Context, string, []rerank.Candidate) ([]rerank.Ranked, error) {
	return nil, nil
}

func validV4Dataset() string {
	var document map[string]any
	if err := json.Unmarshal([]byte(validV3Dataset()), &document); err != nil {
		panic(err)
	}
	document["schema_version"] = float64(DocumentRoutingSchemaVersion)
	configuration := document["configuration"].(map[string]any)
	configuration["document_routing_strategy"] = string(DocumentRoutingBlendedRRF)
	configuration["routing_candidate_limit"] = float64(20)
	configuration["routing_rrf_constant"] = float64(60)
	encoded, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestSchemaV4RequiresIndependentBoundedDocumentRoutingStrategy(t *testing.T) {
	dataset, err := Load(strings.NewReader(validV4Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Configuration.DocumentRoutingStrategy != DocumentRoutingBlendedRRF ||
		dataset.Configuration.RetrievalStrategy != RetrievalVectorOnly {
		t.Fatalf("configuration = %#v", dataset.Configuration)
	}
	invalid := strings.Replace(validV4Dataset(), `"document_routing_strategy":"blended-rrf"`, `"document_routing_strategy":"unknown"`, 1)
	if _, err := Load(strings.NewReader(invalid)); err == nil || !strings.Contains(err.Error(), "document_routing_strategy") {
		t.Fatalf("invalid strategy error = %v", err)
	}
}

func TestSchemaV4CanonicalReportNormalizesScoresAndEmptySlices(t *testing.T) {
	report := normalizeReport(Report{
		SchemaVersion: DocumentRoutingSchemaVersion,
		Queries: []QueryReport{
			{ID: "empty", Results: []RetrievedItem{}},
			{ID: "scored", Results: []RetrievedItem{{ID: "candidate", Score: 0.1234567}}},
		},
		Cohorts: []CohortAggregateMetrics{},
	})
	if report.Queries == nil || report.Cohorts == nil || report.Queries[0].Results == nil {
		t.Fatal("schema v4 canonical empty slices must encode as arrays")
	}
	if got := report.Queries[1].Results[0].Score; got != 0.12346 {
		t.Fatalf("canonical score = %v, want 0.12346", got)
	}
}

func TestDecodeReportV3RejectsV4RoutingConfigurationAndTrace(t *testing.T) {
	base, err := RenderJSON(validV3ComparisonReport())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "configuration",
			mutate: func(document map[string]any) {
				document["configuration"].(map[string]any)["document_routing_strategy"] = "flat-only"
			},
			want: "document_routing_strategy",
		},
		{
			name: "query trace",
			mutate: func(document map[string]any) {
				document["queries"].([]any)[0].(map[string]any)["routing"] = map[string]any{
					"strategy": "flat-only", "reason_codes": []any{}, "selected_folders": []any{}, "results": []any{},
				}
			},
			want: "routing trace",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(base, &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(document)
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeReport(data); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeReport error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestSchemaV4RoutingCandidateBoundaryMatchesFusionCapacity(t *testing.T) {
	withLimit := func(limit float64) string {
		var document map[string]any
		if err := json.Unmarshal([]byte(validV4Dataset()), &document); err != nil {
			t.Fatal(err)
		}
		document["configuration"].(map[string]any)["routing_candidate_limit"] = limit
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if _, err := Load(strings.NewReader(withLimit(100))); err != nil {
		t.Fatalf("limit 100 rejected: %v", err)
	}
	if _, err := Load(strings.NewReader(withLimit(101))); err == nil || !strings.Contains(err.Error(), "between 1 and 100") {
		t.Fatalf("limit 101 error = %v", err)
	}
}

func TestRunSchemaV4FailsClosedForMissingOrMismatchedReranker(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal([]byte(validV4Dataset()), &document); err != nil {
		t.Fatal(err)
	}
	configuration := document["configuration"].(map[string]any)
	configuration["reranker_model_id"] = "model/revision"
	configuration["reranker_candidate_cap"] = float64(10)
	configuration["reranker_timeout_ms"] = float64(500)
	data, _ := json.Marshal(document)
	dataset, err := Load(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		reranker  rerank.Reranker
		wantError string
	}{
		{name: "missing", wantError: "no reranker service"},
		{name: "mismatched identity", reranker: testRerankerIdentity{modelID: "other/revision"}, wantError: "identity does not match"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Run(context.Background(), dataset, RunOptions{Source: "fixture", QdrantURL: "http://unused", Reranker: test.reranker})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Run error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}
