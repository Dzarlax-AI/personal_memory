package conformance

import (
	"bytes"
	"testing"
)

func TestRunProducesDeterministicPrivacySafeReport(t *testing.T) {
	suite, err := LoadSuite(bytes.NewBufferString(validSuiteJSON))
	if err != nil {
		t.Fatal(err)
	}
	trace, err := DecodeTrace([]byte(validTraceJSON))
	if err != nil {
		t.Fatal(err)
	}
	secondTrace := trace
	secondTrace.ClientFamily = ClientClaude
	catalog := ContractCatalog{
		Version: "1.0.0", ScenarioIDs: []string{"TASK-002"},
	}
	firstReport, err := Run(suite, &TraceBundle{
		SchemaVersion: 1, ContractVersion: "1.0.0", Traces: []Trace{trace, secondTrace},
	}, catalog, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	secondReport, err := Run(suite, &TraceBundle{
		SchemaVersion: 1, ContractVersion: "1.0.0", Traces: []Trace{secondTrace, trace},
	}, catalog, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !firstReport.GatesPassed || firstReport.Aggregate.Pass != 2 {
		t.Fatalf("report = %#v", firstReport)
	}
	first, err := RenderJSON(firstReport)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderJSON(secondReport)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("JSON report is not deterministic")
	}
	decoded, err := DecodeReport(first)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.GatesPassed || decoded.Aggregate != firstReport.Aggregate {
		t.Fatalf("decoded report = %#v", decoded)
	}
	for _, forbidden := range [][]byte{
		[]byte("Synthetic reminder"), []byte("prompt"), []byte("tool_arguments"),
	} {
		if bytes.Contains(first, forbidden) {
			t.Fatalf("report contains forbidden content %q", forbidden)
		}
	}
}

func TestRunRejectsNilAndDuplicateTraceBundles(t *testing.T) {
	suite, err := LoadSuite(bytes.NewBufferString(validSuiteJSON))
	if err != nil {
		t.Fatal(err)
	}
	catalog := ContractCatalog{Version: "1.0.0", ScenarioIDs: []string{"TASK-002"}}
	if _, err := Run(suite, nil, catalog, "fixture"); err == nil {
		t.Fatal("Run() accepted nil bundle")
	}
	trace, err := DecodeTrace([]byte(validTraceJSON))
	if err != nil {
		t.Fatal(err)
	}
	bundle := &TraceBundle{
		SchemaVersion: 1, ContractVersion: "1.0.0", Traces: []Trace{trace, trace},
	}
	if _, err := Run(suite, bundle, catalog, "fixture"); err == nil {
		t.Fatal("Run() accepted duplicate client-scenario traces")
	}
}

func TestDecodeReportRejectsInconsistentResultReasons(t *testing.T) {
	report := Report{
		SchemaVersion: 1, ContractVersion: "1.0.0", SuiteVersion: "1.0.0",
		Source: "fixture", GatesPassed: true, Aggregate: Aggregate{Pass: 1},
		Results: []ScenarioResult{{
			ScenarioID: "TASK-002", ClientFamily: ClientSynthetic,
			Status: StatusPass, Reasons: []ReasonCode{ReasonObservation},
		}},
	}
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(data); err == nil {
		t.Fatal("DecodeReport() accepted a passing result with reasons")
	}
}

func TestReportNormalizationDeduplicatesReasonsAndRejectsEmptyResults(t *testing.T) {
	report := Report{
		SchemaVersion: 1, ContractVersion: "1.0.0", SuiteVersion: "1.0.0",
		Source: "fixture", Aggregate: Aggregate{Fail: 1},
		Results: []ScenarioResult{{
			ScenarioID: "TASK-002", ClientFamily: ClientSynthetic, Status: StatusFail,
			Reasons: []ReasonCode{ReasonObservation, ReasonObservation},
		}},
	}
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(data); err != nil {
		t.Fatalf("DecodeReport() rejected normalized report: %v", err)
	}
	empty := []byte(`{
	  "schema_version": 1,
	  "contract_version": "1.0.0",
	  "suite_version": "1.0.0",
	  "source": "fixture",
	  "gates_passed": true,
	  "aggregate": {"pass":0,"fail":0,"inconclusive":0,"error":0},
	  "results": []
	}`)
	if _, err := DecodeReport(empty); err == nil {
		t.Fatal("DecodeReport() accepted an empty report")
	}
}
