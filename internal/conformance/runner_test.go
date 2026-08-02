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
	bundle := &TraceBundle{
		SchemaVersion: 1, ContractVersion: "1.0.0", Traces: []Trace{trace},
	}
	report, err := Run(suite, bundle, ContractCatalog{
		Version: "1.0.0", ScenarioIDs: []string{"TASK-002"},
	}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !report.GatesPassed || report.Aggregate.Pass != 1 {
		t.Fatalf("report = %#v", report)
	}
	first, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderJSON(report)
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
	if !decoded.GatesPassed || decoded.Aggregate != report.Aggregate {
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
