package eval

import (
	"fmt"
	"math"
	"sort"
)

// AggregateCohorts derives ranking aggregates from query reports. A query in
// multiple cohorts contributes once to each cohort; lifecycle presentation
// metrics remain in the separate lifecycle report.
func AggregateCohorts(queries []QueryReport, topK []int) []CohortAggregateMetrics {
	grouped := make(map[QueryCohort][]QueryMetrics)
	for _, query := range queries {
		for _, cohort := range query.Cohorts {
			grouped[cohort] = append(grouped[cohort], query.Metrics)
		}
	}
	cohorts := make([]QueryCohort, 0, len(grouped))
	for cohort := range grouped {
		cohorts = append(cohorts, cohort)
	}
	sort.Slice(cohorts, func(i, j int) bool { return cohorts[i] < cohorts[j] })

	result := make([]CohortAggregateMetrics, 0, len(cohorts))
	for _, cohort := range cohorts {
		metrics := Aggregate(grouped[cohort], topK)
		result = append(result, CohortAggregateMetrics{
			Cohort:              cohort,
			QueryCount:          len(grouped[cohort]),
			HitAt:               metrics.HitAt,
			MRR:                 metrics.MRR,
			NDCGAt:              metrics.NDCGAt,
			InvariantViolations: metrics.InvariantViolations,
		})
	}
	return result
}

func validateCohortAggregates(report Report) error {
	if report.Cohorts == nil {
		return fmt.Errorf("cohorts must be an array")
	}
	expected := AggregateCohorts(report.Queries, report.TopK)
	if len(report.Cohorts) != len(expected) {
		return fmt.Errorf("cohort aggregates do not match query cohorts")
	}
	for i := range expected {
		got := report.Cohorts[i]
		want := expected[i]
		if got.present != nil {
			for _, field := range []string{
				"cohort", "query_count", "hit_at", "mrr", "ndcg_at", "invariant_violations",
			} {
				if !got.present[field] {
					return fmt.Errorf("cohort aggregate field %s is required", field)
				}
			}
		}
		if got.Cohort != want.Cohort || got.QueryCount != want.QueryCount ||
			got.QueryCount < 1 || got.InvariantViolations != want.InvariantViolations ||
			!metricClose(got.MRR, want.MRR) ||
			!metricMapClose(got.HitAt, want.HitAt, report.TopK) ||
			!metricMapClose(got.NDCGAt, want.NDCGAt, report.TopK) {
			return fmt.Errorf("cohort aggregates do not match query reports")
		}
	}
	return nil
}

func metricMapClose(got, want map[int]float64, topK []int) bool {
	if got == nil || want == nil || len(got) != len(topK) || len(want) != len(topK) {
		return false
	}
	for _, k := range topK {
		gotValue, gotExists := got[k]
		wantValue, wantExists := want[k]
		if !gotExists || !wantExists || !metricClose(gotValue, wantValue) {
			return false
		}
	}
	return true
}

func metricClose(left, right float64) bool {
	return !math.IsNaN(left) && !math.IsInf(left, 0) &&
		!math.IsNaN(right) && !math.IsInf(right, 0) &&
		math.Abs(left-right) <= ComparisonEpsilon
}
