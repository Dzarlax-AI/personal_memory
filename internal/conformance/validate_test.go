package conformance

import "testing"

func TestValidateScenarioAssertionMatrix(t *testing.T) {
	zero, one := 0, 1
	scenario := Scenario{
		ID: "RECALL-001", RequiredObservations: []Observation{ObservationToolEvents},
		Assertions: Assertions{
			Must:    []EventPattern{{Event: EventToolCall, Capability: CapabilityMemory, Operation: OperationRecall}},
			MustNot: []EventPattern{{Event: EventClaim, Code: CodePreferenceInvented}},
			Counts: []CountAssertion{{
				Pattern: EventPattern{Event: EventToolCall, Capability: CapabilityMemory, Operation: OperationRecall},
				Min:     &one, Max: &one,
			}},
			Ordered: []OrderAssertion{{
				Before: EventPattern{Event: EventToolCall, Capability: CapabilityMemory, Operation: OperationRecall},
				After:  EventPattern{Event: EventClaim, Code: CodeFactFound},
			}},
			MaxRetries: &zero,
		},
	}
	base := Trace{
		ContractVersion: "1.0.0", ScenarioID: "RECALL-001",
		Observed: []Observation{ObservationToolEvents},
		Events: []Event{
			{Sequence: 1, Event: EventToolCall, Capability: CapabilityMemory, Operation: OperationRecall},
			{Sequence: 2, Event: EventClaim, Code: CodeFactFound},
		},
	}
	if got := ValidateScenario(scenario, base, "1.0.0"); got.Status != StatusPass {
		t.Fatalf("base result = %#v", got)
	}
	tests := []struct {
		name   string
		mutate func(*Trace)
		status ResultStatus
		reason ReasonCode
	}{
		{"missing observation", func(trace *Trace) { trace.Observed = nil }, StatusInconclusive, ReasonObservation},
		{"required missing", func(trace *Trace) { trace.Events = trace.Events[1:] }, StatusFail, ReasonRequiredEventMissing},
		{"forbidden present", func(trace *Trace) {
			trace.Events = append(trace.Events, Event{Sequence: 3, Event: EventClaim, Code: CodePreferenceInvented})
		}, StatusFail, ReasonForbiddenEvent},
		{"count invalid", func(trace *Trace) {
			trace.Events = append(trace.Events, Event{
				Sequence: 3, Event: EventToolCall, Capability: CapabilityMemory, Operation: OperationRecall,
			})
		}, StatusFail, ReasonEventCount},
		{"order invalid", func(trace *Trace) {
			trace.Events[0], trace.Events[1] = trace.Events[1], trace.Events[0]
			trace.Events[0].Sequence, trace.Events[1].Sequence = 1, 2
		}, StatusFail, ReasonEventOrder},
		{"retry exceeded", func(trace *Trace) {
			retry := 1
			trace.Events = append(trace.Events, Event{
				Sequence: 3, Event: EventToolCall, Capability: CapabilityMemory,
				Operation: OperationRecall, RetryOf: &retry,
			})
		}, StatusFail, ReasonRetryLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trace := base
			trace.Observed = append([]Observation{}, base.Observed...)
			trace.Events = append([]Event{}, base.Events...)
			tt.mutate(&trace)
			got := ValidateScenario(scenario, trace, "1.0.0")
			if got.Status != tt.status || !containsReason(got.Reasons, tt.reason) {
				t.Fatalf("result = %#v, want %s/%s", got, tt.status, tt.reason)
			}
		})
	}
}

func TestValidateScenarioRejectsIdentityMismatch(t *testing.T) {
	scenario := Scenario{ID: "RECALL-001"}
	trace := Trace{ContractVersion: "2.0.0", ScenarioID: "RECALL-001"}
	got := ValidateScenario(scenario, trace, "1.0.0")
	if got.Status != StatusError || !containsReason(got.Reasons, ReasonContractVersion) {
		t.Fatalf("contract result = %#v", got)
	}
	trace.ContractVersion = "1.0.0"
	trace.ScenarioID = "RECALL-002"
	got = ValidateScenario(scenario, trace, "1.0.0")
	if got.Status != StatusError || !containsReason(got.Reasons, ReasonScenarioUnknown) {
		t.Fatalf("scenario result = %#v", got)
	}
}

func containsReason(reasons []ReasonCode, want ReasonCode) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}
