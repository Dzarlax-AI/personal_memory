package conformance

import (
	"strings"
	"testing"
)

const validSuiteJSON = `{
  "schema_version": 1,
  "contract_version": "1.0.0",
  "suite_version": "1.0.0",
  "scenarios": [{
    "id": "TASK-002",
    "intent_class": "create_reminder",
    "synthetic_input": "Synthetic reminder",
    "capabilities": {"memory":"available","documents":"disabled","todoist":"disabled"},
    "required_observations": ["capabilities","tool_events","user_visible_claims"],
    "assertions": {
      "must": [{"event":"disclosure","code":"task_not_created"}],
      "must_not": [{"event":"tool_call","capability":"memory","operation":"store"}]
    }
  }]
}`

const validTraceJSON = `{
  "schema_version": 1,
  "contract_version": "1.0.0",
  "scenario_id": "TASK-002",
  "client_family": "codex",
  "observed": ["capabilities","tool_events","user_visible_claims"],
  "events": [
    {"sequence":1,"event":"capability","capability":"todoist","outcome":"unavailable"},
    {"sequence":2,"event":"disclosure","code":"task_not_created"}
  ]
}`

func TestLoadSuiteStrictlyValidatesSchema(t *testing.T) {
	if _, err := LoadSuite(strings.NewReader(validSuiteJSON)); err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}
	tests := []struct {
		name string
		old  string
		new  string
		want string
	}{
		{"unknown field", `"suite_version": "1.0.0",`, `"suite_version": "1.0.0", "private_prompt": "no",`, "unknown field"},
		{"invalid ID", `"TASK-002"`, `"private scenario"`, "scenario ID"},
		{"missing capability", `"documents":"disabled",`, ``, "capability \"documents\" is required"},
		{"unknown event code", `"task_not_created"`, `"raw response text"`, "code"},
		{"assertion observation missing", `"capabilities","tool_events","user_visible_claims"`, `"capabilities","tool_events"`, "user_visible_claims"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := strings.Replace(validSuiteJSON, tt.old, tt.new, 1)
			_, err := LoadSuite(strings.NewReader(value))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadSuite() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadSuiteRejectsNullScenarios(t *testing.T) {
	start := strings.Index(validSuiteJSON, `"scenarios":`)
	if start < 0 {
		t.Fatal("valid suite fixture has no scenarios field")
	}
	value := validSuiteJSON[:start] + `"scenarios": null` + "\n}"
	_, err := LoadSuite(strings.NewReader(value))
	if err == nil || !strings.Contains(err.Error(), "non-empty array") {
		t.Fatalf("LoadSuite() error = %v, want non-empty array failure", err)
	}
}

func TestDecodeTraceRejectsPrivateAndMalformedFields(t *testing.T) {
	if _, err := DecodeTrace([]byte(validTraceJSON)); err != nil {
		t.Fatalf("DecodeTrace() error = %v", err)
	}
	tests := []struct {
		name string
		old  string
		new  string
		want string
	}{
		{"private text", `"client_family": "codex",`, `"client_family": "codex", "prompt": "secret",`, "unknown field"},
		{"unknown family", `"codex"`, `"other"`, "client_family"},
		{"duplicate observation", `"capabilities","tool_events"`, `"capabilities","capabilities","tool_events"`, "duplicated"},
		{"sequence order", `"sequence":2`, `"sequence":1`, "strictly increasing"},
		{"unknown code", `"task_not_created"`, `"task created: private title"`, "code"},
		{"invalid retry target", `"sequence":2,"event":"disclosure"`, `"sequence":2,"event":"disclosure","retry_of":1`, "valid only for tool_call"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := strings.Replace(validTraceJSON, tt.old, tt.new, 1)
			_, err := DecodeTrace([]byte(value))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DecodeTrace() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDecodeTraceRejectsOrphanToolResult(t *testing.T) {
	value := strings.Replace(
		validTraceJSON,
		`{"sequence":1,"event":"capability","capability":"todoist","outcome":"unavailable"}`,
		`{"sequence":1,"event":"tool_result","capability":"todoist","operation":"task_create","outcome":"success"}`,
		1,
	)
	_, err := DecodeTrace([]byte(value))
	if err == nil || !strings.Contains(err.Error(), "no matching preceding tool_call") {
		t.Fatalf("DecodeTrace() error = %v, want orphan result failure", err)
	}
}

func TestLoadContractCatalogAndCoverage(t *testing.T) {
	contract := `# Contract
Contract version: **1.0.0**
## Conformance scenarios
| ID |
| --- |
| ` + "`RECALL-001`" + ` |
| ` + "`TASK-002`" + ` |
## Additional guidance
Mention ` + "`IGNORED-999`" + ` later.
`
	catalog, err := LoadContractCatalog(strings.NewReader(contract))
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Version != "1.0.0" || len(catalog.ScenarioIDs) != 2 ||
		catalog.ScenarioIDs[0] != "RECALL-001" || catalog.ScenarioIDs[1] != "TASK-002" {
		t.Fatalf("catalog = %#v", catalog)
	}
	suite, err := LoadSuite(strings.NewReader(validSuiteJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCoverage(suite, catalog); err == nil || !strings.Contains(err.Error(), "covers 1") {
		t.Fatalf("ValidateCoverage() error = %v, want coverage failure", err)
	}
	catalog.ScenarioIDs = []string{"TASK-002"}
	if err := ValidateCoverage(suite, catalog); err != nil {
		t.Fatalf("ValidateCoverage() error = %v", err)
	}
}

func TestLoadContractCatalogRejectsDuplicateIDs(t *testing.T) {
	contract := `Contract version: **1.0.0**
## Conformance scenarios
| ` + "`TASK-002`" + ` |
| ` + "`TASK-002`" + ` |
## Contract evolution
`
	_, err := LoadContractCatalog(strings.NewReader(contract))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("LoadContractCatalog() error = %v, want duplicate failure", err)
	}
}
