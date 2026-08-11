package memory

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func TestParseLifecycleRecallOptions(t *testing.T) {
	tests := []struct {
		name   string
		args   map[string]interface{}
		mode   RecallLifecycleMode
		asOf   string
		errSub string
	}{
		{name: "omitted defaults current", args: map[string]interface{}{}, mode: RecallLifecycleCurrent},
		{name: "explicit current", args: map[string]interface{}{"lifecycle_mode": "current"}, mode: RecallLifecycleCurrent},
		{name: "history", args: map[string]interface{}{"lifecycle_mode": "history"}, mode: RecallLifecycleHistory},
		{name: "include all", args: map[string]interface{}{"lifecycle_mode": "include_all"}, mode: RecallLifecycleIncludeAll},
		{name: "as of", args: map[string]interface{}{"lifecycle_mode": "as_of", "as_of": "2025-03-14"}, mode: RecallLifecycleAsOf, asOf: "2025-03-14"},
		{name: "unknown mode", args: map[string]interface{}{"lifecycle_mode": "future"}, errSub: "lifecycle_mode"},
		{name: "empty explicit mode", args: map[string]interface{}{"lifecycle_mode": ""}, errSub: "lifecycle_mode"},
		{name: "non string mode", args: map[string]interface{}{"lifecycle_mode": 1}, errSub: "lifecycle_mode must be a string"},
		{name: "as of missing date", args: map[string]interface{}{"lifecycle_mode": "as_of"}, errSub: "as_of is required"},
		{name: "malformed date", args: map[string]interface{}{"lifecycle_mode": "as_of", "as_of": "14-03-2025"}, errSub: "YYYY-MM-DD"},
		{name: "non exact date", args: map[string]interface{}{"lifecycle_mode": "as_of", "as_of": "2025-3-14"}, errSub: "YYYY-MM-DD"},
		{name: "impossible date", args: map[string]interface{}{"lifecycle_mode": "as_of", "as_of": "2025-02-29"}, errSub: "YYYY-MM-DD"},
		{name: "non string date", args: map[string]interface{}{"lifecycle_mode": "as_of", "as_of": nil}, errSub: "as_of must be a string"},
		{name: "current forbids date", args: map[string]interface{}{"lifecycle_mode": "current", "as_of": "2025-03-14"}, errSub: "only valid"},
		{name: "history forbids empty date", args: map[string]interface{}{"lifecycle_mode": "history", "as_of": ""}, errSub: "only valid"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseLifecycleRecallOptions(test.args)
			if test.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), test.errSub) {
					t.Fatalf("error = %v, want substring %q", err, test.errSub)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Mode != test.mode || got.AsOf != test.asOf {
				t.Fatalf("options = %#v, want mode=%q as_of=%q", got, test.mode, test.asOf)
			}
		})
	}
}

func TestPresentLifecycleRecallCandidatesVisibilityAndEvidence(t *testing.T) {
	points := []qdrant.Point{
		{ID: "historical", Score: .99, Payload: map[string]interface{}{"lifecycle_state": "historical"}},
		{ID: "superseded", Score: .98, Payload: map[string]interface{}{"lifecycle_state": "superseded", "superseded_by": []interface{}{"current"}}},
		{ID: "disputed", Score: .97, Payload: map[string]interface{}{"lifecycle_state": "disputed"}},
		{ID: "ordinary", Score: .96, Payload: map[string]interface{}{"lifecycle_state": "current"}},
		{ID: "canonical", Score: .70, Payload: map[string]interface{}{"lifecycle_state": "current", "canonical": true}},
		{ID: "legacy", Score: .60, Payload: map[string]interface{}{}},
		{ID: "malformed", Score: 1, Payload: map[string]interface{}{"lifecycle_state": "current", "canonical": "yes"}},
		{ID: "expired", Score: 1, Payload: map[string]interface{}{"lifecycle_state": "current", "valid_until": "2020-01-01"}},
	}
	now := time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		mode RecallLifecycleMode
		asOf string
		want []struct {
			id       string
			decision LifecyclePresentationDecision
			reason   LifecycleReasonCode
			semantic int
		}
	}{
		{mode: RecallLifecycleCurrent, want: []struct {
			id       string
			decision LifecyclePresentationDecision
			reason   LifecycleReasonCode
			semantic int
		}{
			{"canonical", LifecycleDecisionInclude, LifecycleReasonCurrentTruth, 5},
			{"ordinary", LifecycleDecisionDemote, LifecycleReasonCanonicalPreference, 4},
			{"legacy", LifecycleDecisionDemote, LifecycleReasonCanonicalPreference, 6},
		}},
		{mode: RecallLifecycleHistory, want: []struct {
			id       string
			decision LifecyclePresentationDecision
			reason   LifecycleReasonCode
			semantic int
		}{
			{"canonical", LifecycleDecisionInclude, LifecycleReasonCurrentContext, 5},
			{"ordinary", LifecycleDecisionInclude, LifecycleReasonCurrentContext, 4},
			{"legacy", LifecycleDecisionInclude, LifecycleReasonCurrentContext, 6},
			{"disputed", LifecycleDecisionUncertain, LifecycleReasonDisputed, 3},
			{"historical", LifecycleDecisionInclude, LifecycleReasonHistoricalContext, 1},
			{"superseded", LifecycleDecisionInclude, LifecycleReasonSupersededContext, 2},
		}},
		{mode: RecallLifecycleAsOf, asOf: "2026-01-02", want: []struct {
			id       string
			decision LifecyclePresentationDecision
			reason   LifecycleReasonCode
			semantic int
		}{
			{"canonical", LifecycleDecisionInclude, LifecycleReasonCurrentContext, 5},
			{"ordinary", LifecycleDecisionInclude, LifecycleReasonCurrentContext, 4},
			{"legacy", LifecycleDecisionInclude, LifecycleReasonCurrentContext, 6},
			{"disputed", LifecycleDecisionUncertain, LifecycleReasonDisputed, 3},
			{"historical", LifecycleDecisionInclude, LifecycleReasonHistoricalContext, 1},
			{"superseded", LifecycleDecisionInclude, LifecycleReasonSupersededContext, 2},
		}},
		{mode: RecallLifecycleIncludeAll, want: []struct {
			id       string
			decision LifecyclePresentationDecision
			reason   LifecycleReasonCode
			semantic int
		}{
			{"canonical", LifecycleDecisionInclude, LifecycleReasonCurrentContext, 5},
			{"ordinary", LifecycleDecisionInclude, LifecycleReasonCurrentContext, 4},
			{"legacy", LifecycleDecisionInclude, LifecycleReasonCurrentContext, 6},
			{"disputed", LifecycleDecisionUncertain, LifecycleReasonDisputed, 3},
			{"historical", LifecycleDecisionInclude, LifecycleReasonHistoricalContext, 1},
			{"superseded", LifecycleDecisionInclude, LifecycleReasonSupersededContext, 2},
		}},
	}

	for _, test := range tests {
		t.Run(string(test.mode), func(t *testing.T) {
			got := presentLifecycleRecallCandidates(points, LifecycleRecallOptions{Mode: test.mode, AsOf: test.asOf}, now)
			if len(got) != len(test.want) {
				t.Fatalf("got %d candidates, want %d: %#v", len(got), len(test.want), got)
			}
			for index, want := range test.want {
				candidate := got[index]
				if candidate.point.ID != want.id || candidate.Decision != want.decision || !reflect.DeepEqual(candidate.ReasonCodes, []LifecycleReasonCode{want.reason}) || candidate.SemanticRank != want.semantic || candidate.FinalRank != index+1 {
					t.Errorf("candidate %d = {id:%q decision:%q reasons:%q semantic:%d final:%d}, want %#v", index, candidate.point.ID, candidate.Decision, candidate.ReasonCodes, candidate.SemanticRank, candidate.FinalRank, want)
				}
				if candidate.point.Score != points[want.semantic-1].Score {
					t.Errorf("candidate %q score changed: %v", candidate.point.ID, candidate.point.Score)
				}
			}
		})
	}
}

func TestPresentLifecycleRecallCandidatesUsesExistingDeterministicTieBreak(t *testing.T) {
	points := []qdrant.Point{
		{ID: "z", Score: .8, Payload: map[string]interface{}{"lifecycle_state": "current"}},
		{ID: "a", Score: .8, Payload: map[string]interface{}{"lifecycle_state": "current"}},
	}
	got := presentLifecycleRecallCandidates(points, LifecycleRecallOptions{}, time.Now())
	if len(got) != 2 || got[0].point.ID != "a" || got[1].point.ID != "z" {
		t.Fatalf("tie order = %#v, want point ID order a, z", got)
	}
	if got[0].SemanticRank != 2 || got[0].FinalRank != 1 || got[1].SemanticRank != 1 || got[1].FinalRank != 2 {
		t.Fatalf("ranks do not preserve semantic order and final order: %#v", got)
	}
}

func TestPresentLifecycleRecallCandidatesKeepsFirstDuplicatePointID(t *testing.T) {
	points := []qdrant.Point{
		{ID: "duplicate", Score: .91, Payload: map[string]interface{}{"lifecycle_state": "historical", "text": "first"}},
		{ID: "other", Score: .90, Payload: map[string]interface{}{"lifecycle_state": "current"}},
		{ID: "duplicate", Score: .99, Payload: map[string]interface{}{"lifecycle_state": "current", "canonical": true, "text": "conflicting second"}},
	}
	got := presentLifecycleRecallCandidates(points, LifecycleRecallOptions{Mode: RecallLifecycleHistory}, time.Now())
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want one candidate per point ID: %#v", len(got), got)
	}
	for _, candidate := range got {
		if candidate.point.ID != "duplicate" {
			continue
		}
		if candidate.point.Score != .91 || candidate.point.Payload["text"] != "first" || candidate.SemanticRank != 1 {
			t.Fatalf("duplicate policy did not preserve first Qdrant hit: %#v", candidate)
		}
		return
	}
	t.Fatal("deduplicated candidate was not returned")
}

func TestPresentLifecycleRecallCandidatesAsOfUsesDateOnlyForExpiry(t *testing.T) {
	points := []qdrant.Point{
		{ID: "same-date", Score: .9, Payload: map[string]interface{}{"lifecycle_state": "historical", "valid_until": "2025-03-14"}},
		{ID: "before-date", Score: .8, Payload: map[string]interface{}{"lifecycle_state": "current", "valid_until": "2025-03-13"}},
		{ID: "state-not-inferred", Score: .7, Payload: map[string]interface{}{"lifecycle_state": "superseded", "superseded_by": []interface{}{"replacement"}}},
	}
	got := presentLifecycleRecallCandidates(points, LifecycleRecallOptions{Mode: RecallLifecycleAsOf, AsOf: "2025-03-14"}, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	ids := make([]string, 0, len(got))
	for _, candidate := range got {
		ids = append(ids, candidate.point.ID)
	}
	if want := []string{"same-date", "state-not-inferred"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func TestFactExpiredAtComparesUTCCalendarDates(t *testing.T) {
	payload := map[string]interface{}{"valid_until": "2025-03-14"}
	tests := []struct {
		name      string
		reference time.Time
		expired   bool
	}{
		{name: "same day midday UTC", reference: time.Date(2025, 3, 14, 12, 0, 0, 0, time.UTC)},
		{name: "next local day but same UTC date", reference: time.Date(2025, 3, 15, 0, 30, 0, 0, time.FixedZone("UTC+2", 2*60*60))},
		{name: "previous local day but next UTC date", reference: time.Date(2025, 3, 14, 23, 30, 0, 0, time.FixedZone("UTC-2", -2*60*60)), expired: true},
		{name: "next day UTC", reference: time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC), expired: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := factExpiredAt(payload, test.reference); got != test.expired {
				t.Fatalf("factExpiredAt() = %v, want %v for %s", got, test.expired, test.reference)
			}
		})
	}
}

func TestLifecycleRecallFiltersPreserveBaseAndScopeCurrentOnly(t *testing.T) {
	base := map[string]interface{}{"must": []map[string]interface{}{{"key": "namespace", "match": map[string]interface{}{"value": "projects"}}}}
	current := lifecycleRecallFilters(base, RecallLifecycleCurrent)
	if _, ok := current["should"]; !ok || !reflect.DeepEqual(current["must"], base["must"]) {
		t.Fatalf("current filters = %#v", current)
	}
	for _, mode := range []RecallLifecycleMode{RecallLifecycleHistory, RecallLifecycleAsOf, RecallLifecycleIncludeAll} {
		got := lifecycleRecallFilters(base, mode)
		if !reflect.DeepEqual(got, base) {
			t.Errorf("mode %q filters = %#v, want base %#v", mode, got, base)
		}
		got["new"] = true
		if _, mutated := base["new"]; mutated {
			t.Errorf("mode %q returned mutable base map", mode)
		}
		gotMust := got["must"].([]map[string]interface{})
		gotMust[0]["key"] = "changed"
		gotMust[0]["match"].(map[string]interface{})["value"] = "changed"
		baseMust := base["must"].([]map[string]interface{})
		if baseMust[0]["key"] != "namespace" || baseMust[0]["match"].(map[string]interface{})["value"] != "projects" {
			t.Errorf("mode %q returned aliased nested filters: %#v", mode, base)
		}
	}
}

func TestLifecycleRecallCacheIdentitySeparatesModesAndDates(t *testing.T) {
	identities := []string{
		lifecycleRecallCacheIdentity(LifecycleRecallOptions{Mode: RecallLifecycleCurrent}),
		lifecycleRecallCacheIdentity(LifecycleRecallOptions{Mode: RecallLifecycleHistory}),
		lifecycleRecallCacheIdentity(LifecycleRecallOptions{Mode: RecallLifecycleIncludeAll}),
		lifecycleRecallCacheIdentity(LifecycleRecallOptions{Mode: RecallLifecycleAsOf, AsOf: "2025-03-14"}),
		lifecycleRecallCacheIdentity(LifecycleRecallOptions{Mode: RecallLifecycleAsOf, AsOf: "2025-03-15"}),
	}
	seen := map[string]bool{}
	for _, identity := range identities {
		if seen[identity] {
			t.Fatalf("duplicate cache identity %q in %v", identity, identities)
		}
		seen[identity] = true
		if !strings.Contains(identity, "lifecycle-recall-v2") {
			t.Errorf("unversioned identity %q", identity)
		}
	}
	if got := lifecycleRecallCacheIdentity(LifecycleRecallOptions{}); got != identities[0] {
		t.Fatalf("zero options identity = %q, want default current %q", got, identities[0])
	}
}
