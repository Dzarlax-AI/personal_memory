package lifecycle

import (
	"reflect"
	"testing"
)

func TestDecidePresentation(t *testing.T) {
	tests := []struct {
		name                string
		intent              PresentationIntent
		view                View
		expired             bool
		hasCanonicalCurrent bool
		decision            PresentationDecision
		reason              PresentationReasonCode
	}{
		{name: "current canonical", intent: PresentationIntentCurrent, view: View{State: Current, Canonical: true, Valid: true}, hasCanonicalCurrent: true, decision: PresentationInclude, reason: PresentationReasonCurrentTruth},
		{name: "current ordinary without canonical candidate", intent: PresentationIntentCurrent, view: View{State: Current, Valid: true}, decision: PresentationInclude, reason: PresentationReasonCurrentTruth},
		{name: "current ordinary with canonical candidate", intent: PresentationIntentCurrent, view: View{State: Current, Valid: true}, hasCanonicalCurrent: true, decision: PresentationDemote, reason: PresentationReasonCanonicalPreference},
		{name: "current historical", intent: PresentationIntentCurrent, view: View{State: Historical, Valid: true}, decision: PresentationSuppress, reason: PresentationReasonHistorical},
		{name: "current superseded", intent: PresentationIntentCurrent, view: View{State: Superseded, Valid: true}, decision: PresentationSuppress, reason: PresentationReasonSuperseded},
		{name: "current disputed", intent: PresentationIntentCurrent, view: View{State: Disputed, Valid: true}, decision: PresentationSuppress, reason: PresentationReasonDisputed},
		{name: "current invalid", intent: PresentationIntentCurrent, view: View{State: Current}, decision: PresentationSuppress, reason: PresentationReasonInvalidLifecycle},
		{name: "current expired", intent: PresentationIntentCurrent, view: View{State: Current, Valid: true}, expired: true, decision: PresentationSuppress, reason: PresentationReasonExpired},
		{name: "broad canonical current", intent: PresentationIntentBroad, view: View{State: Current, Canonical: true, Valid: true}, hasCanonicalCurrent: true, decision: PresentationInclude, reason: PresentationReasonCurrentContext},
		{name: "broad ordinary current", intent: PresentationIntentBroad, view: View{State: Current, Valid: true}, hasCanonicalCurrent: true, decision: PresentationInclude, reason: PresentationReasonCurrentContext},
		{name: "broad historical", intent: PresentationIntentBroad, view: View{State: Historical, Valid: true}, decision: PresentationInclude, reason: PresentationReasonHistoricalContext},
		{name: "broad superseded", intent: PresentationIntentBroad, view: View{State: Superseded, Valid: true}, decision: PresentationInclude, reason: PresentationReasonSupersededContext},
		{name: "broad disputed", intent: PresentationIntentBroad, view: View{State: Disputed, Valid: true}, decision: PresentationUncertain, reason: PresentationReasonDisputed},
		{name: "broad invalid", intent: PresentationIntentBroad, view: View{State: Historical}, decision: PresentationSuppress, reason: PresentationReasonInvalidLifecycle},
		{name: "broad expired", intent: PresentationIntentBroad, view: View{State: Historical, Valid: true}, expired: true, decision: PresentationSuppress, reason: PresentationReasonExpired},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := DecidePresentation(test.intent, test.view, test.expired, test.hasCanonicalCurrent)
			if got.Decision != test.decision || !reflect.DeepEqual(got.ReasonCodes, []PresentationReasonCode{test.reason}) {
				t.Fatalf("DecidePresentation() = %#v, want decision=%q reason=%q", got, test.decision, test.reason)
			}
		})
	}
}
