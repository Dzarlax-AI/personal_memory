package memory

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func TestDuplicateCandidate(t *testing.T) {
	const (
		low   = 0.60
		dedup = 0.97
		eps   = 0.000001
	)
	expired := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	tests := []struct {
		name        string
		payload     map[string]interface{}
		score       float64
		wantBlocker bool
		wantRelated bool
	}{
		{name: "current below related low", payload: lifecyclePayload(lifecycle.Current), score: low - eps},
		{name: "current at related low", payload: lifecyclePayload(lifecycle.Current), score: low, wantRelated: true},
		{name: "current below dedup", payload: lifecyclePayload(lifecycle.Current), score: dedup - eps, wantRelated: true},
		{name: "current at dedup", payload: lifecyclePayload(lifecycle.Current), score: dedup, wantBlocker: true},
		{name: "current above dedup", payload: lifecyclePayload(lifecycle.Current), score: dedup + eps, wantBlocker: true},
		{name: "legacy current at dedup", payload: map[string]interface{}{}, score: dedup, wantBlocker: true},
		{name: "historical at dedup", payload: lifecyclePayload(lifecycle.Historical), score: dedup, wantBlocker: true},
		{name: "disputed at dedup", payload: lifecyclePayload(lifecycle.Disputed), score: dedup, wantBlocker: true},
		{name: "invalid explicit lifecycle at dedup", payload: map[string]interface{}{"lifecycle_state": "unknown"}, score: dedup, wantBlocker: true},
		{name: "expired current at dedup", payload: map[string]interface{}{"lifecycle_state": "current", "valid_until": expired}, score: dedup, wantBlocker: true},
		{name: "expired current below dedup", payload: map[string]interface{}{"lifecycle_state": "current", "valid_until": expired}, score: dedup - eps},
		{name: "superseded below related low", payload: lifecyclePayload(lifecycle.Superseded), score: low - eps},
		{name: "superseded at related low", payload: lifecyclePayload(lifecycle.Superseded), score: low, wantRelated: true},
		{name: "superseded below dedup", payload: lifecyclePayload(lifecycle.Superseded), score: dedup - eps, wantRelated: true},
		{name: "superseded at dedup", payload: lifecyclePayload(lifecycle.Superseded), score: dedup, wantRelated: true},
		{name: "superseded above dedup", payload: lifecyclePayload(lifecycle.Superseded), score: dedup + eps, wantRelated: true},
		{name: "NaN score", payload: lifecyclePayload(lifecycle.Current), score: math.NaN()},
		{name: "positive infinity score", payload: lifecyclePayload(lifecycle.Current), score: math.Inf(1)},
		{name: "negative infinity score", payload: lifecyclePayload(lifecycle.Current), score: math.Inf(-1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := tt.payload
			payload["text"] = tt.name
			point := qdrant.Point{ID: "candidate", Score: tt.score, Payload: payload}

			duplicate, related := selectRelatedCandidates([]qdrant.Point{point}, low, dedup, 5)
			if got := duplicate != nil; got != tt.wantBlocker {
				t.Fatalf("duplicate present = %v, want %v; duplicate=%#v", got, tt.wantBlocker, duplicate)
			}
			if got := len(related) == 1; got != tt.wantRelated {
				t.Fatalf("related present = %v, want %v; related=%#v", got, tt.wantRelated, related)
			}
			if duplicate != nil && len(related) != 0 {
				t.Fatalf("blocking duplicate was repeated in related candidates: %#v", related)
			}
			if duplicate != nil && duplicate.Score != tt.score {
				t.Fatalf("duplicate score = %v, want raw score %v", duplicate.Score, tt.score)
			}
			if len(related) == 1 && related[0].Score != tt.score {
				t.Fatalf("related score = %v, want raw score %v", related[0].Score, tt.score)
			}
		})
	}
}

func TestDuplicateCandidateSelectsHighestScoreAndPreservesInputOrderForTies(t *testing.T) {
	points := []qdrant.Point{
		{ID: "first", Score: 0.98, Payload: map[string]interface{}{"text": "first"}},
		{ID: "second", Score: 0.99, Payload: map[string]interface{}{"text": "second"}},
		{ID: "third", Score: 0.99, Payload: map[string]interface{}{"text": "third"}},
	}

	duplicate, related := selectRelatedCandidates(points, 0.60, 0.97, 5)
	if duplicate == nil || duplicate.PointID != "second" {
		t.Fatalf("duplicate = %#v, want highest score and first input tie candidate second", duplicate)
	}
	if len(related) != 0 {
		t.Fatalf("blocking candidates must not appear as related: %#v", related)
	}
}

func TestRelatedCandidatesLifecycleRankingFilteringAndLimit(t *testing.T) {
	points := []qdrant.Point{
		{ID: "historical", Score: 0.95, Payload: mergePayload(lifecyclePayload(lifecycle.Historical), map[string]interface{}{"text": "historical"})},
		{ID: "ordinary", Score: 0.96, Payload: mergePayload(lifecyclePayload(lifecycle.Current), map[string]interface{}{"text": "ordinary"})},
		{ID: "canonical", Score: 0.61, Payload: mergePayload(lifecyclePayload(lifecycle.Current), map[string]interface{}{"text": "canonical", "canonical": true})},
		{ID: "below-low", Score: 0.59, Payload: map[string]interface{}{"text": "below"}},
	}

	duplicate, related := selectRelatedCandidates(points, 0.60, 0.97, 2)
	if duplicate != nil {
		t.Fatalf("unexpected duplicate: %#v", duplicate)
	}
	if len(related) != 2 {
		t.Fatalf("related length = %d, want 2; related=%#v", len(related), related)
	}
	gotIDs := []string{related[0].PointID, related[1].PointID}
	if want := []string{"canonical", "ordinary"}; !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("related IDs = %v, want %v", gotIDs, want)
	}
}

func TestRelatedCandidatesProjectionAndJSON(t *testing.T) {
	points := []qdrant.Point{
		{
			ID:    "typed-tags",
			Score: math.Nextafter(0.8, 1),
			Payload: map[string]interface{}{
				"text":        "typed",
				"namespace":   "projects",
				"tags":        []string{"one", "two"},
				"primary_tag": "one",
			},
		},
		{
			ID:    "mixed-tags",
			Score: 0.79,
			Payload: map[string]interface{}{
				"text": "mixed",
				"tags": []interface{}{"kept", 42, nil},
			},
		},
		{ID: "malformed", Score: 0.78, Payload: map[string]interface{}{"text": 17, "namespace": []string{"bad"}, "tags": "bad"}},
	}

	_, related := selectRelatedCandidates(points, 0.60, 0.97, 10)
	if len(related) != 3 {
		t.Fatalf("related length = %d, want 3", len(related))
	}
	if got, want := related[0].Tags, []string{"one", "two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("typed tags = %#v, want %#v", got, want)
	}
	if got, want := related[1].Tags, []string{"kept"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed tags = %#v, want %#v", got, want)
	}
	if related[2].Tags == nil {
		t.Fatal("malformed tags normalized to nil, want non-nil empty slice")
	}
	if related[0].Score != points[0].Score {
		t.Fatalf("score = %v, want raw score %v", related[0].Score, points[0].Score)
	}
	if related[0].Lifecycle.State != lifecycle.Current || !related[0].Lifecycle.Legacy {
		t.Fatalf("lifecycle projection = %#v, want legacy current", related[0].Lifecycle)
	}

	encoded, err := json.Marshal(related[2])
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]interface{}
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"point_id", "text", "score", "namespace", "tags", "lifecycle"} {
		if _, ok := object[key]; !ok {
			t.Errorf("JSON is missing required key %q: %s", key, encoded)
		}
	}
	if _, ok := object["primary_tag"]; ok {
		t.Errorf("empty primary_tag must be omitted: %s", encoded)
	}
	if got, ok := object["tags"].([]interface{}); !ok || len(got) != 0 {
		t.Errorf("tags JSON = %#v, want []", object["tags"])
	}
}

func lifecyclePayload(state lifecycle.State) map[string]interface{} {
	payload := map[string]interface{}{"lifecycle_state": string(state)}
	if state == lifecycle.Superseded {
		payload["superseded_by"] = []interface{}{"replacement"}
	}
	return payload
}

func mergePayload(left, right map[string]interface{}) map[string]interface{} {
	for key, value := range right {
		left[key] = value
	}
	return left
}
