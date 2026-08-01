package eval

import (
	"math"
	"sort"
)

// ScoreQuery computes ranking and invariant metrics for one query.
func ScoreQuery(query Query, results []RetrievedItem, topK []int) QueryMetrics {
	grades := make(map[string]int, len(query.Expected))
	idealGrades := make([]int, 0, len(query.Expected))
	for _, expected := range query.Expected {
		grades[expected.ID] = expected.Grade
		idealGrades = append(idealGrades, expected.Grade)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(idealGrades)))
	forbidden := make(map[string]struct{}, len(query.ForbiddenIDs))
	for _, id := range query.ForbiddenIDs {
		forbidden[id] = struct{}{}
	}

	metrics := QueryMetrics{
		HitAt:  make(map[int]float64, len(topK)),
		NDCGAt: make(map[int]float64, len(topK)),
	}
	firstRelevantRank := 0
	for i, result := range results {
		if grade, ok := grades[result.ID]; ok && grade > 0 && firstRelevantRank == 0 {
			firstRelevantRank = i + 1
		}
		if _, blocked := forbidden[result.ID]; blocked {
			metrics.InvariantViolations = append(metrics.InvariantViolations, result.ID)
		}
		if result.MissingText {
			metrics.MissingTextResultIDs = append(metrics.MissingTextResultIDs, result.ID)
		}
	}
	if firstRelevantRank > 0 {
		metrics.MRR = 1 / float64(firstRelevantRank)
	}
	for _, k := range topK {
		limit := min(k, len(results))
		dcg := 0.0
		hit := 0.0
		for i := 0; i < limit; i++ {
			grade := grades[results[i].ID]
			if grade > 0 {
				hit = 1
				dcg += (math.Pow(2, float64(grade)) - 1) / math.Log2(float64(i)+2)
			}
		}
		idealLimit := min(k, len(idealGrades))
		idcg := 0.0
		for i := 0; i < idealLimit; i++ {
			idcg += (math.Pow(2, float64(idealGrades[i])) - 1) / math.Log2(float64(i)+2)
		}
		metrics.HitAt[k] = hit
		if idcg > 0 {
			metrics.NDCGAt[k] = dcg / idcg
		} else {
			metrics.NDCGAt[k] = 0
		}
	}
	sort.Strings(metrics.InvariantViolations)
	sort.Strings(metrics.MissingTextResultIDs)
	return metrics
}

// Aggregate averages query metrics and totals invariant violations.
func Aggregate(queries []QueryMetrics, topK []int) AggregateMetrics {
	result := AggregateMetrics{
		HitAt:  make(map[int]float64, len(topK)),
		NDCGAt: make(map[int]float64, len(topK)),
	}
	if len(queries) == 0 {
		return result
	}
	for _, metrics := range queries {
		result.MRR += metrics.MRR
		result.InvariantViolations += len(metrics.InvariantViolations)
		for _, k := range topK {
			result.HitAt[k] += metrics.HitAt[k]
			result.NDCGAt[k] += metrics.NDCGAt[k]
		}
	}
	count := float64(len(queries))
	result.MRR /= count
	for _, k := range topK {
		result.HitAt[k] /= count
		result.NDCGAt[k] /= count
	}
	return result
}
