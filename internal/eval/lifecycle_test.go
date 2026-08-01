package eval

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func TestPresentFactCandidatesCurrentIntent(t *testing.T) {
	points := []qdrant.Point{
		lifecyclePoint("ordinary", 0.99, map[string]any{"lifecycle_state": "current", "canonical": false}),
		lifecyclePoint("canonical", 0.70, map[string]any{"lifecycle_state": "current", "canonical": true}),
		lifecyclePoint("old", 1, map[string]any{"lifecycle_state": "historical"}),
		lifecyclePoint("replaced", .98, map[string]any{"lifecycle_state": "superseded", "superseded_by": []any{"canonical"}}),
		lifecyclePoint("argued", .97, map[string]any{"lifecycle_state": "disputed"}),
	}
	query := Query{ID: "q-current", Intent: QueryIntentCurrent}
	got := presentFactCandidates(query, points, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	if ids := resultIDs(got.results); strings.Join(ids, ",") != "canonical,ordinary" {
		t.Fatalf("result IDs = %v, want canonical authority ahead of higher score ordinary", ids)
	}
	assertCandidate(t, got.report, "canonical", lifecycle.Current, PresentationInclude, ReasonCurrentTruth)
	assertCandidate(t, got.report, "ordinary", lifecycle.Current, PresentationDemote, ReasonCanonicalPreference)
	assertCandidate(t, got.report, "old", lifecycle.Historical, PresentationSuppress, ReasonHistorical)
	assertCandidate(t, got.report, "replaced", lifecycle.Superseded, PresentationSuppress, ReasonSuperseded)
	assertCandidate(t, got.report, "argued", lifecycle.Disputed, PresentationSuppress, ReasonDisputed)
	if got.canonical.CanonicalPreferenceChecks != 1 || got.canonical.CanonicalPreferenceViolations != 0 {
		t.Fatalf("canonical metrics = %#v", got.canonical)
	}
}

func TestRequiresBroadLifecycleSearch(t *testing.T) {
	tests := []struct {
		name  string
		query Query
		want  bool
	}{
		{name: "v1 compatible current", query: Query{}, want: false},
		{name: "current include omitted state", query: Query{
			Intent:                QueryIntentCurrent,
			LifecycleExpectations: []LifecycleExpectation{{Decision: PresentationInclude}},
		}, want: false},
		{name: "current include current state", query: Query{
			Intent: QueryIntentCurrent,
			LifecycleExpectations: []LifecycleExpectation{{
				State: lifecycle.Current, Decision: PresentationInclude,
			}},
		}, want: false},
		{name: "current suppress", query: Query{
			Intent:                QueryIntentCurrent,
			LifecycleExpectations: []LifecycleExpectation{{Decision: PresentationSuppress}},
		}, want: true},
		{name: "current demote", query: Query{
			Intent:                QueryIntentCurrent,
			LifecycleExpectations: []LifecycleExpectation{{Decision: PresentationDemote}},
		}, want: true},
		{name: "current uncertain", query: Query{
			Intent:                QueryIntentCurrent,
			LifecycleExpectations: []LifecycleExpectation{{Decision: PresentationUncertain}},
		}, want: true},
		{name: "current non-current state", query: Query{
			Intent: QueryIntentCurrent,
			LifecycleExpectations: []LifecycleExpectation{{
				State: lifecycle.Historical, Decision: PresentationInclude,
			}},
		}, want: true},
		{name: "history", query: Query{Intent: QueryIntentHistory}, want: true},
		{name: "as of", query: Query{Intent: QueryIntentAsOf}, want: true},
		{name: "uncertainty", query: Query{Intent: QueryIntentUncertainty}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requiresBroadLifecycleSearch(tt.query); got != tt.want {
				t.Fatalf("requiresBroadLifecycleSearch() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestPresentFactCandidatesCurrentWithoutCanonicalIncludesOrdinary(t *testing.T) {
	got := presentFactCandidates(
		Query{ID: "q", Intent: QueryIntentCurrent},
		[]qdrant.Point{lifecyclePoint("ordinary", .8, map[string]any{})},
		time.Now(),
	)
	assertCandidate(t, got.report, "ordinary", lifecycle.Current, PresentationInclude, ReasonCurrentTruth)
}

func TestPresentFactCandidatesHistoryAndUncertainty(t *testing.T) {
	points := []qdrant.Point{
		lifecyclePoint("current", .9, map[string]any{"lifecycle_state": "current"}),
		lifecyclePoint("historical", .8, map[string]any{"lifecycle_state": "historical"}),
		lifecyclePoint("superseded", .7, map[string]any{"lifecycle_state": "superseded", "superseded_by": []any{"current"}}),
		lifecyclePoint("disputed-high", 1, map[string]any{"lifecycle_state": "disputed"}),
		lifecyclePoint("disputed-low", .6, map[string]any{"lifecycle_state": "disputed"}),
	}
	for _, intent := range []QueryIntent{QueryIntentHistory, QueryIntentUncertainty} {
		t.Run(string(intent), func(t *testing.T) {
			got := presentFactCandidates(Query{ID: "q", Intent: intent}, points, time.Now())
			assertCandidate(t, got.report, "current", lifecycle.Current, PresentationInclude, ReasonCurrentContext)
			assertCandidate(t, got.report, "historical", lifecycle.Historical, PresentationInclude, ReasonHistoricalContext)
			assertCandidate(t, got.report, "superseded", lifecycle.Superseded, PresentationInclude, ReasonSupersededContext)
			assertCandidate(t, got.report, "disputed-high", lifecycle.Disputed, PresentationUncertain, ReasonDisputed)
			assertCandidate(t, got.report, "disputed-low", lifecycle.Disputed, PresentationUncertain, ReasonDisputed)
			for _, id := range resultIDs(got.results) {
				if strings.HasPrefix(id, "disputed") {
					t.Fatalf("uncertain candidate %q entered relevance results", id)
				}
			}
		})
	}
}

func TestPresentFactCandidatesExpiredInvalidAndAsOf(t *testing.T) {
	points := []qdrant.Point{
		lifecyclePoint("expired-canonical", 1, map[string]any{
			"lifecycle_state": "current", "canonical": true,
			"valid_until": "2025-03-14", "permanent": true,
		}),
		lifecyclePoint("valid-on-date", .9, map[string]any{
			"lifecycle_state": "historical", "valid_until": "2025-03-15",
		}),
		lifecyclePoint("invalid-secret", .8, map[string]any{
			"lifecycle_state": "future", "text": "DO-NOT-LEAK",
		}),
	}
	query := Query{ID: "q-as-of", Intent: QueryIntentAsOf, AsOf: "2025-03-15"}
	got := presentFactCandidates(query, points, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	expired := candidateByID(t, got.report, "expired-canonical")
	if !expired.Expired || expired.Decision != PresentationSuppress ||
		!equalReasonCodes(expired.ReasonCodes, []LifecycleReasonCode{ReasonExpired}) {
		t.Fatalf("expired candidate = %#v", expired)
	}
	assertCandidate(t, got.report, "valid-on-date", lifecycle.Historical, PresentationInclude, ReasonHistoricalContext)
	invalid := candidateByID(t, got.report, "invalid-secret")
	if invalid.Valid || invalid.State != "" || invalid.Decision != PresentationSuppress ||
		!equalReasonCodes(invalid.ReasonCodes, []LifecycleReasonCode{ReasonInvalidLifecycle}) {
		t.Fatalf("invalid candidate = %#v", invalid)
	}
	encoded, err := json.Marshal(got.report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "DO-NOT-LEAK") {
		t.Fatal("lifecycle failure leaked fact text")
	}
}

func TestLifecycleExpectationScoringUsesExactReasonCodesAndSafeIDs(t *testing.T) {
	query := Query{
		ID: "query-7", Intent: QueryIntentCurrent,
		LifecycleExpectations: []LifecycleExpectation{{
			ID: "42", State: lifecycle.Current, Decision: PresentationInclude,
			ReasonCodes: []string{"canonical_preference"},
		}},
	}
	report := QueryLifecycleReport{Candidates: []LifecycleCandidateReport{{
		ID: "42", State: lifecycle.Current, Decision: PresentationInclude,
		ReasonCodes: []LifecycleReasonCode{ReasonCurrentTruth}, Valid: true,
	}}}
	scoreLifecycleExpectations(query, &report)
	if report.Checks != 1 || len(report.Violations) != 1 ||
		report.Violations[0] != (LifecycleViolation{
			Scope: ViolationScopeQuery, QueryID: "query-7",
			CandidateID: "42", Invariant: InvariantReasonCodes,
		}) {
		t.Fatalf("scoring = %#v", report)
	}
}

func TestExecuteTransitionScenariosCoversEveryTargetAndIdempotence(t *testing.T) {
	states := []lifecycle.State{lifecycle.Current, lifecycle.Historical, lifecycle.Superseded, lifecycle.Disputed}
	var scenarios []TransitionScenario
	for _, state := range states {
		target := validLifecyclePayload(state)
		scenarios = append(scenarios,
			transitionScenario("to-"+string(state), lifecycle.Current, target, true, ReasonTransitionValid),
			transitionScenario("idempotent-"+string(state), state, target, true, ReasonTransitionValid),
		)
	}
	reports := executeTransitionScenarios(scenarios)
	if len(reports) != len(scenarios) {
		t.Fatalf("transition report count = %d, want %d", len(reports), len(scenarios))
	}
	for _, report := range reports {
		if !report.Valid || !report.Passed || report.ReasonCode != ReasonTransitionValid {
			t.Fatalf("transition %q = %#v", report.ID, report)
		}
	}
}

func TestExecuteTransitionScenarioReturnsSafeInvalidReason(t *testing.T) {
	target := validLifecyclePayload(lifecycle.Historical)
	target.Canonical = true
	scenario := transitionScenario("invalid-target", lifecycle.Current, target, false, ReasonTargetInvalid)
	report := executeTransitionScenarios([]TransitionScenario{scenario})[0]
	if report.Valid || !report.Passed || report.ReasonCode != ReasonTargetInvalid ||
		!safeReasonCode(report.ReasonCode) {
		t.Fatalf("transition = %#v", report)
	}
}

func lifecyclePoint(id string, score float64, payload map[string]any) qdrant.Point {
	if _, exists := payload["text"]; !exists {
		payload["text"] = "private " + id
	}
	return qdrant.Point{ID: id, Score: score, Payload: payload}
}

func assertCandidate(
	t *testing.T,
	report QueryLifecycleReport,
	id string,
	state lifecycle.State,
	decision PresentationDecision,
	reason LifecycleReasonCode,
) {
	t.Helper()
	got := candidateByID(t, report, id)
	if got.State != state || got.Decision != decision ||
		!equalReasonCodes(got.ReasonCodes, []LifecycleReasonCode{reason}) {
		t.Fatalf("candidate %q = %#v, want state=%q decision=%q reason=%q", id, got, state, decision, reason)
	}
}

func candidateByID(t *testing.T, report QueryLifecycleReport, id string) LifecycleCandidateReport {
	t.Helper()
	for _, candidate := range report.Candidates {
		if candidate.ID == id {
			return candidate
		}
	}
	t.Fatalf("candidate %q not found", id)
	return LifecycleCandidateReport{}
}

func validLifecyclePayload(state lifecycle.State) LifecyclePayload {
	payload := LifecyclePayload{
		State: state, Supersedes: []string{}, SupersededBy: []string{},
		present: map[string]bool{
			"state": true, "canonical": true, "supersedes": true, "superseded_by": true,
		},
	}
	if state == lifecycle.Superseded {
		payload.SupersededBy = []string{"2"}
	}
	return payload
}

func transitionScenario(
	id string,
	sourceState lifecycle.State,
	target LifecyclePayload,
	expectedValid bool,
	reason LifecycleReasonCode,
) TransitionScenario {
	return TransitionScenario{
		ID: id, PointID: PointID{value: "1", numeric: true},
		SourceLifecycle: validLifecyclePayload(sourceState),
		TargetLifecycle: target, ExpectedValid: expectedValid,
		ExpectedReasonCode: string(reason),
	}
}
