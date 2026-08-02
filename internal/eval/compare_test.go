package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareShowsDegradedRankingAndHonorsExplicitGates(t *testing.T) {
	minimum := 1.0
	baseline := Report{
		SchemaVersion: 1, DatasetVersion: "1.0.0", TopK: []int{1},
		Embedding:     EmbeddingIdentity{Provider: "synthetic", ModelID: "m", ModelRevision: "r", DType: "float32", Pooling: "mean", VectorSize: 2},
		Configuration: Configuration{Name: "cfg"},
		Aggregate:     AggregateMetrics{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 1}, MRR: 1},
		Queries:       []QueryReport{{ID: "q", Target: "facts", Mode: "flat", Results: []RetrievedItem{{ID: "a"}}, Metrics: QueryMetrics{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 1}, MRR: 1}}},
		GatesPassed:   true,
	}
	candidate := baseline
	candidate.Aggregate = AggregateMetrics{HitAt: map[int]float64{1: 0}, NDCGAt: map[int]float64{1: 0}, MRR: 0.5}
	candidate.Queries = []QueryReport{{ID: "q", Target: "facts", Mode: "flat", Results: []RetrievedItem{{ID: "b"}, {ID: "a"}}, Metrics: QueryMetrics{HitAt: map[int]float64{1: 0}, NDCGAt: map[int]float64{1: 0}, MRR: 0.5}}}
	candidate.GatesPassed = false
	candidate.GateFailures = EvaluateGates(candidate.Aggregate, Gates{MinimumMRR: &minimum})

	withoutGates, err := Compare(baseline, candidate, false)
	if err != nil {
		t.Fatal(err)
	}
	if !withoutGates.GatesPassed || withoutGates.Aggregate.MRR != -0.5 {
		t.Fatalf("comparison without gates = %#v", withoutGates)
	}
	withGates, err := Compare(baseline, candidate, true)
	if err != nil {
		t.Fatal(err)
	}
	if withGates.GatesPassed || len(withGates.GateFailures) != 1 {
		t.Fatalf("comparison with gates = %#v", withGates)
	}
}

func TestEvaluateGatesReportsMalformedKeys(t *testing.T) {
	failures := EvaluateGates(AggregateMetrics{}, Gates{
		MinimumHitAt:  map[string]float64{"bad": 1},
		MinimumNDCGAt: map[string]float64{"also-bad": 1},
	})
	if len(failures) != 2 || !strings.Contains(strings.Join(failures, "\n"), "not an integer") {
		t.Fatalf("failures = %#v", failures)
	}
}

func TestCompareKeepsV2LifecycleRegressionsVisible(t *testing.T) {
	base := Report{
		SchemaVersion: LifecycleSchemaVersion, DatasetVersion: "2",
		TopK: []int{1}, Configuration: Configuration{Name: "cfg"},
		Aggregate: AggregateMetrics{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 1}},
		Queries: []QueryReport{{
			ID: "q", Target: "facts", Mode: "flat", Metrics: QueryMetrics{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 1}},
			Lifecycle: &QueryLifecycleReport{Intent: QueryIntentCurrent, Checks: 1},
		}},
		Lifecycle:   &LifecycleReport{Aggregate: LifecycleAggregateMetrics{Checks: 1}},
		GatesPassed: true,
	}
	candidate := base
	candidate.Queries = []QueryReport{{
		ID: "q", Target: "facts", Mode: "flat", Metrics: QueryMetrics{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 1}},
		Lifecycle: &QueryLifecycleReport{
			Intent: QueryIntentCurrent, Checks: 1,
			Violations: []LifecycleViolation{{
				Scope: ViolationScopeQuery, QueryID: "q",
				CandidateID: "1", Invariant: InvariantCandidatePresent,
			}},
		},
	}}
	candidate.Lifecycle = &LifecycleReport{Aggregate: LifecycleAggregateMetrics{Checks: 1, Violations: 1}}
	comparison, err := Compare(base, candidate, false)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.SchemaVersion != LifecycleSchemaVersion || comparison.Lifecycle == nil ||
		len(comparison.Lifecycle.CandidateViolations) != 1 {
		t.Fatalf("comparison = %#v", comparison)
	}
}

func TestCompareRejectsUnsafeLifecycleViolationIdentifiers(t *testing.T) {
	baseline := validV2LifecycleReport()
	candidate := validV2LifecycleReport()
	candidate.Queries[0].Lifecycle.Checks = 1
	candidate.Queries[0].Lifecycle.Violations = []LifecycleViolation{{
		Scope: ViolationScopeQuery, QueryID: "private query text",
		CandidateID: "1", Invariant: InvariantCandidatePresent,
	}}
	candidate.Lifecycle.Aggregate = LifecycleAggregateMetrics{Checks: 2, Violations: 1}
	if _, err := Compare(baseline, candidate, false); err == nil ||
		!strings.Contains(err.Error(), "invalid lifecycle violation identifiers") {
		t.Fatalf("Compare() error = %v, want safe identifier rejection", err)
	}
}

func TestCompareRejectsPerQueryContractMismatch(t *testing.T) {
	tests := []struct {
		name      string
		baseline  func() Report
		candidate func() Report
		field     string
	}{
		{
			name:     "fact mode",
			baseline: validV2LifecycleReport,
			candidate: func() Report {
				report := validV2LifecycleReport()
				report.Queries[0].Mode = "hierarchical"
				return report
			},
			field: "mode",
		},
		{
			name:     "target",
			baseline: validV2LifecycleReport,
			candidate: func() Report {
				report := validV2LifecycleReport()
				report.Queries[0].Target = "documents"
				report.Queries[0].Lifecycle = nil
				return report
			},
			field: "target",
		},
		{
			name:     "lifecycle presence",
			baseline: validV2LifecycleReport,
			candidate: func() Report {
				report := validV2LifecycleReport()
				report.Queries[0].Lifecycle = nil
				return report
			},
			field: "lifecycle",
		},
		{
			name:     "intent",
			baseline: validV2LifecycleReport,
			candidate: func() Report {
				report := validV2LifecycleReport()
				report.Queries[0].Lifecycle.Intent = QueryIntentHistory
				return report
			},
			field: "intent",
		},
		{
			name: "as of",
			baseline: func() Report {
				return validAsOfComparisonReport("2025-03-14")
			},
			candidate: func() Report {
				return validAsOfComparisonReport("2025-03-15")
			},
			field: "as_of",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compare(tt.baseline(), tt.candidate(), false)
			want := `query "q" contract field ` + tt.field + ` mismatch`
			if err == nil || err.Error() != want {
				t.Fatalf("Compare() error = %v, want %q", err, want)
			}
		})
	}
}

func TestCompareAcceptsIdenticalValidV2Reports(t *testing.T) {
	report := validV2LifecycleReport()
	if _, err := Compare(report, report, false); err != nil {
		t.Fatalf("Compare() rejected identical valid reports: %v", err)
	}
}

func TestCompareAcceptsPublicV1Baseline(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "evaldata", "public", "v1", "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := DecodeReport(data)
	if err != nil {
		t.Fatalf("DecodeReport(public v1 baseline): %v", err)
	}
	if _, err := Compare(report, report, false); err != nil {
		t.Fatalf("Compare(public v1 baseline): %v", err)
	}
}

func validAsOfComparisonReport(asOf string) Report {
	report := validV2LifecycleReport()
	lifecycleReport := report.Queries[0].Lifecycle
	lifecycleReport.Intent = QueryIntentAsOf
	lifecycleReport.AsOf = asOf
	candidate := &lifecycleReport.Candidates[0]
	candidate.Decision = PresentationInclude
	candidate.ReasonCodes = []LifecycleReasonCode{ReasonCurrentContext}
	return report
}
