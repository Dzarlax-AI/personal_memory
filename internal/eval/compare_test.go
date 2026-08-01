package eval

import "testing"

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
