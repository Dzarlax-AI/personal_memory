package integrationbundle

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

type LatencyBucket string

const (
	LatencyUnder100MS LatencyBucket = "under_100ms"
	Latency100To500MS LatencyBucket = "100_to_500ms"
	Latency500MSTo2S  LatencyBucket = "500ms_to_2s"
	LatencyOver2S     LatencyBucket = "over_2s"
)

// TelemetryEvent is deliberately closed and content-free. It exactly matches
// the bundle policy allowlist and has no extension or metadata field.
type TelemetryEvent struct {
	ContractVersion string                   `json:"contract_version"`
	ScenarioID      string                   `json:"scenario_id"`
	Capability      conformance.Capability   `json:"capability"`
	Operation       conformance.Operation    `json:"operation"`
	Outcome         conformance.Outcome      `json:"outcome"`
	LatencyBucket   LatencyBucket            `json:"latency_bucket"`
	RetryCount      int                      `json:"retry_count"`
	ClientFamily    conformance.ClientFamily `json:"client_family"`
}

type TelemetrySink struct {
	enabled         bool
	writer          io.Writer
	contractVersion string
	scenarios       map[string]bool
	tuples          map[string]map[string]bool
	mu              *sync.Mutex
}

func (b *Bundle) NewTelemetrySink(enabled bool, writer io.Writer) (TelemetrySink, error) {
	if b == nil {
		return TelemetrySink{}, fmt.Errorf("bundle must not be nil")
	}
	scenarios := map[string]bool{}
	for _, m := range b.policy.ScenarioMappings {
		scenarios[m.ScenarioID] = true
	}
	if enabled && writer == nil {
		return TelemetrySink{}, fmt.Errorf("telemetry is enabled without a local sink")
	}
	return TelemetrySink{enabled: enabled, writer: writer, contractVersion: b.manifest.ContractVersion, scenarios: scenarios, tuples: b.telemetryTuples, mu: &sync.Mutex{}}, nil
}

func (s TelemetrySink) Record(event TelemetryEvent) error {
	if !s.enabled {
		return nil
	}
	if s.writer == nil {
		return fmt.Errorf("telemetry is enabled without a local sink")
	}
	if err := s.validate(event); err != nil {
		return err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	line := append(encoded, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.writer.Write(line)
	if err == nil && n != len(line) {
		return io.ErrShortWrite
	}
	return err
}

func (s TelemetrySink) validate(e TelemetryEvent) error {
	if e.ContractVersion != s.contractVersion || !s.scenarios[e.ScenarioID] || e.RetryCount < 0 || e.RetryCount > 1 {
		return fmt.Errorf("telemetry event contains an invalid closed value")
	}
	key := string(e.Capability) + "\x00" + string(e.Operation) + "\x00" + string(e.Outcome)
	if !s.tuples[e.ScenarioID][key] {
		return fmt.Errorf("telemetry event tuple is not allowed for scenario")
	}
	if e.Capability != conformance.CapabilityMemory && e.Capability != conformance.CapabilityDocuments && e.Capability != conformance.CapabilityTodoist && e.Capability != conformance.CapabilityOrdinaryContext {
		return fmt.Errorf("telemetry event contains an invalid closed value")
	}
	switch e.Operation {
	case conformance.OperationRecall, conformance.OperationSearch, conformance.OperationStore, conformance.OperationTaskList, conformance.OperationTaskCreate, conformance.OperationTaskUpdate, conformance.OperationTaskComplete, conformance.OperationTaskDelete, conformance.OperationLifecycle, conformance.OperationFallback:
	default:
		return fmt.Errorf("telemetry event contains an invalid closed value")
	}
	switch e.Outcome {
	case conformance.OutcomeSuccess, conformance.OutcomeEmpty, conformance.OutcomeDuplicate, conformance.OutcomeUnavailable, conformance.OutcomeTimeout, conformance.OutcomeRejected, conformance.OutcomeAmbiguous, conformance.OutcomeError:
	default:
		return fmt.Errorf("telemetry event contains an invalid closed value")
	}
	switch e.LatencyBucket {
	case LatencyUnder100MS, Latency100To500MS, Latency500MSTo2S, LatencyOver2S:
	default:
		return fmt.Errorf("telemetry event contains an invalid closed value")
	}
	if !validClient(e.ClientFamily) {
		return fmt.Errorf("telemetry event contains an invalid closed value")
	}
	return nil
}
func buildTelemetryTuples(suite *conformance.Suite) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, s := range suite.Scenarios {
		tuples := map[string]bool{}
		add := func(p conformance.EventPattern) {
			if p.Capability == "" || p.Operation == "" || p.Outcome == "" {
				return
			}
			tuples[string(p.Capability)+"\x00"+string(p.Operation)+"\x00"+string(p.Outcome)] = true
		}
		for _, p := range s.Assertions.Must {
			add(p)
		}
		for _, a := range s.Assertions.AnyOf {
			for _, p := range a.Must {
				add(p)
			}
		}
		for _, c := range s.Assertions.Counts {
			add(c.Pattern)
		}
		out[s.ID] = tuples
	}
	return out
}
