package eval

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

// ComparisonEpsilon absorbs insignificant floating-point representation noise
// while keeping evaluation gates conservative.
const ComparisonEpsilon = 1e-12

// MetricDelta stores candidate-minus-baseline ranking metric changes.
type MetricDelta struct {
	MRR    float64         `json:"mrr"`
	HitAt  map[int]float64 `json:"hit_at"`
	NDCGAt map[int]float64 `json:"ndcg_at"`
}

// QueryDelta describes result and metric changes for one query.
type QueryDelta struct {
	ID                 string      `json:"id"`
	BaselineResultIDs  []string    `json:"baseline_result_ids"`
	CandidateResultIDs []string    `json:"candidate_result_ids"`
	Metrics            MetricDelta `json:"metrics"`
}

// Comparison is the deterministic baseline/candidate comparison output.
type Comparison struct {
	SchemaVersion          int                  `json:"schema_version"`
	DatasetVersion         string               `json:"dataset_version"`
	BaselineEmbedding      *EmbeddingIdentity   `json:"baseline_embedding,omitempty"`
	CandidateEmbedding     *EmbeddingIdentity   `json:"candidate_embedding,omitempty"`
	BaselineConfiguration  *Configuration       `json:"baseline_configuration,omitempty"`
	CandidateConfiguration *Configuration       `json:"candidate_configuration,omitempty"`
	BaselineMode           string               `json:"baseline_mode,omitempty"`
	CandidateMode          string               `json:"candidate_mode,omitempty"`
	BaselineDiagnostics    *Diagnostics         `json:"baseline_diagnostics,omitempty"`
	CandidateDiagnostics   *Diagnostics         `json:"candidate_diagnostics,omitempty"`
	Aggregate              MetricDelta          `json:"aggregate"`
	Cohorts                []CohortComparison   `json:"cohorts,omitempty"`
	Queries                []QueryDelta         `json:"queries"`
	Lifecycle              *LifecycleComparison `json:"lifecycle,omitempty"`
	GatesPassed            bool                 `json:"gates_passed"`
	GateFailures           []string             `json:"gate_failures,omitempty"`
}

// CohortComparison keeps protected and exploratory cohort results auditable.
type CohortComparison struct {
	Cohort    QueryCohort            `json:"cohort"`
	Baseline  CohortAggregateMetrics `json:"baseline"`
	Candidate CohortAggregateMetrics `json:"candidate"`
	Metrics   MetricDelta            `json:"metrics"`
}

// LifecycleComparison keeps lifecycle regressions visible without blending
// them into ranking deltas.
type LifecycleComparison struct {
	BaselineAggregate   LifecycleAggregateMetrics `json:"baseline_aggregate"`
	CandidateAggregate  LifecycleAggregateMetrics `json:"candidate_aggregate"`
	BaselineViolations  []LifecycleViolation      `json:"baseline_violations,omitempty"`
	CandidateViolations []LifecycleViolation      `json:"candidate_violations,omitempty"`
}

// Compare validates report compatibility and computes candidate deltas.
func Compare(baseline, candidate Report, enforceGates bool) (Comparison, error) {
	if baseline.SchemaVersion != candidate.SchemaVersion ||
		baseline.DatasetVersion != candidate.DatasetVersion {
		return Comparison{}, fmt.Errorf("baseline and candidate identities are incompatible")
	}
	if baseline.SchemaVersion == CurrentReportSchemaVersion {
		if baseline.Mode != candidate.Mode {
			return Comparison{}, fmt.Errorf("baseline and candidate modes are incompatible")
		}
		if !baseEmbeddingIdentityEqual(baseline.Embedding, candidate.Embedding) ||
			!baseConfigurationEqual(baseline.Configuration, candidate.Configuration) {
			return Comparison{}, fmt.Errorf("baseline and candidate identities are incompatible")
		}
	} else if !strictEmbeddingIdentityEqual(baseline.Embedding, candidate.Embedding) ||
		!strictConfigurationEqual(baseline.Configuration, candidate.Configuration) {
		return Comparison{}, fmt.Errorf("baseline and candidate identities are incompatible")
	}
	if err := validateMatchedQueryContracts(baseline, candidate); err != nil {
		return Comparison{}, err
	}
	if err := validateReportQueryContracts(baseline); err != nil {
		return Comparison{}, fmt.Errorf("baseline report query contract is invalid: %w", err)
	}
	if err := validateReportQueryContracts(candidate); err != nil {
		return Comparison{}, fmt.Errorf("candidate report query contract is invalid: %w", err)
	}
	if baseline.SchemaVersion >= LifecycleSchemaVersion {
		if baseline.Lifecycle == nil || candidate.Lifecycle == nil {
			return Comparison{}, fmt.Errorf("schema_version %d reports require lifecycle sections", baseline.SchemaVersion)
		}
		if err := validateLifecycleReport(baseline); err != nil {
			return Comparison{}, fmt.Errorf("baseline lifecycle report is invalid: %w", err)
		}
		if err := validateLifecycleReport(candidate); err != nil {
			return Comparison{}, fmt.Errorf("candidate lifecycle report is invalid: %w", err)
		}
	}
	if len(baseline.TopK) != len(candidate.TopK) {
		return Comparison{}, fmt.Errorf("baseline and candidate top_k differ")
	}
	for i := range baseline.TopK {
		if baseline.TopK[i] != candidate.TopK[i] {
			return Comparison{}, fmt.Errorf("baseline and candidate top_k differ")
		}
	}
	if baseline.SchemaVersion == CurrentReportSchemaVersion {
		if err := validateV3Report(baseline); err != nil {
			return Comparison{}, fmt.Errorf("baseline v3 report is invalid: %w", err)
		}
		if err := validateV3Report(candidate); err != nil {
			return Comparison{}, fmt.Errorf("candidate v3 report is invalid: %w", err)
		}
		baseline = recomputeV3Ranking(baseline)
		candidate = recomputeV3Ranking(candidate)
	}
	comparison := Comparison{
		SchemaVersion:  baseline.SchemaVersion,
		DatasetVersion: baseline.DatasetVersion,
		Aggregate:      deltaMetrics(baseline.Aggregate, candidate.Aggregate, baseline.TopK),
		GatesPassed:    !enforceGates || candidate.GatesPassed,
	}
	if baseline.SchemaVersion == CurrentReportSchemaVersion {
		baselineEmbedding := baseline.Embedding
		candidateEmbedding := candidate.Embedding
		baselineConfiguration := cloneConfiguration(baseline.Configuration)
		candidateConfiguration := cloneConfiguration(candidate.Configuration)
		comparison.BaselineEmbedding = &baselineEmbedding
		comparison.CandidateEmbedding = &candidateEmbedding
		comparison.BaselineConfiguration = &baselineConfiguration
		comparison.CandidateConfiguration = &candidateConfiguration
		comparison.BaselineMode = baseline.Mode
		comparison.CandidateMode = candidate.Mode
		comparison.BaselineDiagnostics = cloneDiagnostics(baseline.Diagnostics)
		comparison.CandidateDiagnostics = cloneDiagnostics(candidate.Diagnostics)
		comparison.Cohorts = compareCohorts(baseline, candidate)
	}
	if baseline.SchemaVersion >= LifecycleSchemaVersion {
		comparison.Lifecycle = &LifecycleComparison{
			BaselineAggregate:   baseline.Lifecycle.Aggregate,
			CandidateAggregate:  candidate.Lifecycle.Aggregate,
			BaselineViolations:  reportLifecycleViolations(baseline),
			CandidateViolations: reportLifecycleViolations(candidate),
		}
	}
	if enforceGates {
		if baseline.SchemaVersion == CurrentReportSchemaVersion {
			if !candidate.GatesPassed || len(candidate.GateFailures) != 0 {
				comparison.GateFailures = append(comparison.GateFailures, "candidate dataset gates failed")
			}
			comparison.GateFailures = append(comparison.GateFailures,
				evaluateV3ComparisonGates(baseline, candidate)...)
			comparison.GateFailures = uniqueSorted(comparison.GateFailures)
			comparison.GatesPassed = len(comparison.GateFailures) == 0
		} else {
			comparison.GateFailures = append([]string(nil), candidate.GateFailures...)
		}
		sort.Strings(comparison.GateFailures)
	}
	baseQueries := make(map[string]QueryReport, len(baseline.Queries))
	for _, query := range baseline.Queries {
		baseQueries[query.ID] = query
	}
	for _, candidateQuery := range candidate.Queries {
		baselineQuery, exists := baseQueries[candidateQuery.ID]
		if !exists {
			return Comparison{}, fmt.Errorf("candidate query %q is absent from baseline", candidateQuery.ID)
		}
		comparison.Queries = append(comparison.Queries, QueryDelta{
			ID:                 candidateQuery.ID,
			BaselineResultIDs:  resultIDs(baselineQuery.Results),
			CandidateResultIDs: resultIDs(candidateQuery.Results),
			Metrics:            deltaQueryMetrics(baselineQuery.Metrics, candidateQuery.Metrics, baseline.TopK),
		})
		delete(baseQueries, candidateQuery.ID)
	}
	if len(baseQueries) > 0 {
		return Comparison{}, fmt.Errorf("candidate report is missing %d baseline queries", len(baseQueries))
	}
	sort.Slice(comparison.Queries, func(i, j int) bool { return comparison.Queries[i].ID < comparison.Queries[j].ID })
	return comparison, nil
}

func cloneDiagnostics(source *Diagnostics) *Diagnostics {
	if source == nil {
		return nil
	}
	cloned := *source
	if source.Corpus != nil {
		corpus := *source.Corpus
		cloned.Corpus = &corpus
	}
	return &cloned
}

func recomputeV3Ranking(report Report) Report {
	queryMetrics := make([]QueryMetrics, len(report.Queries))
	for i := range report.Queries {
		queryMetrics[i] = report.Queries[i].Metrics
	}
	report.Aggregate = Aggregate(queryMetrics, report.TopK)
	report.Cohorts = AggregateCohorts(report.Queries, report.TopK)
	return report
}

func validateMatchedQueryContracts(baseline, candidate Report) error {
	baselineQueries := make(map[string]QueryReport, len(baseline.Queries))
	for _, query := range baseline.Queries {
		if _, duplicate := baselineQueries[query.ID]; duplicate {
			return fmt.Errorf("baseline contains duplicate query ID %q", query.ID)
		}
		baselineQueries[query.ID] = query
	}
	for _, candidateQuery := range candidate.Queries {
		baselineQuery, exists := baselineQueries[candidateQuery.ID]
		if !exists {
			return fmt.Errorf("candidate query %q is absent from baseline", candidateQuery.ID)
		}
		if baselineQuery.Target != candidateQuery.Target {
			return queryContractMismatch(candidateQuery.ID, "target")
		}
		if baselineQuery.Mode != candidateQuery.Mode {
			return queryContractMismatch(candidateQuery.ID, "mode")
		}
		if (baselineQuery.Lifecycle == nil) != (candidateQuery.Lifecycle == nil) {
			return queryContractMismatch(candidateQuery.ID, "lifecycle")
		}
		if baseline.SchemaVersion >= LifecycleSchemaVersion && baselineQuery.Lifecycle != nil {
			if baselineQuery.Lifecycle.Intent != candidateQuery.Lifecycle.Intent {
				return queryContractMismatch(candidateQuery.ID, "intent")
			}
			if baselineQuery.Lifecycle.AsOf != candidateQuery.Lifecycle.AsOf {
				return queryContractMismatch(candidateQuery.ID, "as_of")
			}
		}
		if baseline.SchemaVersion == CurrentReportSchemaVersion &&
			!reflect.DeepEqual(baselineQuery.Cohorts, candidateQuery.Cohorts) {
			return queryContractMismatch(candidateQuery.ID, "cohorts")
		}
		delete(baselineQueries, candidateQuery.ID)
	}
	if len(baselineQueries) != 0 {
		return fmt.Errorf("candidate report is missing %d baseline queries", len(baselineQueries))
	}
	return nil
}

func baseEmbeddingIdentityEqual(left, right EmbeddingIdentity) bool {
	return left.Provider == right.Provider &&
		left.ModelID == right.ModelID &&
		left.ModelRevision == right.ModelRevision &&
		left.DType == right.DType &&
		left.Pooling == right.Pooling &&
		left.VectorSize == right.VectorSize
}

func strictEmbeddingIdentityEqual(left, right EmbeddingIdentity) bool {
	return baseEmbeddingIdentityEqual(left, right) &&
		left.InputProfile == right.InputProfile
}

func baseConfigurationEqual(left, right Configuration) bool {
	return left.FactCollection == right.FactCollection &&
		left.ChunkCollection == right.ChunkCollection &&
		left.FolderCollection == right.FolderCollection &&
		left.FolderTopK == right.FolderTopK &&
		left.FolderThreshold == right.FolderThreshold
}

func strictConfigurationEqual(left, right Configuration) bool {
	return left.Name == right.Name &&
		baseConfigurationEqual(left, right) &&
		equalInts(left.TopK, right.TopK) &&
		left.RetrievalStrategy == right.RetrievalStrategy &&
		left.DenseCandidateLimit == right.DenseCandidateLimit &&
		left.RRFConstant == right.RRFConstant
}

func cloneConfiguration(source Configuration) Configuration {
	cloned := source
	cloned.TopK = append([]int(nil), source.TopK...)
	cloned.present = clonePresence(source.present)
	return cloned
}

func queryContractMismatch(queryID, field string) error {
	return fmt.Errorf("query %q contract field %s mismatch", queryID, field)
}

func reportLifecycleViolations(report Report) []LifecycleViolation {
	var violations []LifecycleViolation
	for _, query := range report.Queries {
		if query.Lifecycle != nil {
			violations = append(violations, query.Lifecycle.Violations...)
		}
	}
	if report.Lifecycle != nil {
		for _, transition := range report.Lifecycle.Transitions {
			violations = append(violations, transition.Violations...)
		}
	}
	sortLifecycleViolations(violations)
	return violations
}

// EvaluateGates returns deterministic descriptions of explicit gate failures.
func EvaluateGates(metrics AggregateMetrics, gates Gates) []string {
	var failures []string
	if gates.ForbidInvariantViolations && metrics.InvariantViolations > 0 {
		failures = append(failures, fmt.Sprintf("invariant violations: got %d, want 0", metrics.InvariantViolations))
	}
	if gates.MinimumMRR != nil && metrics.MRR < *gates.MinimumMRR {
		failures = append(failures, fmt.Sprintf("MRR %.6f is below %.6f", metrics.MRR, *gates.MinimumMRR))
	}
	for rawK, minimum := range gates.MinimumHitAt {
		k, err := strconv.Atoi(rawK)
		if err != nil {
			failures = append(failures, fmt.Sprintf("minimum_hit_at key %q is not an integer", rawK))
			continue
		}
		if metrics.HitAt[k] < minimum {
			failures = append(failures, fmt.Sprintf("Hit@%d %.6f is below %.6f", k, metrics.HitAt[k], minimum))
		}
	}
	for rawK, minimum := range gates.MinimumNDCGAt {
		k, err := strconv.Atoi(rawK)
		if err != nil {
			failures = append(failures, fmt.Sprintf("minimum_ndcg_at key %q is not an integer", rawK))
			continue
		}
		if metrics.NDCGAt[k] < minimum {
			failures = append(failures, fmt.Sprintf("nDCG@%d %.6f is below %.6f", k, metrics.NDCGAt[k], minimum))
		}
	}
	sort.Strings(failures)
	return failures
}

func deltaMetrics(baseline, candidate AggregateMetrics, topK []int) MetricDelta {
	delta := MetricDelta{MRR: candidate.MRR - baseline.MRR, HitAt: map[int]float64{}, NDCGAt: map[int]float64{}}
	for _, k := range topK {
		delta.HitAt[k] = candidate.HitAt[k] - baseline.HitAt[k]
		delta.NDCGAt[k] = candidate.NDCGAt[k] - baseline.NDCGAt[k]
	}
	return delta
}

func deltaQueryMetrics(baseline, candidate QueryMetrics, topK []int) MetricDelta {
	return deltaMetrics(
		AggregateMetrics{HitAt: baseline.HitAt, MRR: baseline.MRR, NDCGAt: baseline.NDCGAt},
		AggregateMetrics{HitAt: candidate.HitAt, MRR: candidate.MRR, NDCGAt: candidate.NDCGAt},
		topK,
	)
}

func resultIDs(results []RetrievedItem) []string {
	ids := make([]string, len(results))
	for i, result := range results {
		ids[i] = result.ID
	}
	return ids
}
