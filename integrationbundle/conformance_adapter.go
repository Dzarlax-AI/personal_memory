package integrationbundle

import (
	"fmt"
	"reflect"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

// ConformanceTrace derives a normalized artifact-conformance trace from the
// validated bundle policy, scenario binding, and rendered client adapter. It
// does not execute a model or connect to any client, tool, or endpoint.
func (b *Bundle) ConformanceTrace(request conformance.AdapterRequest) (conformance.Trace, error) {
	if b == nil {
		return conformance.Trace{}, fmt.Errorf("bundle must not be nil")
	}
	if err := validatePolicy(&b.policy, &b.manifest, &b.suite); err != nil {
		return conformance.Trace{}, fmt.Errorf("bundle policy is no longer canonical")
	}
	if request.SchemaVersion != conformance.CurrentSchemaVersion || request.ContractVersion != b.manifest.ContractVersion {
		return conformance.Trace{}, fmt.Errorf("adapter request version identity mismatch")
	}
	if !bundleClientSupported(b.manifest.Clients, request.ClientFamily) {
		return conformance.Trace{}, fmt.Errorf("adapter request client identity mismatch")
	}
	scenario, ok := bundleScenario(b.suite.Scenarios, request.ScenarioID)
	if !ok || request.IntentClass != scenario.IntentClass || request.SyntheticInput != scenario.SyntheticInput || !reflect.DeepEqual(request.Capabilities, scenario.Capabilities) {
		return conformance.Trace{}, fmt.Errorf("adapter request scenario identity mismatch")
	}
	mapping, ok := scenarioMapping(b.policy.ScenarioMappings, scenario.ID)
	if !ok {
		return conformance.Trace{}, fmt.Errorf("adapter request policy identity mismatch")
	}
	sets, err := b.Render(capabilityConfigForTrace(scenario.Capabilities))
	if err != nil {
		return conformance.Trace{}, fmt.Errorf("render adapter semantics: %w", err)
	}
	set, ok := artifactSetForClient(sets, request.ClientFamily)
	if !ok || set.CapabilityConfig != capabilityConfigForTrace(scenario.Capabilities) {
		return conformance.Trace{}, fmt.Errorf("rendered adapter identity mismatch")
	}
	for _, path := range canonicalInstructionPaths[request.ClientFamily] {
		artifact, found := artifactByPath(set.Artifacts, path)
		if !found {
			return conformance.Trace{}, fmt.Errorf("rendered adapter policy missing")
		}
		renderedPolicy, err := embeddedCanonicalPolicy(artifact.Content)
		if err != nil || !reflect.DeepEqual(renderedPolicy, b.policy) {
			return conformance.Trace{}, fmt.Errorf("rendered adapter policy mismatch")
		}
	}
	return conformance.Trace{
		SchemaVersion: conformance.CurrentSchemaVersion, ContractVersion: b.manifest.ContractVersion,
		ScenarioID: scenario.ID, ClientFamily: request.ClientFamily,
		Observed: append([]conformance.Observation(nil), mapping.TraceRecipe.Observed...),
		Events:   append([]conformance.Event(nil), mapping.TraceRecipe.Events...),
	}, nil
}

func bundleClientSupported(clients []ClientManifest, family conformance.ClientFamily) bool {
	for _, client := range clients {
		if client.ID == family {
			return true
		}
	}
	return false
}

func bundleScenario(scenarios []conformance.Scenario, id string) (conformance.Scenario, bool) {
	for _, scenario := range scenarios {
		if scenario.ID == id {
			return scenario, true
		}
	}
	return conformance.Scenario{}, false
}

func scenarioMapping(mappings []ScenarioMapping, id string) (ScenarioMapping, bool) {
	for _, mapping := range mappings {
		if mapping.ScenarioID == id && len(mapping.PolicyRefs) != 0 {
			return mapping, true
		}
	}
	return ScenarioMapping{}, false
}

func artifactByPath(artifacts []Artifact, path string) (Artifact, bool) {
	for _, artifact := range artifacts {
		if artifact.Path == path {
			return artifact, true
		}
	}
	return Artifact{}, false
}

func capabilityConfigForTrace(capabilities map[conformance.Capability]conformance.CapabilityState) CapabilityConfig {
	return CapabilityConfig{
		Memory:    traceCapabilityState(capabilities[conformance.CapabilityMemory]),
		Documents: traceCapabilityState(capabilities[conformance.CapabilityDocuments]),
		Todoist:   traceCapabilityState(capabilities[conformance.CapabilityTodoist]),
	}
}

func traceCapabilityState(state conformance.CapabilityState) CapabilityState {
	switch state {
	case conformance.CapabilityAvailable:
		return CapabilityAvailable
	case conformance.CapabilityDisabled:
		return CapabilityDisabled
	default:
		return CapabilityUnavailable
	}
}

func artifactSetForClient(sets []ArtifactSet, family conformance.ClientFamily) (ArtifactSet, bool) {
	for _, set := range sets {
		if set.ClientID == family {
			return set, true
		}
	}
	return ArtifactSet{}, false
}
