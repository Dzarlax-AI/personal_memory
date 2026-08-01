package eval

import (
	"fmt"
	"sort"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

// LifecycleReasonCode is a stable, non-sensitive explanation for a
// presentation or transition decision.
type LifecycleReasonCode string

const (
	ReasonCurrentTruth        LifecycleReasonCode = "current_truth"
	ReasonCanonicalPreference LifecycleReasonCode = "canonical_preference"
	ReasonCurrentContext      LifecycleReasonCode = "current_context"
	ReasonHistorical          LifecycleReasonCode = "historical"
	ReasonHistoricalContext   LifecycleReasonCode = "historical_context"
	ReasonSuperseded          LifecycleReasonCode = "superseded"
	ReasonSupersededContext   LifecycleReasonCode = "superseded_context"
	ReasonDisputed            LifecycleReasonCode = "disputed"
	ReasonInvalidLifecycle    LifecycleReasonCode = "invalid_lifecycle"
	ReasonExpired             LifecycleReasonCode = "expired"

	ReasonTransitionValid   LifecycleReasonCode = "transition_valid"
	ReasonSourceInvalid     LifecycleReasonCode = "source_invalid"
	ReasonTargetInvalid     LifecycleReasonCode = "target_invalid"
	ReasonTransitionInvalid LifecycleReasonCode = "transition_invalid"
)

// LifecycleCandidateReport contains presentation evidence without fact text.
type LifecycleCandidateReport struct {
	ID          string                `json:"id"`
	State       lifecycle.State       `json:"state,omitempty"`
	Canonical   bool                  `json:"canonical"`
	Expired     bool                  `json:"expired"`
	Decision    PresentationDecision  `json:"decision"`
	ReasonCodes []LifecycleReasonCode `json:"reason_codes"`
	Valid       bool                  `json:"valid"`
}

// QueryLifecycleReport scores declared lifecycle expectations independently
// from semantic relevance metrics. Reason-code expectations use exact matching.
type QueryLifecycleReport struct {
	Intent     QueryIntent                `json:"intent"`
	AsOf       string                     `json:"as_of,omitempty"`
	Candidates []LifecycleCandidateReport `json:"candidates"`
	Checks     int                        `json:"checks"`
	Violations []string                   `json:"violations,omitempty"`
}

// LifecycleAggregateMetrics does not participate in MRR or nDCG aggregation.
type LifecycleAggregateMetrics struct {
	Checks                        int `json:"checks"`
	Violations                    int `json:"violations"`
	CanonicalPreferenceChecks     int `json:"canonical_preference_checks"`
	CanonicalPreferenceViolations int `json:"canonical_preference_violations"`
}

// TransitionReport records read-only validation of one declared transition.
type TransitionReport struct {
	ID         string              `json:"id"`
	Valid      bool                `json:"valid"`
	ReasonCode LifecycleReasonCode `json:"reason_code"`
	Passed     bool                `json:"passed"`
	Violations []string            `json:"violations,omitempty"`
}

// LifecycleReport is the dedicated schema-v2 lifecycle report section.
type LifecycleReport struct {
	Aggregate   LifecycleAggregateMetrics `json:"aggregate"`
	Transitions []TransitionReport        `json:"transitions"`
}

type presentedFacts struct {
	results   []RetrievedItem
	report    QueryLifecycleReport
	canonical LifecycleAggregateMetrics
}

func presentFactCandidates(query Query, points []qdrant.Point, now time.Time) presentedFacts {
	reference := now.UTC()
	if query.EffectiveIntent() == QueryIntentAsOf {
		reference, _ = time.Parse("2006-01-02", query.AsOf)
	}

	type parsedCandidate struct {
		point   qdrant.Point
		view    lifecycle.View
		expired bool
	}
	parsed := make(map[string]parsedCandidate, len(points))
	ordered := make([]lifecycle.Candidate, 0, len(points))
	hasCanonicalCurrent := false
	for _, point := range points {
		view, _ := lifecycle.Parse(point.Payload, point.ID)
		expired := factExpiredAt(point.Payload, reference)
		parsed[point.ID] = parsedCandidate{point: point, view: view, expired: expired}
		ordered = append(ordered, lifecycle.Candidate{PointID: point.ID, Score: point.Score, View: view})
		if view.Valid && !expired && view.State == lifecycle.Current && view.Canonical {
			hasCanonicalCurrent = true
		}
	}
	lifecycle.SortCandidates(ordered)

	output := presentedFacts{report: QueryLifecycleReport{
		Intent: query.EffectiveIntent(),
		AsOf:   query.AsOf,
	}}
	for _, candidate := range ordered {
		value := parsed[candidate.PointID]
		evidence := decidePresentation(query.EffectiveIntent(), value.view, value.expired, hasCanonicalCurrent)
		evidence.ID = candidate.PointID
		output.report.Candidates = append(output.report.Candidates, evidence)
		if value.view.Valid && !value.expired && value.view.State == lifecycle.Current &&
			!value.view.Canonical && hasCanonicalCurrent {
			output.canonical.CanonicalPreferenceChecks++
			if evidence.Decision != PresentationDemote ||
				!equalReasonCodes(evidence.ReasonCodes, []LifecycleReasonCode{ReasonCanonicalPreference}) {
				output.canonical.CanonicalPreferenceViolations++
			}
		}
		if evidence.Decision == PresentationInclude || evidence.Decision == PresentationDemote {
			output.results = append(output.results, itemsFromPoints([]qdrant.Point{value.point})...)
		}
	}
	scoreLifecycleExpectations(query, &output.report)
	output.canonical.Checks = output.report.Checks
	output.canonical.Violations = len(output.report.Violations)
	return output
}

func decidePresentation(intent QueryIntent, view lifecycle.View, expired, hasCanonicalCurrent bool) LifecycleCandidateReport {
	result := LifecycleCandidateReport{
		State: view.State, Canonical: view.Canonical, Expired: expired, Valid: view.Valid,
	}
	if !view.Valid {
		result.State = ""
		result.Decision = PresentationSuppress
		result.ReasonCodes = []LifecycleReasonCode{ReasonInvalidLifecycle}
		return result
	}
	if expired {
		result.Decision = PresentationSuppress
		result.ReasonCodes = []LifecycleReasonCode{ReasonExpired}
		return result
	}

	switch intent {
	case QueryIntentCurrent:
		switch view.State {
		case lifecycle.Current:
			if !view.Canonical && hasCanonicalCurrent {
				result.Decision = PresentationDemote
				result.ReasonCodes = []LifecycleReasonCode{ReasonCanonicalPreference}
			} else {
				result.Decision = PresentationInclude
				result.ReasonCodes = []LifecycleReasonCode{ReasonCurrentTruth}
			}
		case lifecycle.Historical:
			result.Decision = PresentationSuppress
			result.ReasonCodes = []LifecycleReasonCode{ReasonHistorical}
		case lifecycle.Superseded:
			result.Decision = PresentationSuppress
			result.ReasonCodes = []LifecycleReasonCode{ReasonSuperseded}
		case lifecycle.Disputed:
			result.Decision = PresentationSuppress
			result.ReasonCodes = []LifecycleReasonCode{ReasonDisputed}
		}
	case QueryIntentHistory, QueryIntentAsOf:
		historyStyleDecision(&result, view.State)
	case QueryIntentUncertainty:
		uncertaintyDecision(&result, view.State)
	}
	return result
}

func historyStyleDecision(result *LifecycleCandidateReport, state lifecycle.State) {
	switch state {
	case lifecycle.Current:
		result.Decision = PresentationInclude
		result.ReasonCodes = []LifecycleReasonCode{ReasonCurrentContext}
	case lifecycle.Historical:
		result.Decision = PresentationInclude
		result.ReasonCodes = []LifecycleReasonCode{ReasonHistoricalContext}
	case lifecycle.Superseded:
		result.Decision = PresentationInclude
		result.ReasonCodes = []LifecycleReasonCode{ReasonSupersededContext}
	case lifecycle.Disputed:
		result.Decision = PresentationUncertain
		result.ReasonCodes = []LifecycleReasonCode{ReasonDisputed}
	}
}

func uncertaintyDecision(result *LifecycleCandidateReport, state lifecycle.State) {
	switch state {
	case lifecycle.Current:
		result.Decision = PresentationInclude
		result.ReasonCodes = []LifecycleReasonCode{ReasonCurrentContext}
	case lifecycle.Historical:
		result.Decision = PresentationInclude
		result.ReasonCodes = []LifecycleReasonCode{ReasonHistoricalContext}
	case lifecycle.Superseded:
		result.Decision = PresentationInclude
		result.ReasonCodes = []LifecycleReasonCode{ReasonSupersededContext}
	case lifecycle.Disputed:
		result.Decision = PresentationUncertain
		result.ReasonCodes = []LifecycleReasonCode{ReasonDisputed}
	}
}

func scoreLifecycleExpectations(query Query, report *QueryLifecycleReport) {
	byID := make(map[string]LifecycleCandidateReport, len(report.Candidates))
	for _, candidate := range report.Candidates {
		byID[candidate.ID] = candidate
	}
	for _, expectation := range query.LifecycleExpectations {
		report.Checks++
		candidate, exists := byID[expectation.ID]
		if !exists {
			report.Violations = append(report.Violations,
				lifecycleViolation(query.ID, expectation.ID, "candidate_present"))
			continue
		}
		if expectation.State != "" && candidate.State != expectation.State {
			report.Violations = append(report.Violations,
				lifecycleViolation(query.ID, expectation.ID, "state"))
		}
		if candidate.Decision != expectation.Decision {
			report.Violations = append(report.Violations,
				lifecycleViolation(query.ID, expectation.ID, "decision"))
		}
		expectedReasons := make([]LifecycleReasonCode, len(expectation.ReasonCodes))
		for i, reason := range expectation.ReasonCodes {
			expectedReasons[i] = LifecycleReasonCode(reason)
		}
		if !equalReasonCodes(candidate.ReasonCodes, expectedReasons) {
			report.Violations = append(report.Violations,
				lifecycleViolation(query.ID, expectation.ID, "reason_codes"))
		}
	}
	sort.Strings(report.Violations)
}

func lifecycleViolation(queryID, candidateID, invariant string) string {
	return fmt.Sprintf("query %s candidate %s invariant %s", queryID, candidateID, invariant)
}

func equalReasonCodes(left, right []LifecycleReasonCode) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]LifecycleReasonCode(nil), left...)
	rightCopy := append([]LifecycleReasonCode(nil), right...)
	sort.Slice(leftCopy, func(i, j int) bool { return leftCopy[i] < leftCopy[j] })
	sort.Slice(rightCopy, func(i, j int) bool { return rightCopy[i] < rightCopy[j] })
	for i := range leftCopy {
		if leftCopy[i] != rightCopy[i] {
			return false
		}
	}
	return true
}

func executeTransitionScenarios(scenarios []TransitionScenario) []TransitionReport {
	reports := make([]TransitionReport, 0, len(scenarios))
	for _, scenario := range scenarios {
		valid, reason := executeTransitionScenario(scenario)
		report := TransitionReport{
			ID: scenario.ID, Valid: valid, ReasonCode: reason,
			Passed: valid == scenario.ExpectedValid &&
				(scenario.ExpectedReasonCode == "" || reason == LifecycleReasonCode(scenario.ExpectedReasonCode)),
		}
		if valid != scenario.ExpectedValid {
			report.Violations = append(report.Violations,
				fmt.Sprintf("scenario %s invariant valid", scenario.ID))
		}
		if scenario.ExpectedReasonCode != "" && reason != LifecycleReasonCode(scenario.ExpectedReasonCode) {
			report.Violations = append(report.Violations,
				fmt.Sprintf("scenario %s invariant reason_code", scenario.ID))
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].ID < reports[j].ID })
	return reports
}

func executeTransitionScenario(scenario TransitionScenario) (bool, LifecycleReasonCode) {
	pointID := scenario.PointID.String()
	source, err := lifecycle.NormalizeInput(pointID, lifecycleInput(scenario.SourceLifecycle))
	if err != nil {
		return false, ReasonSourceInvalid
	}
	target, err := lifecycle.NormalizeInput(pointID, lifecycleInput(scenario.TargetLifecycle))
	if err != nil {
		return false, ReasonTargetInvalid
	}
	if err := lifecycle.ValidateTransition(pointID, source, target); err != nil {
		return false, ReasonTransitionInvalid
	}
	return true, ReasonTransitionValid
}

func lifecycleInput(payload LifecyclePayload) lifecycle.Input {
	return lifecycle.Input{
		State: payload.State, Canonical: payload.Canonical, Provenance: payload.Provenance,
		VerifiedAt:   payload.VerifiedAt,
		Supersedes:   append([]string(nil), payload.Supersedes...),
		SupersededBy: append([]string(nil), payload.SupersededBy...),
	}
}

func (code LifecycleReasonCode) validPresentation() bool {
	switch code {
	case ReasonCurrentTruth, ReasonCanonicalPreference, ReasonCurrentContext,
		ReasonHistorical, ReasonHistoricalContext, ReasonSuperseded,
		ReasonSupersededContext, ReasonDisputed, ReasonInvalidLifecycle, ReasonExpired:
		return true
	default:
		return false
	}
}

func (code LifecycleReasonCode) validTransition() bool {
	switch code {
	case ReasonTransitionValid, ReasonSourceInvalid, ReasonTargetInvalid, ReasonTransitionInvalid:
		return true
	default:
		return false
	}
}

func safeReasonCode(value LifecycleReasonCode) bool {
	return value.validPresentation() || value.validTransition()
}
