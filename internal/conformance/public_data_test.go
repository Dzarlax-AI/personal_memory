package conformance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicV1SuiteCoversContractAndPasses(t *testing.T) {
	root := filepath.Join("..", "..")
	suite := loadSuitePath(t, filepath.Join(root, "conformancedata", "public", "v1", "scenarios.json"))
	catalog := loadCatalogPath(t, filepath.Join(root, "docs", "model-usage-contract.md"))
	if len(catalog.ScenarioIDs) != 32 {
		t.Fatalf("contract scenario count = %d, want 32", len(catalog.ScenarioIDs))
	}
	if err := ValidateCoverage(suite, catalog); err != nil {
		t.Fatal(err)
	}
	bundle := loadTraceBundlePath(t,
		filepath.Join(root, "conformancedata", "public", "v1", "traces", "passing.json"))
	if len(bundle.Traces) != 32 {
		t.Fatalf("passing trace count = %d, want 32", len(bundle.Traces))
	}
	report, err := Run(suite, bundle, catalog, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !report.GatesPassed || report.Aggregate != (Aggregate{Pass: 32}) {
		t.Fatalf("public report aggregate = %#v, gates = %t", report.Aggregate, report.GatesPassed)
	}
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range suite.Scenarios {
		if bytes.Contains(data, []byte(scenario.SyntheticInput)) {
			t.Fatalf("report leaks synthetic input for %s", scenario.ID)
		}
	}
}

func TestPublicV1FailingTracesExerciseValidatorReasons(t *testing.T) {
	root := filepath.Join("..", "..")
	suite := loadSuitePath(t, filepath.Join(root, "conformancedata", "public", "v1", "scenarios.json"))
	bundle := loadTraceBundlePath(t,
		filepath.Join(root, "conformancedata", "public", "v1", "traces", "failing.json"))
	scenarios := make(map[string]Scenario, len(suite.Scenarios))
	for _, scenario := range suite.Scenarios {
		scenarios[scenario.ID] = scenario
	}
	expected := map[string]struct {
		status ResultStatus
		reason ReasonCode
	}{
		"codex\x00RECALL-001":       {StatusFail, ReasonRequiredEventMissing},
		"claude\x00TASK-002":        {StatusFail, ReasonForbiddenEvent},
		"chatgpt\x00RECALL-001":     {StatusFail, ReasonEventOrder},
		"generic_mcp\x00RECALL-001": {StatusFail, ReasonEventCount},
		"codex\x00STORE-008":        {StatusFail, ReasonRetryLimit},
		"chatgpt\x00TASK-002":       {StatusInconclusive, ReasonObservation},
		"generic_mcp\x00TASK-002":   {StatusFail, ReasonForbiddenEvent},
		"claude\x00FAILURE-002":     {StatusFail, ReasonRetryLimit},
	}
	if len(bundle.Traces) != len(expected) {
		t.Fatalf("failing trace count = %d, want %d", len(bundle.Traces), len(expected))
	}
	for _, trace := range bundle.Traces {
		key := string(trace.ClientFamily) + "\x00" + trace.ScenarioID
		want, exists := expected[key]
		if !exists {
			t.Fatalf("unexpected failing trace %q", key)
		}
		got := ValidateScenario(scenarios[trace.ScenarioID], trace, suite.ContractVersion)
		if got.Status != want.status || !containsReason(got.Reasons, want.reason) {
			t.Errorf("%s result = %#v, want %s/%s", key, got, want.status, want.reason)
		}
	}
}

func loadSuitePath(t *testing.T, path string) *Suite {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	suite, err := LoadSuite(file)
	if err != nil {
		t.Fatal(err)
	}
	return suite
}

func loadTraceBundlePath(t *testing.T, path string) *TraceBundle {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	bundle, err := LoadTraceBundle(file)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func loadCatalogPath(t *testing.T, path string) ContractCatalog {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	catalog, err := LoadContractCatalog(file)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
