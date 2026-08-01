package eval

import (
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
		Queries:       []QueryReport{{ID: "q", Results: []RetrievedItem{{ID: "a"}}, Metrics: QueryMetrics{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 1}, MRR: 1}}},
		GatesPassed:   true,
	}
	candidate := baseline
	candidate.Aggregate = AggregateMetrics{HitAt: map[int]float64{1: 0}, NDCGAt: map[int]float64{1: 0}, MRR: 0.5}
	candidate.Queries = []QueryReport{{ID: "q", Results: []RetrievedItem{{ID: "b"}, {ID: "a"}}, Metrics: QueryMetrics{HitAt: map[int]float64{1: 0}, NDCGAt: map[int]float64{1: 0}, MRR: 0.5}}}
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
		SchemaVersion: CurrentReportSchemaVersion, DatasetVersion: "2",
		TopK: []int{1}, Configuration: Configuration{Name: "cfg"},
		Aggregate: AggregateMetrics{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 1}},
		Queries: []QueryReport{{
			ID: "q", Target: "facts", Metrics: QueryMetrics{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 1}},
			Lifecycle: &QueryLifecycleReport{Intent: QueryIntentCurrent, Checks: 1},
		}},
		Lifecycle:   &LifecycleReport{Aggregate: LifecycleAggregateMetrics{Checks: 1}},
		GatesPassed: true,
	}
	candidate := base
	candidate.Queries = []QueryReport{{
		ID: "q", Target: "facts", Metrics: QueryMetrics{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 1}},
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
	if comparison.SchemaVersion != CurrentReportSchemaVersion || comparison.Lifecycle == nil ||
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
	candidate.Lifecycle.Aggregate = LifecycleAggregateMetrics{Checks: 1, Violations: 1}
	if _, err := Compare(baseline, candidate, false); err == nil ||
		!strings.Contains(err.Error(), "invalid lifecycle violation identifiers") {
		t.Fatalf("Compare() error = %v, want safe identifier rejection", err)
	}
}
