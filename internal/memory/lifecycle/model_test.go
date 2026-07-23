package lifecycle

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestParseLegacyAndKnownStates(t *testing.T) {
	legacy, err := Parse(map[string]interface{}{}, "legacy-id")
	if err != nil {
		t.Fatalf("parse legacy payload: %v", err)
	}
	if legacy.State != Current || !legacy.Legacy || !legacy.Valid {
		t.Fatalf("unexpected legacy view: %#v", legacy)
	}

	tests := []struct {
		state   State
		extra   map[string]interface{}
		visible bool
	}{
		{state: Current, visible: true},
		{state: Historical},
		{state: Disputed},
		{state: Superseded, extra: map[string]interface{}{"superseded_by": []interface{}{"new-id"}}},
	}
	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			payload := map[string]interface{}{"lifecycle_state": string(test.state)}
			for key, value := range test.extra {
				payload[key] = value
			}
			view, err := Parse(payload, "point-id")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if view.State != test.state || view.Legacy || !view.Valid {
				t.Fatalf("unexpected view: %#v", view)
			}
			if got := IsCurrentTruth(view, false); got != test.visible {
				t.Fatalf("IsCurrentTruth = %v, want %v", got, test.visible)
			}
		})
	}
}

func TestParseNormalizesMetadata(t *testing.T) {
	payload := map[string]interface{}{
		"lifecycle_state": "superseded",
		"provenance": map[string]interface{}{
			"source":    " user ",
			"reference": "ticket-1",
		},
		"verified_at":   "2026-07-21T08:30:00Z",
		"supersedes":    []interface{}{"old", "old", json.Number("42"), float64(7)},
		"superseded_by": []string{"replacement"},
	}
	view, err := Parse(payload, "self")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if view.Provenance == nil || view.Provenance.Source != "user" || view.Provenance.Reference != "ticket-1" {
		t.Fatalf("unexpected provenance: %#v", view.Provenance)
	}
	if got, want := view.Supersedes, []string{"old", "42", "7"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("supersedes = %#v, want %#v", got, want)
	}
	if view.VerifiedAt != "2026-07-21T08:30:00Z" {
		t.Fatalf("verified_at = %q", view.VerifiedAt)
	}
}

func TestParseRejectsMalformedMetadata(t *testing.T) {
	tests := []struct {
		name           string
		payload        map[string]interface{}
		pointID        string
		want           string
		wantState      State
		checkWantState bool
	}{
		{name: "unknown state", payload: map[string]interface{}{"lifecycle_state": "unknown"}, want: "lifecycle_state", wantState: "unknown", checkWantState: true},
		{name: "whitespace state", payload: map[string]interface{}{"lifecycle_state": " current "}, want: "lifecycle_state", wantState: " current ", checkWantState: true},
		{name: "null state", payload: map[string]interface{}{"lifecycle_state": nil}, want: "lifecycle_state", checkWantState: true},
		{name: "non-string state", payload: map[string]interface{}{"lifecycle_state": 1}, want: "lifecycle_state", checkWantState: true},
		{name: "null canonical", payload: map[string]interface{}{"canonical": nil}, want: "boolean"},
		{name: "canonical type", payload: map[string]interface{}{"canonical": "true"}, want: "boolean"},
		{name: "canonical historical", payload: map[string]interface{}{"lifecycle_state": "historical", "canonical": true}, want: "must be current"},
		{name: "superseded relation missing", payload: map[string]interface{}{"lifecycle_state": "superseded"}, want: "require superseded_by"},
		{name: "current has replacement", payload: map[string]interface{}{"superseded_by": []string{"new"}}, want: "cannot have"},
		{name: "provenance not object", payload: map[string]interface{}{"provenance": "user"}, want: "object"},
		{name: "provenance source missing", payload: map[string]interface{}{"provenance": map[string]interface{}{}}, want: "source"},
		{name: "provenance null reference", payload: map[string]interface{}{"provenance": map[string]interface{}{"source": "user", "reference": nil}}, want: "reference"},
		{name: "provenance reference type", payload: map[string]interface{}{"provenance": map[string]interface{}{"source": "user", "reference": 3}}, want: "reference"},
		{name: "invalid timestamp", payload: map[string]interface{}{"verified_at": "2026-07-21"}, want: "RFC3339"},
		{name: "relationships not array", payload: map[string]interface{}{"supersedes": "old"}, want: "array"},
		{name: "empty ID", payload: map[string]interface{}{"supersedes": []string{" "}}, want: "empty"},
		{name: "fractional ID", payload: map[string]interface{}{"supersedes": []interface{}{1.5}}, want: "integer"},
		{name: "negative ID", payload: map[string]interface{}{"supersedes": []interface{}{json.Number("-1")}}, want: "integer"},
		{name: "unsafe float ID", payload: map[string]interface{}{"supersedes": []interface{}{float64(1 << 53)}}, want: "integer"},
		{name: "overflow numeric ID", payload: map[string]interface{}{"supersedes": []interface{}{json.Number("18446744073709551616")}}, want: "integer"},
		{name: "self reference", payload: map[string]interface{}{"supersedes": []string{"self"}}, pointID: "self", want: "itself"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const secret = "TOP_SECRET_FACT_TEXT"
			test.payload["text"] = secret
			view, err := Parse(test.payload, test.pointID)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
			if view.Valid || IsCurrentTruth(view, false) {
				t.Fatalf("invalid metadata became current truth: %#v", view)
			}
			if strings.Contains(view.InvalidReason, secret) || strings.Contains(err.Error(), secret) {
				t.Fatalf("validation error leaked fact text: error=%q reason=%q", err, view.InvalidReason)
			}
			if view.Legacy {
				t.Fatalf("explicit malformed lifecycle metadata was marked legacy: %#v", view)
			}
			if test.checkWantState && view.State != test.wantState {
				t.Fatalf("invalid state = %q, want %q", view.State, test.wantState)
			}
		})
	}
}

func TestValidateTransitionAllowsExplicitCorrectionsAndIdempotence(t *testing.T) {
	states := []State{Current, Historical, Disputed, Superseded}
	for _, from := range states {
		for _, to := range states {
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				target := View{State: to, Valid: true}
				if to == Superseded {
					target.SupersededBy = []string{"replacement"}
				}
				if err := ValidateTransition("point-id", View{State: from, Valid: true}, target); err != nil {
					t.Fatalf("valid explicit correction rejected: %v", err)
				}
			})
		}
	}

	if err := ValidateTransition("point-id", View{State: Current, Valid: true}, View{State: Historical, Canonical: true, Valid: true}); err == nil {
		t.Fatal("invalid target transition accepted")
	}
	invalidTargets := []View{
		{State: Current, Valid: false},
		{State: Current, Valid: true, VerifiedAt: "not-a-time"},
		{State: Current, Valid: true, Provenance: &Provenance{}},
		{State: Historical, Valid: true, Supersedes: []string{"duplicate", "duplicate"}},
		{State: Historical, Valid: true, Supersedes: []string{"point-id", " point-id "}},
	}
	for _, target := range invalidTargets {
		if err := ValidateTransition("point-id", View{State: Current, Valid: true}, target); err == nil {
			t.Fatalf("invalid direct target accepted: %#v", target)
		}
	}
	if err := ValidateTransition("point-id", View{State: Current, Valid: true}, View{State: Historical, Valid: true, Supersedes: []string{"point-id"}}); err == nil {
		t.Fatal("self-referencing transition accepted")
	}
	if err := ValidateTransition("", View{State: Current, Valid: true}, View{State: Historical, Valid: true}); err == nil {
		t.Fatal("transition without point ID accepted")
	}
}

func TestNormalizeInputAndApplyToPayload(t *testing.T) {
	input := Input{
		State:     Superseded,
		Canonical: false,
		Provenance: &Provenance{
			Source:    " user ",
			Reference: "decision-7",
		},
		VerifiedAt:   "2026-07-23T10:00:00Z",
		Supersedes:   []string{"old", "old"},
		SupersededBy: []string{"new"},
	}
	view, err := NormalizeInput("self", input)
	if err != nil {
		t.Fatalf("NormalizeInput: %v", err)
	}
	if view.Legacy || !view.Valid || view.Provenance == nil || view.Provenance.Source != "user" {
		t.Fatalf("unexpected normalized view: %#v", view)
	}
	if got, want := view.Supersedes, []string{"old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("supersedes = %#v, want %#v", got, want)
	}

	original := map[string]interface{}{
		"text":              "private",
		"recall_count":      float64(9),
		"lifecycle_state":   "current",
		"canonical":         true,
		"obsolete_metadata": "preserved",
	}
	applied := ApplyToPayload(original, view)
	if original["lifecycle_state"] != "current" {
		t.Fatal("ApplyToPayload mutated the source payload")
	}
	if applied["lifecycle_state"] != "superseded" || applied["canonical"] != false {
		t.Fatalf("unexpected lifecycle payload: %#v", applied)
	}
	if applied["text"] != "private" || applied["recall_count"] != float64(9) || applied["obsolete_metadata"] != "preserved" {
		t.Fatalf("unrelated metadata changed: %#v", applied)
	}
}

func TestApplyToPayloadClearsAbsentOptionalLifecycleFields(t *testing.T) {
	view, err := NormalizeInput("self", Input{State: Historical})
	if err != nil {
		t.Fatalf("NormalizeInput: %v", err)
	}
	applied := ApplyToPayload(map[string]interface{}{
		"text":          "private",
		"provenance":    map[string]interface{}{"source": "old"},
		"verified_at":   "2026-07-20T00:00:00Z",
		"supersedes":    []interface{}{"old"},
		"superseded_by": []interface{}{"replacement"},
	}, view)
	for _, key := range []string{"provenance", "verified_at"} {
		if _, exists := applied[key]; exists {
			t.Fatalf("%s was not cleared: %#v", key, applied)
		}
	}
	if got, ok := applied["supersedes"].([]string); !ok || len(got) != 0 {
		t.Fatalf("supersedes = %#v, want empty []string", applied["supersedes"])
	}
	if got, ok := applied["superseded_by"].([]string); !ok || len(got) != 0 {
		t.Fatalf("superseded_by = %#v, want empty []string", applied["superseded_by"])
	}
}

func TestNormalizeInputRejectsInvalidTargets(t *testing.T) {
	tests := []struct {
		name  string
		input Input
	}{
		{name: "unknown state", input: Input{State: "unknown"}},
		{name: "canonical historical", input: Input{State: Historical, Canonical: true}},
		{name: "superseded without replacement", input: Input{State: Superseded}},
		{name: "current with replacement", input: Input{State: Current, SupersededBy: []string{"new"}}},
		{name: "invalid verified at", input: Input{State: Current, VerifiedAt: "2026-07-23"}},
		{name: "self reference", input: Input{State: Historical, Supersedes: []string{"self"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeInput("self", test.input); err == nil {
				t.Fatalf("NormalizeInput accepted invalid target: %#v", test.input)
			}
		})
	}
}

func TestValidateRelationshipsForPointIgnoresUnrelatedMalformedMetadata(t *testing.T) {
	payload := map[string]interface{}{
		"lifecycle_state": "wrong",
		"canonical":       "not-a-boolean",
		"supersedes":      []interface{}{"old"},
	}
	if err := ValidateRelationshipsForPoint(payload, "new"); err != nil {
		t.Fatalf("unrelated malformed metadata blocked relationship validation: %v", err)
	}
	payload["supersedes"] = []interface{}{"new"}
	if err := ValidateRelationshipsForPoint(payload, "new"); err == nil {
		t.Fatal("self-reference was accepted")
	}
}

func TestCurrentTruthExpirationAndRetentionIndependence(t *testing.T) {
	view := View{State: Current, Valid: true}
	if !IsCurrentTruth(view, false) {
		t.Fatal("current non-expired fact should be visible")
	}
	if IsCurrentTruth(view, true) {
		t.Fatal("expired current fact should not be visible")
	}
	// IsCurrentTruth deliberately has no permanent argument: retention cannot
	// raise authority or override expiration.
}

func TestSortCandidatesUsesLifecycleThenScoreThenPointID(t *testing.T) {
	candidates := []Candidate{
		{PointID: "s", Score: .99, View: View{State: Superseded, SupersededBy: []string{"n"}, Valid: true}},
		{PointID: "b", Score: .8, View: View{State: Current, Valid: true}},
		{PointID: "a", Score: .8, View: View{State: Current, Valid: true}},
		{PointID: "z", Score: .9, View: View{State: Current, Valid: true}},
		{PointID: "d", Score: .95, View: View{State: Disputed, Valid: true}},
		{PointID: "h", Score: .96, View: View{State: Historical, Valid: true}},
		{PointID: "c", Score: .7, View: View{State: Current, Canonical: true, Valid: true}},
		{PointID: "bad", Score: 1, View: View{State: Current, Valid: false}},
		{PointID: "nan", Score: math.NaN(), View: View{State: Current, Canonical: true, Valid: true}},
	}
	SortCandidates(candidates)
	got := make([]string, len(candidates))
	for i := range candidates {
		got[i] = candidates[i].PointID
	}
	want := []string{"c", "z", "a", "b", "d", "h", "s", "bad", "nan"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %#v, want %#v", got, want)
	}
	if candidates[0].Score != .7 || candidates[6].Score != .99 || !math.IsNaN(candidates[8].Score) {
		t.Fatal("sorting rewrote raw scores")
	}
}
