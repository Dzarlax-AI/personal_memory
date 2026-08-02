package eval

import (
	"fmt"
	"sort"
)

func compareCohorts(baseline, candidate Report) []CohortComparison {
	candidateByCohort := make(map[QueryCohort]CohortAggregateMetrics, len(candidate.Cohorts))
	for _, cohort := range candidate.Cohorts {
		candidateByCohort[cohort.Cohort] = cohort
	}
	result := make([]CohortComparison, 0, len(baseline.Cohorts))
	for _, baselineCohort := range baseline.Cohorts {
		candidateCohort, exists := candidateByCohort[baselineCohort.Cohort]
		if !exists {
			continue
		}
		result = append(result, CohortComparison{
			Cohort:    baselineCohort.Cohort,
			Baseline:  baselineCohort,
			Candidate: candidateCohort,
			Metrics: deltaMetrics(
				cohortAsAggregate(baselineCohort),
				cohortAsAggregate(candidateCohort),
				baseline.TopK,
			),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Cohort < result[j].Cohort })
	return result
}

func cohortAsAggregate(cohort CohortAggregateMetrics) AggregateMetrics {
	return AggregateMetrics{
		HitAt: cohort.HitAt, MRR: cohort.MRR, NDCGAt: cohort.NDCGAt,
		InvariantViolations: cohort.InvariantViolations,
	}
}

func evaluateV3ComparisonGates(baseline, candidate Report) []string {
	var failures []string
	if candidate.Aggregate.InvariantViolations != 0 {
		failures = append(failures, "aggregate invariant violations must be zero")
	}
	if candidate.Aggregate.InvariantViolations > baseline.Aggregate.InvariantViolations {
		failures = append(failures, "aggregate invariant violations regressed")
	}
	failures = append(failures, rankingRegressionFailures(
		"aggregate", baseline.Aggregate, candidate.Aggregate, baseline.TopK,
	)...)

	baselineLifecycle := baseline.Lifecycle.Aggregate.Violations
	candidateLifecycle := candidate.Lifecycle.Aggregate.Violations
	if candidateLifecycle != 0 {
		failures = append(failures, "lifecycle violations must be zero")
	}
	if candidateLifecycle > baselineLifecycle {
		failures = append(failures, "lifecycle violations regressed")
	}

	baselineCohorts := cohortMap(baseline.Cohorts)
	candidateCohorts := cohortMap(candidate.Cohorts)
	improved := false
	for _, protected := range []QueryCohort{CohortExactName, CohortIdentifierPath} {
		baselineCohort, baselineExists := baselineCohorts[protected]
		candidateCohort, candidateExists := candidateCohorts[protected]
		if !baselineExists || !candidateExists ||
			baselineCohort.QueryCount == 0 || candidateCohort.QueryCount == 0 {
			failures = append(failures, fmt.Sprintf("cohort %s is required", protected))
			continue
		}
		failures = append(failures, rankingRegressionFailures(
			"cohort "+string(protected),
			cohortAsAggregate(baselineCohort),
			cohortAsAggregate(candidateCohort),
			baseline.TopK,
		)...)
		if candidateCohort.InvariantViolations > baselineCohort.InvariantViolations {
			failures = append(failures,
				fmt.Sprintf("cohort %s invariant violations regressed", protected))
		}
		if rankingImproved(baselineCohort, candidateCohort, baseline.TopK) {
			improved = true
		}
	}
	if !improved {
		failures = append(failures, "protected cohorts require a ranking improvement")
	}
	return uniqueSorted(failures)
}

func rankingRegressionFailures(
	scope string,
	baseline AggregateMetrics,
	candidate AggregateMetrics,
	topK []int,
) []string {
	var failures []string
	if candidate.MRR < baseline.MRR-ComparisonEpsilon {
		failures = append(failures, scope+" MRR regressed")
	}
	for _, k := range topK {
		if candidate.HitAt[k] < baseline.HitAt[k]-ComparisonEpsilon {
			failures = append(failures, fmt.Sprintf("%s Hit@%d regressed", scope, k))
		}
		if candidate.NDCGAt[k] < baseline.NDCGAt[k]-ComparisonEpsilon {
			failures = append(failures, fmt.Sprintf("%s nDCG@%d regressed", scope, k))
		}
	}
	return failures
}

func rankingImproved(
	baseline CohortAggregateMetrics,
	candidate CohortAggregateMetrics,
	topK []int,
) bool {
	if candidate.MRR > baseline.MRR+ComparisonEpsilon {
		return true
	}
	for _, k := range topK {
		if candidate.HitAt[k] > baseline.HitAt[k]+ComparisonEpsilon ||
			candidate.NDCGAt[k] > baseline.NDCGAt[k]+ComparisonEpsilon {
			return true
		}
	}
	return false
}

func cohortMap(cohorts []CohortAggregateMetrics) map[QueryCohort]CohortAggregateMetrics {
	result := make(map[QueryCohort]CohortAggregateMetrics, len(cohorts))
	for _, cohort := range cohorts {
		result[cohort.Cohort] = cohort
	}
	return result
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
