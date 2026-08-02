package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAggregateCohortsUsesQueryMembershipAndSorts(t *testing.T) {
	queries := []QueryReport{
		{
			ID: "b", Cohorts: []QueryCohort{CohortMultilingual, CohortExactName},
			Metrics: QueryMetrics{
				MRR: 0.5, HitAt: map[int]float64{1: 0, 3: 1},
				NDCGAt:              map[int]float64{1: 0, 3: 0.5},
				InvariantViolations: []string{"1"},
			},
		},
		{
			ID: "a", Cohorts: []QueryCohort{CohortExactName},
			Metrics: QueryMetrics{
				MRR: 1, HitAt: map[int]float64{1: 1, 3: 1},
				NDCGAt: map[int]float64{1: 1, 3: 1},
			},
		},
	}
	got := AggregateCohorts(queries, []int{1, 3})
	if len(got) != 2 || got[0].Cohort != CohortExactName || got[1].Cohort != CohortMultilingual {
		t.Fatalf("cohorts = %#v", got)
	}
	exact := got[0]
	if exact.QueryCount != 2 || exact.MRR != 0.75 || exact.HitAt[1] != 0.5 ||
		exact.HitAt[3] != 1 || exact.NDCGAt[1] != 0.5 ||
		exact.NDCGAt[3] != 0.75 || exact.InvariantViolations != 1 {
		t.Fatalf("exact-name aggregate = %#v", exact)
	}
}

func TestDecodeReportV3RejectsClaimedCohortAggregateDrift(t *testing.T) {
	report := validV3ComparisonReport()
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(data); err != nil {
		t.Fatalf("DecodeReport(valid v3): %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	cohort := document["cohorts"].([]any)[0].(map[string]any)
	cohort["mrr"] = 0.123
	tampered, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(tampered); err == nil || !strings.Contains(err.Error(), "cohort aggregates") {
		t.Fatalf("DecodeReport() error = %v, want cohort aggregate rejection", err)
	}
}

func TestDecodeReportV3RejectsMissingOrMalformedIdentityAndCohorts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "missing profile",
			mutate: func(document map[string]any) {
				delete(document["embedding"].(map[string]any), "input_profile")
			},
			want: "input_profile",
		},
		{
			name: "null profile",
			mutate: func(document map[string]any) {
				document["embedding"].(map[string]any)["input_profile"] = nil
			},
			want: "input_profile",
		},
		{
			name: "missing retrieval strategy",
			mutate: func(document map[string]any) {
				delete(document["configuration"].(map[string]any), "retrieval_strategy")
			},
			want: "retrieval_strategy",
		},
		{
			name: "null RRF constant",
			mutate: func(document map[string]any) {
				document["configuration"].(map[string]any)["rrf_constant"] = nil
			},
			want: "rrf_constant",
		},
		{
			name: "null report cohorts",
			mutate: func(document map[string]any) {
				document["cohorts"] = nil
			},
			want: "cohorts",
		},
		{
			name: "null query cohorts",
			mutate: func(document map[string]any) {
				document["queries"].([]any)[0].(map[string]any)["cohorts"] = nil
			},
			want: "cohorts",
		},
		{
			name: "null cohort metric",
			mutate: func(document map[string]any) {
				document["cohorts"].([]any)[0].(map[string]any)["mrr"] = nil
			},
			want: "mrr",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := RenderJSON(validV3ComparisonReport())
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			tt.mutate(document)
			mutated, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeReport(mutated); err == nil ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DecodeReport() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDecodeHistoricalPublicReportsPreservesAbsentV3Fields(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "evaldata", "public", version, "baseline.json"))
			if err != nil {
				t.Fatal(err)
			}
			report, err := DecodeReport(data)
			if err != nil {
				t.Fatal(err)
			}
			if report.Embedding.InputProfile != "" ||
				report.Configuration.RetrievalStrategy != "" ||
				report.Cohorts != nil {
				t.Fatalf("historical v3 fields were synthesized: %#v", report)
			}
			rendered, err := RenderJSON(report)
			if err != nil {
				t.Fatal(err)
			}
			if string(rendered) != string(data) {
				t.Fatalf("historical report changed across decode/render")
			}
		})
	}
}

func TestDecodeHistoricalReportRejectsV3IdentityFields(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "evaldata", "public", "v2", "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["embedding"].(map[string]any)["input_profile"] = string(LegacyRawV1)
	mutated, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(mutated); err == nil ||
		!strings.Contains(err.Error(), "schema_version 3") {
		t.Fatalf("DecodeReport() error = %v, want v3 field rejection", err)
	}
}
