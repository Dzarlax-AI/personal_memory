package eval

import (
	"encoding/json"
	"strings"
	"testing"
)

const validLifecycleFields = `
    "intent": "current",
    "lifecycle_expectations": [{
      "id": "42",
      "state": "current",
      "decision": "include",
      "reason_codes": ["current_truth"]
    }],`

const validTransitionScenarios = `,
  "transition_scenarios": [{
    "id": "current-to-historical",
    "point_id": 42,
    "source_lifecycle": {
      "state": "current",
      "canonical": true,
      "supersedes": [],
      "superseded_by": []
    },
    "target_lifecycle": {
      "state": "historical",
      "canonical": false,
      "supersedes": [],
      "superseded_by": []
    },
    "expected_valid": true,
    "expected_reason_code": "transition_valid"
  }]`

func validV2Dataset() string {
	value := strings.Replace(validDataset, `"schema_version": 1`, `"schema_version": 2`, 1)
	value = strings.Replace(value, `    "id": "q1",`, `    "id": "q1",`+validLifecycleFields, 1)
	return strings.Replace(value, "\n}", validTransitionScenarios+"\n}", 1)
}

func TestLoadV1NormalizesOmittedIntentToCurrent(t *testing.T) {
	dataset, err := Load(strings.NewReader(validDataset))
	if err != nil {
		t.Fatal(err)
	}
	if dataset.SchemaVersion != 1 || dataset.Queries[0].Intent != "" ||
		dataset.Queries[0].EffectiveIntent() != QueryIntentCurrent {
		t.Fatalf("schema/raw/effective intent = %d/%q/%q, want 1/empty/%q",
			dataset.SchemaVersion, dataset.Queries[0].Intent, dataset.Queries[0].EffectiveIntent(), QueryIntentCurrent)
	}
}

func TestLoadV1RoundTripPreservesOmittedIntent(t *testing.T) {
	dataset, err := Load(strings.NewReader(validDataset))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"intent"`) {
		t.Fatalf("marshaled v1 dataset unexpectedly contains intent: %s", encoded)
	}
	if _, err := Load(strings.NewReader(string(encoded))); err != nil {
		t.Fatalf("reload marshaled v1 dataset: %v", err)
	}
}

func TestLoadV2AcceptsLifecycleSchema(t *testing.T) {
	dataset, err := Load(strings.NewReader(validV2Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	if dataset.SchemaVersion != 2 || len(dataset.TransitionScenarios) != 1 {
		t.Fatalf("schema/transitions = %d/%d", dataset.SchemaVersion, len(dataset.TransitionScenarios))
	}
	expectation := dataset.Queries[0].LifecycleExpectations[0]
	if expectation.State != "current" || expectation.Decision != PresentationInclude {
		t.Fatalf("lifecycle expectation = %#v", expectation)
	}
}

func TestLoadV2AcceptsAllPresentationDecisions(t *testing.T) {
	for _, decision := range []string{"include", "suppress", "demote", "uncertain"} {
		t.Run(decision, func(t *testing.T) {
			value := strings.Replace(validV2Dataset(), `"state": "current",`+"\n      ", "", 1)
			value = strings.Replace(value, `"decision": "include"`, `"decision": "`+decision+`"`, 1)
			if _, err := Load(strings.NewReader(value)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoadV2AcceptsAllQueryIntents(t *testing.T) {
	tests := []struct {
		intent string
		asOf   string
	}{
		{intent: "current"},
		{intent: "history"},
		{intent: "as_of", asOf: `"as_of": "2025-03-14",`},
		{intent: "uncertainty"},
	}
	for _, tt := range tests {
		t.Run(tt.intent, func(t *testing.T) {
			value := strings.Replace(validV2Dataset(), `"intent": "current",`, `"intent": "`+tt.intent+`",`+tt.asOf, 1)
			dataset, err := Load(strings.NewReader(value))
			if err != nil {
				t.Fatal(err)
			}
			if string(dataset.Queries[0].Intent) != tt.intent {
				t.Fatalf("intent = %q, want %q", dataset.Queries[0].Intent, tt.intent)
			}
		})
	}
}

func TestLoadV2RejectsInvalidIntentAndAsOfCombinations(t *testing.T) {
	tests := []struct {
		name    string
		replace string
		with    string
		want    string
	}{
		{"unknown intent", `"intent": "current"`, `"intent": "future"`, "intent"},
		{"explicit empty intent", `"intent": "current"`, `"intent": ""`, "intent"},
		{"as_of missing date", `"intent": "current"`, `"intent": "as_of"`, "as_of"},
		{"as_of malformed date", `"intent": "current"`, `"intent": "as_of", "as_of": "14-03-2025"`, "YYYY-MM-DD"},
		{"current rejects date", `"intent": "current"`, `"intent": "current", "as_of": "2025-03-14"`, "only valid"},
		{"current rejects explicit empty as_of", `"intent": "current"`, `"intent": "current", "as_of": ""`, "only valid"},
		{"as_of intent rejects null", `"intent": "current"`, `"intent": "as_of", "as_of": null`, "as_of must be a string"},
		{"current intent rejects null as_of", `"intent": "current"`, `"intent": "current", "as_of": null`, "as_of must be a string"},
		{"null intent", `"intent": "current"`, `"intent": null`, "intent must be a string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(strings.NewReader(strings.Replace(validV2Dataset(), tt.replace, tt.with, 1)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadV2RejectsDocumentLifecycleIntent(t *testing.T) {
	value := strings.Replace(validV2Dataset(), `"target": "facts"`, `"target": "documents"`, 1)
	value = strings.Replace(value, `"intent": "current"`, `"intent": "history"`, 1)
	_, err := Load(strings.NewReader(value))
	if err == nil || !strings.Contains(err.Error(), "document") {
		t.Fatalf("Load() error = %v, want document lifecycle error", err)
	}
}

func TestLoadV2RejectsInvalidLifecycleExpectations(t *testing.T) {
	tests := []struct {
		name    string
		replace string
		with    string
		want    string
	}{
		{"unknown state", `"state": "current"`, `"state": "future"`, "state"},
		{"explicit empty state", `"state": "current"`, `"state": ""`, "state"},
		{"null state", `"state": "current"`, `"state": null`, "state must be a string"},
		{"null state with suppress", `"state": "current",` + "\n      " + `"decision": "include"`, `"state": null,` + "\n      " + `"decision": "suppress"`, "state must be a string"},
		{"unknown decision", `"decision": "include"`, `"decision": "hide"`, "decision"},
		{"null decision", `"decision": "include"`, `"decision": null`, "decision must be a string"},
		{"missing decision", `"decision": "include",`, ``, "decision"},
		{"invalid ID", `"id": "42"`, `"id": "not-qdrant"`, "Qdrant point ID"},
		{"empty reason code", `"reason_codes": ["current_truth"]`, `"reason_codes": [""]`, "reason code"},
		{"duplicate reason code", `"reason_codes": ["current_truth"]`, `"reason_codes": ["current_truth", "current_truth"]`, "duplicate reason code"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(strings.NewReader(strings.Replace(validV2Dataset(), tt.replace, tt.with, 1)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, tt.want)
			}
		})
	}

	duplicate := strings.Replace(
		validV2Dataset(),
		`}],`+"\n    \"target\"",
		`}, {"id": "42", "decision": "suppress"}],`+"\n    \"target\"",
		1,
	)
	_, err := Load(strings.NewReader(duplicate))
	if err == nil || !strings.Contains(err.Error(), "duplicate lifecycle expectation") {
		t.Fatalf("Load() error = %v, want duplicate lifecycle expectation", err)
	}
}

func TestValidateForFixtureChecksLifecycleExpectationReferences(t *testing.T) {
	dataset, err := Load(strings.NewReader(validV2Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Queries[0].LifecycleExpectations[0].ID = "22222222-2222-4222-8222-222222222222"
	if err := dataset.ValidateForSource("live"); err != nil {
		t.Fatalf("live validation rejected normalized lifecycle expectation ID: %v", err)
	}
	if err := dataset.ValidateForSource("fixture"); err == nil || !strings.Contains(err.Error(), "unknown lifecycle expectation ID") {
		t.Fatalf("fixture validation error = %v, want unknown lifecycle expectation ID", err)
	}
}

func TestLoadV2RejectsInvalidTransitionScenarios(t *testing.T) {
	tests := []struct {
		name    string
		replace string
		with    string
		want    string
	}{
		{"invalid point ID", `"point_id": 42`, `"point_id": "not-qdrant"`, "UUID"},
		{"missing target state", `"state": "historical",`, ``, "target_lifecycle.state"},
		{"invalid target state", `"state": "historical"`, `"state": "future"`, "target_lifecycle.state"},
		{"missing expected valid", `"expected_valid": true,`, ``, "expected_valid"},
		{"null expected valid", `"expected_valid": true`, `"expected_valid": null`, "expected_valid must be a boolean"},
		{"null canonical", `"canonical": true`, `"canonical": null`, "canonical must be a boolean"},
		{"empty reason code", `"expected_reason_code": "transition_valid"`, `"expected_reason_code": ""`, "expected_reason_code"},
		{"unknown nested field", `"canonical": true,`, `"canonical": true, "authority": 10,`, "unknown field"},
		{"unknown scenario field", `"expected_valid": true,`, `"expected_valid": true, "note": "no",`, "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(strings.NewReader(strings.Replace(validV2Dataset(), tt.replace, tt.with, 1)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, tt.want)
			}
		})
	}

	duplicate := strings.Replace(
		validV2Dataset(),
		"}]\n}",
		`}, {
    "id": "current-to-historical",
    "point_id": 42,
    "source_lifecycle": {"state": "current", "canonical": false, "supersedes": [], "superseded_by": []},
    "target_lifecycle": {"state": "current", "canonical": false, "supersedes": [], "superseded_by": []},
    "expected_valid": true
  }]
}`,
		1,
	)
	_, err := Load(strings.NewReader(duplicate))
	if err == nil || !strings.Contains(err.Error(), "duplicate transition scenario ID") {
		t.Fatalf("Load() error = %v, want duplicate transition scenario ID", err)
	}
}

func TestLoadV2RejectsUnknownLifecycleFields(t *testing.T) {
	value := strings.Replace(validV2Dataset(), `"decision": "include",`, `"decision": "include", "explanation": "no",`, 1)
	_, err := Load(strings.NewReader(value))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v, want unknown field", err)
	}
}

func TestLoadV1RejectsV2LifecycleFields(t *testing.T) {
	tests := []struct {
		name   string
		insert string
	}{
		{"current intent", `"intent": "current",`},
		{"empty intent", `"intent": "",`},
		{"empty lifecycle expectations", `"lifecycle_expectations": [],`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := strings.Replace(validDataset, `    "id": "q1",`, `    "id": "q1",`+"\n    "+tt.insert, 1)
			_, err := Load(strings.NewReader(value))
			if err == nil || !strings.Contains(err.Error(), "schema_version 2") {
				t.Fatalf("Load() error = %v, want schema_version 2 requirement", err)
			}
		})
	}

	t.Run("empty transition scenarios", func(t *testing.T) {
		value := strings.Replace(validDataset, "\n}", ",\n  \"transition_scenarios\": []\n}", 1)
		_, err := Load(strings.NewReader(value))
		if err == nil || !strings.Contains(err.Error(), "schema_version 2") {
			t.Fatalf("Load() error = %v, want schema_version 2 requirement", err)
		}
	})

	t.Run("nonempty lifecycle expectations", func(t *testing.T) {
		value := strings.Replace(validDataset, `    "id": "q1",`, `    "id": "q1",`+validLifecycleFields, 1)
		_, err := Load(strings.NewReader(value))
		if err == nil || !strings.Contains(err.Error(), "schema_version 2") {
			t.Fatalf("Load() error = %v, want schema_version 2 requirement", err)
		}
	})
}

func TestLoadV2AcceptsOnlyOmittedIntentAsDefaultCurrent(t *testing.T) {
	value := strings.Replace(validV2Dataset(), `    "intent": "current",`+"\n", "", 1)
	dataset, err := Load(strings.NewReader(value))
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Queries[0].Intent != "" || dataset.Queries[0].EffectiveIntent() != QueryIntentCurrent {
		t.Fatalf("raw/effective intent = %q/%q, want empty/%q",
			dataset.Queries[0].Intent, dataset.Queries[0].EffectiveIntent(), QueryIntentCurrent)
	}
	if err := dataset.Validate(); err != nil {
		t.Fatalf("repeated validation rejected omitted v2 intent: %v", err)
	}
}
