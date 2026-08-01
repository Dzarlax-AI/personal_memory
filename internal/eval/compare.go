package eval

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

type MetricDelta struct {
	MRR    float64         `json:"mrr"`
	HitAt  map[int]float64 `json:"hit_at"`
	NDCGAt map[int]float64 `json:"ndcg_at"`
}

type QueryDelta struct {
	ID                 string      `json:"id"`
	BaselineResultIDs  []string    `json:"baseline_result_ids"`
	CandidateResultIDs []string    `json:"candidate_result_ids"`
	Metrics            MetricDelta `json:"metrics"`
}

type Comparison struct {
	SchemaVersion  int          `json:"schema_version"`
	DatasetVersion string       `json:"dataset_version"`
	Aggregate      MetricDelta  `json:"aggregate"`
	Queries        []QueryDelta `json:"queries"`
	GatesPassed    bool         `json:"gates_passed"`
	GateFailures   []string     `json:"gate_failures,omitempty"`
}

func Compare(baseline, candidate Report, enforceGates bool) (Comparison, error) {
	if baseline.SchemaVersion != candidate.SchemaVersion ||
		baseline.DatasetVersion != candidate.DatasetVersion ||
		baseline.Embedding != candidate.Embedding ||
		!reflect.DeepEqual(baseline.Configuration, candidate.Configuration) {
		return Comparison{}, fmt.Errorf("baseline and candidate identities are incompatible")
	}
	if len(baseline.TopK) != len(candidate.TopK) {
		return Comparison{}, fmt.Errorf("baseline and candidate top_k differ")
	}
	for i := range baseline.TopK {
		if baseline.TopK[i] != candidate.TopK[i] {
			return Comparison{}, fmt.Errorf("baseline and candidate top_k differ")
		}
	}
	comparison := Comparison{
		SchemaVersion:  SchemaVersion,
		DatasetVersion: baseline.DatasetVersion,
		Aggregate:      deltaMetrics(baseline.Aggregate, candidate.Aggregate, baseline.TopK),
		GatesPassed:    !enforceGates || candidate.GatesPassed,
	}
	if enforceGates {
		comparison.GateFailures = append([]string(nil), candidate.GateFailures...)
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

func EvaluateGates(metrics AggregateMetrics, gates Gates) []string {
	var failures []string
	if gates.ForbidInvariantViolations && metrics.InvariantViolations > 0 {
		failures = append(failures, fmt.Sprintf("invariant violations: got %d, want 0", metrics.InvariantViolations))
	}
	if gates.MinimumMRR != nil && metrics.MRR < *gates.MinimumMRR {
		failures = append(failures, fmt.Sprintf("MRR %.6f is below %.6f", metrics.MRR, *gates.MinimumMRR))
	}
	for rawK, minimum := range gates.MinimumHitAt {
		k, _ := strconv.Atoi(rawK)
		if metrics.HitAt[k] < minimum {
			failures = append(failures, fmt.Sprintf("Hit@%d %.6f is below %.6f", k, metrics.HitAt[k], minimum))
		}
	}
	for rawK, minimum := range gates.MinimumNDCGAt {
		k, _ := strconv.Atoi(rawK)
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
