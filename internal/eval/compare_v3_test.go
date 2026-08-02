package eval

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func validV3ComparisonReport() Report {
	report := Report{
		SchemaVersion:  CurrentReportSchemaVersion,
		DatasetVersion: "3",
		Mode:           "fixture",
		Embedding: EmbeddingIdentity{
			Provider: "tei", ModelID: "intfloat/multilingual-e5-small",
			ModelRevision: "revision", DType: "float32", Pooling: "mean",
			VectorSize: 3, InputProfile: LegacyRawV1,
		},
		Configuration: Configuration{
			Name: "vector", FactCollection: "memory", ChunkCollection: "doc_chunks",
			FolderCollection: "doc_folders", FolderTopK: 3, FolderThreshold: 0.5,
			TopK: []int{1}, RetrievalStrategy: RetrievalVectorOnly,
			DenseCandidateLimit: 0, RRFConstant: 0,
		},
		TopK: []int{1},
		Queries: []QueryReport{
			{
				ID: "exact", Target: "facts", Mode: "flat",
				Cohorts:   []QueryCohort{CohortExactName},
				Metrics:   QueryMetrics{MRR: 0.5, HitAt: map[int]float64{1: 0}, NDCGAt: map[int]float64{1: 0}},
				Lifecycle: &QueryLifecycleReport{Intent: QueryIntentCurrent},
			},
			{
				ID: "path", Target: "documents", Mode: "flat",
				Cohorts: []QueryCohort{CohortIdentifierPath},
				Metrics: QueryMetrics{MRR: 0.5, HitAt: map[int]float64{1: 0}, NDCGAt: map[int]float64{1: 0}},
			},
		},
		Lifecycle:   &LifecycleReport{Aggregate: LifecycleAggregateMetrics{}},
		GatesPassed: true,
	}
	report.Aggregate = Aggregate([]QueryMetrics{report.Queries[0].Metrics, report.Queries[1].Metrics}, report.TopK)
	report.Cohorts = AggregateCohorts(report.Queries, report.TopK)
	return report
}

func TestCompareV3AllowsVisibleExperimentDimensionsAndSnapshotsThem(t *testing.T) {
	baseline := validV3ComparisonReport()
	candidate := validV3ComparisonReport()
	candidate.Embedding.InputProfile = MultilingualE5V1
	candidate.Configuration.Name = "hybrid"
	candidate.Configuration.RetrievalStrategy = RetrievalHybridRRF
	candidate.Configuration.DenseCandidateLimit = 20
	candidate.Configuration.RRFConstant = 60

	comparison, err := Compare(baseline, candidate, false)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.BaselineEmbedding == nil || comparison.CandidateEmbedding == nil ||
		comparison.BaselineConfiguration == nil || comparison.CandidateConfiguration == nil {
		t.Fatalf("comparison identity snapshots missing: %#v", comparison)
	}
	if comparison.BaselineEmbedding.InputProfile != LegacyRawV1 ||
		comparison.CandidateConfiguration.RetrievalStrategy != RetrievalHybridRRF {
		t.Fatalf("comparison snapshots = %#v", comparison)
	}
}

func TestCompareV3RejectsCompatibilityDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Report)
		want   string
	}{
		{"schema", func(report *Report) { report.SchemaVersion = LifecycleSchemaVersion }, "identities"},
		{"dataset", func(report *Report) { report.DatasetVersion = "other" }, "identities"},
		{"model", func(report *Report) { report.Embedding.ModelRevision = "other" }, "identities"},
		{"vector size", func(report *Report) { report.Embedding.VectorSize++ }, "identities"},
		{"fact collection", func(report *Report) { report.Configuration.FactCollection = "other" }, "identities"},
		{"folder top k", func(report *Report) { report.Configuration.FolderTopK++ }, "identities"},
		{"top k", func(report *Report) { report.TopK = []int{1, 3} }, "top_k"},
		{"query target", func(report *Report) { report.Queries[0].Target = "documents"; report.Queries[0].Lifecycle = nil }, "target"},
		{"cohort drift", func(report *Report) { report.Queries[0].Cohorts = []QueryCohort{CohortGeneralSemantic} }, "cohorts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseline := validV3ComparisonReport()
			candidate := validV3ComparisonReport()
			tt.mutate(&candidate)
			if _, err := Compare(baseline, candidate, false); err == nil ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Compare() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestCompareV3ConservativeGates(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Report, *Report)
		wantPass bool
		wants    []string
	}{
		{
			name: "protected cohort strict improvement",
			mutate: func(_ *Report, candidate *Report) {
				candidate.Queries[0].Metrics.MRR = 0.5 + ComparisonEpsilon*2
			},
			wantPass: true,
		},
		{
			name: "epsilon equality is not improvement",
			mutate: func(_ *Report, candidate *Report) {
				candidate.Queries[0].Metrics.MRR = 0.5 + ComparisonEpsilon
			},
			wants: []string{"protected cohorts require a ranking improvement"},
		},
		{
			name: "aggregate ranking regression",
			mutate: func(_ *Report, candidate *Report) {
				candidate.Queries[0].Metrics.MRR = 0.4
			},
			wants: []string{"aggregate MRR regressed", "cohort exact-name MRR regressed"},
		},
		{
			name: "protected hit and nDCG regression",
			mutate: func(baseline, candidate *Report) {
				baseline.Queries[1].Metrics.HitAt[1] = 1
				baseline.Queries[1].Metrics.NDCGAt[1] = 1
				candidate.Queries[0].Metrics.MRR = 0.6
			},
			wants: []string{
				"cohort identifier-path Hit@1 regressed",
				"cohort identifier-path nDCG@1 regressed",
			},
		},
		{
			name: "ranking invariant must be zero",
			mutate: func(_ *Report, candidate *Report) {
				candidate.Queries[0].Metrics.InvariantViolations = []string{"1"}
			},
			wants: []string{
				"aggregate invariant violations must be zero",
				"aggregate invariant violations regressed",
				"cohort exact-name invariant violations regressed",
			},
		},
		{
			name: "lifecycle violations must be zero",
			mutate: func(_ *Report, candidate *Report) {
				candidate.Queries[0].Lifecycle.Checks = 1
				candidate.Queries[0].Lifecycle.Violations = []LifecycleViolation{{
					Scope: ViolationScopeQuery, QueryID: "exact",
					CandidateID: "1", Invariant: InvariantCandidatePresent,
				}}
				candidate.Lifecycle.Aggregate = LifecycleAggregateMetrics{Checks: 1, Violations: 1}
			},
			wants: []string{
				"lifecycle violations must be zero",
				"lifecycle violations regressed",
			},
		},
		{
			name: "missing protected cohort",
			mutate: func(baseline, candidate *Report) {
				baseline.Queries = baseline.Queries[:1]
				candidate.Queries = candidate.Queries[:1]
			},
			wants: []string{"cohort identifier-path is required"},
		},
		{
			name: "candidate dataset gates remain required",
			mutate: func(_ *Report, candidate *Report) {
				candidate.Queries[0].Metrics.MRR = 0.6
				candidate.GatesPassed = false
				candidate.GateFailures = []string{"dataset minimum_mrr failed"}
			},
			wants: []string{"candidate dataset gates failed"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseline := validV3ComparisonReport()
			candidate := validV3ComparisonReport()
			tt.mutate(&baseline, &candidate)
			recomputeV3Metrics(&baseline)
			recomputeV3Metrics(&candidate)
			comparison, err := Compare(baseline, candidate, true)
			if err != nil {
				t.Fatal(err)
			}
			if comparison.GatesPassed != tt.wantPass {
				t.Fatalf("GatesPassed = %t, failures = %#v", comparison.GatesPassed, comparison.GateFailures)
			}
			for _, want := range tt.wants {
				if !containsString(comparison.GateFailures, want) {
					t.Fatalf("failures = %#v, want %q", comparison.GateFailures, want)
				}
			}
			if !reflect.DeepEqual(comparison.GateFailures, sortedCopy(comparison.GateFailures)) {
				t.Fatalf("gate failures are not sorted: %#v", comparison.GateFailures)
			}
			for _, failure := range comparison.GateFailures {
				if strings.Contains(failure, "query text") {
					t.Fatalf("failure leaks query text: %q", failure)
				}
			}
		})
	}
}

func recomputeV3Metrics(report *Report) {
	metrics := make([]QueryMetrics, len(report.Queries))
	for i := range report.Queries {
		metrics[i] = report.Queries[i].Metrics
	}
	report.Aggregate = Aggregate(metrics, report.TopK)
	report.Cohorts = AggregateCohorts(report.Queries, report.TopK)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortedCopy(values []string) []string {
	cloned := append([]string(nil), values...)
	sort.Strings(cloned)
	return cloned
}
