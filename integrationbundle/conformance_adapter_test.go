package integrationbundle

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

func TestConformanceTracePassesEveryPublicScenarioForEveryClient(t *testing.T) {
	bundle := loadTestBundle(t)
	suite := mustLoadSuite(t)
	for _, client := range bundle.ClientIDs() {
		client := client
		t.Run(string(client), func(t *testing.T) {
			for _, scenario := range suite.Scenarios {
				request := adapterRequest(client, suite.ContractVersion, scenario)
				trace, err := bundle.ConformanceTrace(request)
				if err != nil {
					t.Fatalf("%s: %v", scenario.ID, err)
				}
				encoded, err := json.Marshal(trace)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := conformance.DecodeTrace(encoded); err != nil {
					t.Fatalf("%s: normalized trace is invalid: %v", scenario.ID, err)
				}
				if result := conformance.ValidateScenario(scenario, trace, suite.ContractVersion); result.Status != conformance.StatusPass {
					t.Fatalf("%s: status=%s reasons=%v trace=%+v", scenario.ID, result.Status, result.Reasons, trace)
				}
			}
		})
	}
}

func TestConformanceTraceRequiresExactRequestIdentity(t *testing.T) {
	bundle := loadTestBundle(t)
	suite := mustLoadSuite(t)
	base := adapterRequest(conformance.ClientCodex, suite.ContractVersion, suite.Scenarios[0])

	cases := map[string]func(*conformance.AdapterRequest){
		"schema":   func(r *conformance.AdapterRequest) { r.SchemaVersion++ },
		"contract": func(r *conformance.AdapterRequest) { r.ContractVersion = "9.9.9" },
		"client":   func(r *conformance.AdapterRequest) { r.ClientFamily = conformance.ClientSynthetic },
		"scenario": func(r *conformance.AdapterRequest) { r.ScenarioID = "UNKNOWN-999" },
		"intent":   func(r *conformance.AdapterRequest) { r.IntentClass = "other" },
		"input":    func(r *conformance.AdapterRequest) { r.SyntheticInput = "arbitrary prompt" },
		"capabilities": func(r *conformance.AdapterRequest) {
			r.Capabilities[conformance.CapabilityMemory] = conformance.CapabilityDisabled
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			request := cloneAdapterRequest(base)
			mutate(&request)
			if _, err := bundle.ConformanceTrace(request); err == nil {
				t.Fatal("expected identity rejection")
			}
		})
	}
}

func TestConformanceTraceIsDeterministicAndContainsNoArbitraryContent(t *testing.T) {
	bundle := loadTestBundle(t)
	suite := mustLoadSuite(t)
	request := adapterRequest(conformance.ClientClaude, suite.ContractVersion, suite.Scenarios[3])
	one, err := bundle.ConformanceTrace(request)
	if err != nil {
		t.Fatal(err)
	}
	two, err := bundle.ConformanceTrace(request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one, two) {
		t.Fatalf("trace changed between calls:\n%+v\n%+v", one, two)
	}
	encoded, err := json.Marshal(one)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(request.SyntheticInput)) || bytes.Contains(encoded, []byte(request.IntentClass)) {
		t.Fatalf("trace leaked arbitrary request content: %s", encoded)
	}
}

func TestConformanceTraceDoesNotReadSuiteExpectedResults(t *testing.T) {
	bundle := loadTestBundle(t)
	scenario := bundle.suite.Scenarios[0]
	request := adapterRequest(conformance.ClientCodex, bundle.manifest.ContractVersion, scenario)
	want, err := bundle.ConformanceTrace(request)
	if err != nil {
		t.Fatal(err)
	}
	bundle.suite.Scenarios[0].RequiredObservations = []conformance.Observation{conformance.ObservationArtifacts}
	bundle.suite.Scenarios[0].Assertions = conformance.Assertions{
		Must: []conformance.EventPattern{{Event: conformance.EventArtifact, Code: conformance.CodeTelemetryDisabled}},
	}
	got, err := bundle.ConformanceTrace(request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("suite expected results influenced recipe trace:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestScenarioRecipeValidationRejectsMissingAndIncoherentRecipes(t *testing.T) {
	bundle := loadTestBundle(t)
	suite := mustLoadSuite(t)
	for name, mutate := range map[string]func(*Policy){
		"missing": func(policy *Policy) { policy.ScenarioMappings[0].TraceRecipe = TraceRecipe{} },
		"unknown enum": func(policy *Policy) {
			policy.ScenarioMappings[0].TraceRecipe.Events[0].Event = conformance.EventKind("arbitrary")
		},
		"capability state": func(policy *Policy) {
			policy.ScenarioMappings[0].TraceRecipe.Events[0].Capability = conformance.CapabilityTodoist
		},
		"unpaired result": func(policy *Policy) {
			policy.ScenarioMappings[0].TraceRecipe.Events = policy.ScenarioMappings[0].TraceRecipe.Events[1:]
		},
		"unauthorized operation": func(policy *Policy) {
			policy.ScenarioMappings[0].TraceRecipe.Events[0].Operation = conformance.OperationTaskDelete
		},
		"unauthorized disclosure": func(policy *Policy) {
			policy.ScenarioMappings[0].TraceRecipe.Events[2].Code = conformance.CodeSecretRejected
		},
	} {
		t.Run(name, func(t *testing.T) {
			policy := bundle.Policy()
			mutate(&policy)
			if err := validateScenarioMappings(&policy, suite); err == nil {
				t.Fatal("expected recipe validation error")
			}
		})
	}
}

func TestConformanceTraceRevalidatesCanonicalPolicyBeforeRecipeUse(t *testing.T) {
	bundle := loadTestBundle(t)
	scenario := bundle.suite.Scenarios[0]
	request := adapterRequest(conformance.ClientCodex, bundle.manifest.ContractVersion, scenario)
	bundle.policy.ScenarioMappings[0].TraceRecipe.Events[2].Code = conformance.CodeSecretRejected
	if _, err := bundle.ConformanceTrace(request); err == nil {
		t.Fatal("expected mutated policy recipe to be rejected")
	}
}

func adapterRequest(client conformance.ClientFamily, contract string, scenario conformance.Scenario) conformance.AdapterRequest {
	capabilities := make(map[conformance.Capability]conformance.CapabilityState, len(scenario.Capabilities))
	for capability, state := range scenario.Capabilities {
		capabilities[capability] = state
	}
	return conformance.AdapterRequest{
		SchemaVersion: conformance.CurrentSchemaVersion, ContractVersion: contract,
		ClientFamily: client, ScenarioID: scenario.ID, IntentClass: scenario.IntentClass,
		SyntheticInput: scenario.SyntheticInput, Capabilities: capabilities,
	}
}

func cloneAdapterRequest(request conformance.AdapterRequest) conformance.AdapterRequest {
	return adapterRequest(request.ClientFamily, request.ContractVersion, conformance.Scenario{
		ID: request.ScenarioID, IntentClass: request.IntentClass,
		SyntheticInput: request.SyntheticInput, Capabilities: request.Capabilities,
	})
}
