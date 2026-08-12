package integrationbundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

func TestTelemetryDefaultOffAndClosed(t *testing.T) {
	e := TelemetryEvent{ContractVersion: "1.0.0", ScenarioID: "TASK-001", Capability: conformance.CapabilityTodoist, Operation: conformance.OperationTaskCreate, Outcome: conformance.OutcomeSuccess, LatencyBucket: LatencyUnder100MS, ClientFamily: conformance.ClientCodex}
	var out bytes.Buffer
	b := loadTestBundle(t)
	off, err := b.NewTelemetrySink(false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if err := off.Record(e); err != nil || out.Len() != 0 {
		t.Fatalf("default off: %v %q", err, out.String())
	}
	on, err := b.NewTelemetrySink(true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if err := on.Record(e); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt", "fact", "task content", "/Users/", "endpoint", "secret"} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatalf("captured forbidden content %q", forbidden)
		}
	}
	e.ScenarioID = "TASK-999"
	if err := on.Record(e); err == nil {
		t.Fatal("accepted open value")
	}
	e.ScenarioID = "TASK-001"
	e.ContractVersion = "1.0.1"
	if err := on.Record(e); err == nil {
		t.Fatal("accepted unsupported contract version")
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestTelemetryConcurrentLinesShortWriteAndFalseTuple(t *testing.T) {
	b := loadTestBundle(t)
	var out bytes.Buffer
	sink, err := b.NewTelemetrySink(true, &out)
	if err != nil {
		t.Fatal(err)
	}
	event := TelemetryEvent{ContractVersion: "1.0.0", ScenarioID: "TASK-001", Capability: conformance.CapabilityTodoist, Operation: conformance.OperationTaskCreate, Outcome: conformance.OutcomeSuccess, LatencyBucket: LatencyUnder100MS, ClientFamily: conformance.ClientCodex}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := sink.Record(event); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	if len(lines) != 50 {
		t.Fatalf("lines=%d", len(lines))
	}
	for _, line := range lines {
		var v map[string]any
		if json.Unmarshal(line, &v) != nil {
			t.Fatal("invalid concurrent JSONL")
		}
	}
	short, _ := b.NewTelemetrySink(true, shortWriter{})
	if !errors.Is(short.Record(event), io.ErrShortWrite) {
		t.Fatal("short write not detected")
	}
	event.Capability = conformance.CapabilityMemory
	if err := sink.Record(event); err == nil {
		t.Fatal("false scenario tuple accepted")
	}
}
