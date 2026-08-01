package eval

import (
	"math"
	"testing"
)

func TestScoreQuery(t *testing.T) {
	query := Query{
		ID:           "q1",
		Expected:     []ExpectedItem{{ID: "a", Grade: 3}, {ID: "b", Grade: 1}},
		ForbiddenIDs: []string{"x"},
	}
	got := ScoreQuery(query, []RetrievedItem{{ID: "c"}, {ID: "b"}, {ID: "a"}, {ID: "x"}}, []int{1, 3})
	if got.HitAt[1] != 0 || got.HitAt[3] != 1 {
		t.Fatalf("HitAt = %#v", got.HitAt)
	}
	if got.MRR != 0.5 {
		t.Fatalf("MRR = %v, want 0.5", got.MRR)
	}
	wantNDCG := (1/math.Log2(3) + 7/math.Log2(4)) / (7 + 1/math.Log2(3))
	if math.Abs(got.NDCGAt[3]-wantNDCG) > 1e-12 {
		t.Fatalf("nDCG@3 = %v, want %v", got.NDCGAt[3], wantNDCG)
	}
	if len(got.InvariantViolations) != 1 || got.InvariantViolations[0] != "x" {
		t.Fatalf("violations = %#v", got.InvariantViolations)
	}
}

func TestScoreQueryEmptyResults(t *testing.T) {
	got := ScoreQuery(Query{Expected: []ExpectedItem{{ID: "a", Grade: 2}}}, nil, []int{1})
	if got.HitAt[1] != 0 || got.MRR != 0 || got.NDCGAt[1] != 0 {
		t.Fatalf("score = %#v", got)
	}
}

func TestScoreQueryDoesNotCreditZeroGradeInMRR(t *testing.T) {
	got := ScoreQuery(
		Query{Expected: []ExpectedItem{{ID: "irrelevant", Grade: 0}, {ID: "relevant", Grade: 3}}},
		[]RetrievedItem{{ID: "irrelevant"}, {ID: "relevant"}},
		[]int{1, 2},
	)
	if got.MRR != 0.5 {
		t.Fatalf("MRR = %v, want 0.5", got.MRR)
	}
}

func TestAggregateMetrics(t *testing.T) {
	got := Aggregate([]QueryMetrics{
		{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 0.5}, MRR: 1},
		{HitAt: map[int]float64{1: 0}, NDCGAt: map[int]float64{1: 0}, MRR: 0.5, InvariantViolations: []string{"x"}},
	}, []int{1})
	if got.HitAt[1] != 0.5 || got.NDCGAt[1] != 0.25 || got.MRR != 0.75 || got.InvariantViolations != 1 {
		t.Fatalf("aggregate = %#v", got)
	}
}
