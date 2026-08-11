package lifecycle

// PresentationIntent selects current-truth or broad lifecycle presentation.
type PresentationIntent string

const (
	PresentationIntentCurrent PresentationIntent = "current"
	PresentationIntentBroad   PresentationIntent = "broad"
)

// PresentationDecision is a closed, privacy-safe presentation action.
type PresentationDecision string

const (
	PresentationInclude   PresentationDecision = "include"
	PresentationSuppress  PresentationDecision = "suppress"
	PresentationDemote    PresentationDecision = "demote"
	PresentationUncertain PresentationDecision = "uncertain"
)

// PresentationReasonCode is a closed, privacy-safe explanation for a
// presentation decision.
type PresentationReasonCode string

const (
	PresentationReasonCurrentTruth        PresentationReasonCode = "current_truth"
	PresentationReasonCanonicalPreference PresentationReasonCode = "canonical_preference"
	PresentationReasonCurrentContext      PresentationReasonCode = "current_context"
	PresentationReasonHistorical          PresentationReasonCode = "historical"
	PresentationReasonHistoricalContext   PresentationReasonCode = "historical_context"
	PresentationReasonSuperseded          PresentationReasonCode = "superseded"
	PresentationReasonSupersededContext   PresentationReasonCode = "superseded_context"
	PresentationReasonDisputed            PresentationReasonCode = "disputed"
	PresentationReasonInvalidLifecycle    PresentationReasonCode = "invalid_lifecycle"
	PresentationReasonExpired             PresentationReasonCode = "expired"
)

// Presentation records a lifecycle decision and its stable explanations.
type Presentation struct {
	Decision    PresentationDecision
	ReasonCodes []PresentationReasonCode
}

// DecidePresentation applies the lifecycle presentation policy without
// inspecting fact text, semantic scores, or query text.
func DecidePresentation(intent PresentationIntent, view View, expired, hasCanonicalCurrent bool) Presentation {
	if !view.Valid {
		return presentation(PresentationSuppress, PresentationReasonInvalidLifecycle)
	}
	if expired {
		return presentation(PresentationSuppress, PresentationReasonExpired)
	}

	if intent == PresentationIntentCurrent {
		switch view.State {
		case Current:
			if !view.Canonical && hasCanonicalCurrent {
				return presentation(PresentationDemote, PresentationReasonCanonicalPreference)
			}
			return presentation(PresentationInclude, PresentationReasonCurrentTruth)
		case Historical:
			return presentation(PresentationSuppress, PresentationReasonHistorical)
		case Superseded:
			return presentation(PresentationSuppress, PresentationReasonSuperseded)
		case Disputed:
			return presentation(PresentationSuppress, PresentationReasonDisputed)
		}
	}

	if intent == PresentationIntentBroad {
		switch view.State {
		case Current:
			return presentation(PresentationInclude, PresentationReasonCurrentContext)
		case Historical:
			return presentation(PresentationInclude, PresentationReasonHistoricalContext)
		case Superseded:
			return presentation(PresentationInclude, PresentationReasonSupersededContext)
		case Disputed:
			return presentation(PresentationUncertain, PresentationReasonDisputed)
		}
	}

	return Presentation{}
}

func presentation(decision PresentationDecision, reason PresentationReasonCode) Presentation {
	return Presentation{Decision: decision, ReasonCodes: []PresentationReasonCode{reason}}
}
