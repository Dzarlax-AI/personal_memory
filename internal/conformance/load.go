package conformance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const maxInputBytes = 8 << 20

var (
	safeScenarioID  = regexp.MustCompile(`^[A-Z]+-[0-9]{3}$`)
	safeToken       = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	semver          = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	contractVersion = regexp.MustCompile(`^Contract version:\s+\*\*([0-9]+\.[0-9]+\.[0-9]+)\*\*\s*$`)
	contractID      = regexp.MustCompile("`([A-Z]+-[0-9]{3})`")
)

func LoadSuite(reader io.Reader) (*Suite, error) {
	var suite Suite
	if err := decodeStrict(reader, &suite); err != nil {
		return nil, fmt.Errorf("decode conformance suite: %w", err)
	}
	if err := validateSuite(&suite); err != nil {
		return nil, err
	}
	return &suite, nil
}

func LoadTraceBundle(reader io.Reader) (*TraceBundle, error) {
	var bundle TraceBundle
	if err := decodeStrict(reader, &bundle); err != nil {
		return nil, fmt.Errorf("decode trace bundle: %w", err)
	}
	if bundle.SchemaVersion != CurrentSchemaVersion {
		return nil, fmt.Errorf("trace bundle schema_version must be %d", CurrentSchemaVersion)
	}
	if !semver.MatchString(bundle.ContractVersion) {
		return nil, fmt.Errorf("trace bundle contract_version must be semantic version")
	}
	if bundle.Traces == nil {
		return nil, fmt.Errorf("trace bundle traces must be an array")
	}
	seen := make(map[string]struct{}, len(bundle.Traces))
	for i := range bundle.Traces {
		if err := validateTrace(&bundle.Traces[i], bundle.ContractVersion); err != nil {
			return nil, fmt.Errorf("trace %d: %w", i, err)
		}
		key := string(bundle.Traces[i].ClientFamily) + "\x00" + bundle.Traces[i].ScenarioID
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate trace for client %q scenario %q",
				bundle.Traces[i].ClientFamily, bundle.Traces[i].ScenarioID)
		}
		seen[key] = struct{}{}
	}
	return &bundle, nil
}

func DecodeTrace(data []byte) (Trace, error) {
	var trace Trace
	if err := decodeStrict(bytes.NewReader(data), &trace); err != nil {
		return Trace{}, fmt.Errorf("decode trace: %w", err)
	}
	if err := validateTrace(&trace, trace.ContractVersion); err != nil {
		return Trace{}, err
	}
	return trace, nil
}

func LoadContractCatalog(reader io.Reader) (ContractCatalog, error) {
	data, err := readLimited(reader)
	if err != nil {
		return ContractCatalog{}, fmt.Errorf("read contract: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), maxInputBytes)
	var (
		version     string
		inScenarios bool
		foundStart  bool
		ids         []string
		seen        = map[string]struct{}{}
	)
	for scanner.Scan() {
		line := scanner.Text()
		if version == "" {
			if match := contractVersion.FindStringSubmatch(line); len(match) == 2 {
				version = match[1]
			}
		}
		switch line {
		case "## Conformance scenarios":
			inScenarios = true
			foundStart = true
			continue
		case "## Contract evolution":
			inScenarios = false
		}
		if !inScenarios {
			continue
		}
		for _, match := range contractID.FindAllStringSubmatch(line, -1) {
			id := match[1]
			if _, duplicate := seen[id]; duplicate {
				return ContractCatalog{}, fmt.Errorf("contract contains duplicate scenario ID %q", id)
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if err := scanner.Err(); err != nil {
		return ContractCatalog{}, fmt.Errorf("read contract: %w", err)
	}
	if version == "" {
		return ContractCatalog{}, fmt.Errorf("contract version not found")
	}
	if !foundStart {
		return ContractCatalog{}, fmt.Errorf("conformance scenarios section not found")
	}
	if len(ids) == 0 {
		return ContractCatalog{}, fmt.Errorf("contract contains no conformance scenario IDs")
	}
	sort.Strings(ids)
	return ContractCatalog{Version: version, ScenarioIDs: ids}, nil
}

func ValidateCoverage(suite *Suite, catalog ContractCatalog) error {
	if suite.ContractVersion != catalog.Version {
		return fmt.Errorf("suite contract version %q does not match contract %q",
			suite.ContractVersion, catalog.Version)
	}
	suiteIDs := make([]string, len(suite.Scenarios))
	for i := range suite.Scenarios {
		suiteIDs[i] = suite.Scenarios[i].ID
	}
	sort.Strings(suiteIDs)
	if len(suiteIDs) != len(catalog.ScenarioIDs) {
		return fmt.Errorf("suite covers %d scenarios; contract publishes %d", len(suiteIDs), len(catalog.ScenarioIDs))
	}
	for i := range suiteIDs {
		if suiteIDs[i] != catalog.ScenarioIDs[i] {
			return fmt.Errorf("suite scenario coverage differs at %q and %q", suiteIDs[i], catalog.ScenarioIDs[i])
		}
	}
	return nil
}

func decodeStrict(reader io.Reader, target any) error {
	data, err := readLimited(reader)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("input contains trailing JSON")
		}
		return err
	}
	return nil
}

func readLimited(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxInputBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxInputBytes)
	}
	return data, nil
}

func validateSuite(suite *Suite) error {
	if suite.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("suite schema_version must be %d", CurrentSchemaVersion)
	}
	if !semver.MatchString(suite.ContractVersion) {
		return fmt.Errorf("suite contract_version must be semantic version")
	}
	if !semver.MatchString(suite.SuiteVersion) {
		return fmt.Errorf("suite suite_version must be semantic version")
	}
	if suite.Scenarios == nil || len(suite.Scenarios) == 0 {
		return fmt.Errorf("suite scenarios must be a non-empty array")
	}
	seen := make(map[string]struct{}, len(suite.Scenarios))
	for i := range suite.Scenarios {
		scenario := &suite.Scenarios[i]
		if err := validateScenario(scenario); err != nil {
			return fmt.Errorf("scenario %d: %w", i, err)
		}
		if _, duplicate := seen[scenario.ID]; duplicate {
			return fmt.Errorf("duplicate scenario ID %q", scenario.ID)
		}
		seen[scenario.ID] = struct{}{}
	}
	return nil
}

func validateScenario(scenario *Scenario) error {
	if !safeScenarioID.MatchString(scenario.ID) {
		return fmt.Errorf("scenario ID %q is invalid", scenario.ID)
	}
	if !safeToken.MatchString(scenario.IntentClass) {
		return fmt.Errorf("scenario %q intent_class must be a normalized token", scenario.ID)
	}
	if strings.TrimSpace(scenario.SyntheticInput) == "" {
		return fmt.Errorf("scenario %q synthetic_input must not be empty", scenario.ID)
	}
	if scenario.Capabilities == nil {
		return fmt.Errorf("scenario %q capabilities must be an object", scenario.ID)
	}
	for _, capability := range []Capability{CapabilityMemory, CapabilityDocuments, CapabilityTodoist} {
		state, exists := scenario.Capabilities[capability]
		if !exists {
			return fmt.Errorf("scenario %q capability %q is required", scenario.ID, capability)
		}
		if !validCapabilityState(state) {
			return fmt.Errorf("scenario %q capability %q has invalid state", scenario.ID, capability)
		}
	}
	if len(scenario.Capabilities) != 3 {
		return fmt.Errorf("scenario %q capabilities contain unknown key", scenario.ID)
	}
	if scenario.RequiredObservations == nil || len(scenario.RequiredObservations) == 0 {
		return fmt.Errorf("scenario %q required_observations must be non-empty", scenario.ID)
	}
	seenObservations := map[Observation]struct{}{}
	for _, observation := range scenario.RequiredObservations {
		if !validObservation(observation) {
			return fmt.Errorf("scenario %q has invalid observation %q", scenario.ID, observation)
		}
		if _, duplicate := seenObservations[observation]; duplicate {
			return fmt.Errorf("scenario %q has duplicate observation %q", scenario.ID, observation)
		}
		seenObservations[observation] = struct{}{}
	}
	if scenario.Assertions.Must == nil || scenario.Assertions.MustNot == nil {
		return fmt.Errorf("scenario %q assertions must and must_not must be arrays", scenario.ID)
	}
	for _, pattern := range append(
		append([]EventPattern{}, scenario.Assertions.Must...),
		scenario.Assertions.MustNot...,
	) {
		if err := validatePattern(pattern); err != nil {
			return fmt.Errorf("scenario %q: %w", scenario.ID, err)
		}
		if err := requirePatternObservation(scenario.ID, pattern, seenObservations); err != nil {
			return err
		}
	}
	for _, count := range scenario.Assertions.Counts {
		if err := validatePattern(count.Pattern); err != nil {
			return fmt.Errorf("scenario %q count: %w", scenario.ID, err)
		}
		if err := requirePatternObservation(scenario.ID, count.Pattern, seenObservations); err != nil {
			return err
		}
		if count.Min == nil && count.Max == nil {
			return fmt.Errorf("scenario %q count assertion requires min or max", scenario.ID)
		}
		if count.Min != nil && *count.Min < 0 || count.Max != nil && *count.Max < 0 {
			return fmt.Errorf("scenario %q count bounds must be non-negative", scenario.ID)
		}
		if count.Min != nil && count.Max != nil && *count.Min > *count.Max {
			return fmt.Errorf("scenario %q count min exceeds max", scenario.ID)
		}
	}
	for _, order := range scenario.Assertions.Ordered {
		if err := validatePattern(order.Before); err != nil {
			return fmt.Errorf("scenario %q order before: %w", scenario.ID, err)
		}
		if err := requirePatternObservation(scenario.ID, order.Before, seenObservations); err != nil {
			return err
		}
		if err := validatePattern(order.After); err != nil {
			return fmt.Errorf("scenario %q order after: %w", scenario.ID, err)
		}
		if err := requirePatternObservation(scenario.ID, order.After, seenObservations); err != nil {
			return err
		}
	}
	if scenario.Assertions.MaxRetries != nil && *scenario.Assertions.MaxRetries < 0 {
		return fmt.Errorf("scenario %q max_retries must be non-negative", scenario.ID)
	}
	if scenario.Assertions.MaxRetries != nil {
		if _, exists := seenObservations[ObservationRetryRelations]; !exists {
			return fmt.Errorf("scenario %q assertions require observation %q", scenario.ID, ObservationRetryRelations)
		}
	}
	return nil
}

func requirePatternObservation(
	scenarioID string,
	pattern EventPattern,
	observed map[Observation]struct{},
) error {
	var required Observation
	switch pattern.Event {
	case EventCapability:
		required = ObservationCapabilities
	case EventToolCall, EventToolResult:
		required = ObservationToolEvents
	case EventDisclosure, EventClaim, EventFallback:
		required = ObservationUserVisibleClaims
	case EventArtifact:
		required = ObservationArtifacts
	}
	if _, exists := observed[required]; !exists {
		return fmt.Errorf("scenario %q assertions require observation %q", scenarioID, required)
	}
	return nil
}

func validateTrace(trace *Trace, contract string) error {
	if trace.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("trace schema_version must be %d", CurrentSchemaVersion)
	}
	if trace.ContractVersion != contract || !semver.MatchString(trace.ContractVersion) {
		return fmt.Errorf("trace contract_version is invalid or inconsistent")
	}
	if !safeScenarioID.MatchString(trace.ScenarioID) {
		return fmt.Errorf("trace scenario_id is invalid")
	}
	if !validClientFamily(trace.ClientFamily) {
		return fmt.Errorf("trace client_family is invalid")
	}
	if trace.Observed == nil {
		return fmt.Errorf("trace observed must be an array")
	}
	observed := map[Observation]struct{}{}
	for _, observation := range trace.Observed {
		if !validObservation(observation) {
			return fmt.Errorf("trace observation %q is invalid", observation)
		}
		if _, duplicate := observed[observation]; duplicate {
			return fmt.Errorf("trace observation %q is duplicated", observation)
		}
		observed[observation] = struct{}{}
	}
	if trace.Events == nil {
		return fmt.Errorf("trace events must be an array")
	}
	sequences := map[int]Event{}
	previous := 0
	for i, event := range trace.Events {
		if event.Sequence <= previous {
			return fmt.Errorf("trace event %d sequence must be strictly increasing", i)
		}
		if _, duplicate := sequences[event.Sequence]; duplicate {
			return fmt.Errorf("trace event sequence %d is duplicated", event.Sequence)
		}
		if err := validateEvent(event); err != nil {
			return fmt.Errorf("trace event %d: %w", i, err)
		}
		if event.RetryOf != nil {
			if event.Event != EventToolCall {
				return fmt.Errorf("trace event %d retry_of is valid only for tool_call", i)
			}
			if *event.RetryOf >= event.Sequence {
				return fmt.Errorf("trace event %d retry_of must reference an earlier sequence", i)
			}
			original, exists := sequences[*event.RetryOf]
			if !exists {
				return fmt.Errorf("trace event %d retry_of references unknown sequence", i)
			}
			if original.Event != EventToolCall ||
				original.Capability != event.Capability || original.Operation != event.Operation {
				return fmt.Errorf("trace event %d retry_of must reference the same tool operation", i)
			}
		}
		sequences[event.Sequence] = event
		previous = event.Sequence
	}
	return nil
}

func validatePattern(pattern EventPattern) error {
	if !validEventKind(pattern.Event) {
		return fmt.Errorf("event pattern kind %q is invalid", pattern.Event)
	}
	return validateEventFields(pattern.Event, pattern.Capability, pattern.Operation, pattern.Outcome, pattern.Code)
}

func validateEvent(event Event) error {
	if event.Sequence <= 0 {
		return fmt.Errorf("sequence must be positive")
	}
	if !validEventKind(event.Event) {
		return fmt.Errorf("kind %q is invalid", event.Event)
	}
	return validateEventFields(event.Event, event.Capability, event.Operation, event.Outcome, event.Code)
}

func validateEventFields(kind EventKind, capability Capability, operation Operation, outcome Outcome, code EventCode) error {
	if capability != "" && !validCapability(capability) {
		return fmt.Errorf("capability %q is invalid", capability)
	}
	if operation != "" && !validOperation(operation) {
		return fmt.Errorf("operation %q is invalid", operation)
	}
	if outcome != "" && !validOutcome(outcome) {
		return fmt.Errorf("outcome %q is invalid", outcome)
	}
	if code != "" && !validEventCode(code) {
		return fmt.Errorf("code %q is invalid", code)
	}
	switch kind {
	case EventCapability:
		if capability == "" || outcome == "" || operation != "" || code != "" {
			return fmt.Errorf("capability event requires capability and outcome only")
		}
	case EventToolCall:
		if capability == "" || operation == "" || outcome != "" || code != "" {
			return fmt.Errorf("tool_call event requires capability and operation only")
		}
	case EventToolResult:
		if capability == "" || operation == "" || outcome == "" || code != "" {
			return fmt.Errorf("tool_result event requires capability, operation, and outcome")
		}
	case EventDisclosure, EventClaim, EventArtifact:
		if code == "" || capability != "" || operation != "" || outcome != "" {
			return fmt.Errorf("%s event requires code only", kind)
		}
	case EventFallback:
		if capability != CapabilityOrdinaryContext || operation != OperationFallback || outcome == "" || code != "" {
			return fmt.Errorf("fallback event requires ordinary_context, fallback, and outcome")
		}
	}
	return nil
}

func validClientFamily(value ClientFamily) bool {
	switch value {
	case ClientCodex, ClientClaude, ClientChatGPT, ClientGenericMCP, ClientSynthetic:
		return true
	default:
		return false
	}
}

func validCapability(value Capability) bool {
	switch value {
	case CapabilityMemory, CapabilityDocuments, CapabilityTodoist, CapabilityOrdinaryContext:
		return true
	default:
		return false
	}
}

func validCapabilityState(value CapabilityState) bool {
	switch value {
	case CapabilityAvailable, CapabilityDisabled, CapabilityUnavailable, CapabilityTimeout:
		return true
	default:
		return false
	}
}

func validObservation(value Observation) bool {
	switch value {
	case ObservationCapabilities, ObservationToolEvents, ObservationUserVisibleClaims,
		ObservationRetryRelations, ObservationArtifacts:
		return true
	default:
		return false
	}
}

func validEventKind(value EventKind) bool {
	switch value {
	case EventCapability, EventToolCall, EventToolResult, EventDisclosure, EventClaim, EventFallback, EventArtifact:
		return true
	default:
		return false
	}
}

func validOperation(value Operation) bool {
	switch value {
	case OperationRecall, OperationSearch, OperationStore, OperationTaskCreate,
		OperationTaskUpdate, OperationTaskComplete, OperationTaskDelete,
		OperationLifecycle, OperationFallback:
		return true
	default:
		return false
	}
}

func validOutcome(value Outcome) bool {
	switch value {
	case OutcomeSuccess, OutcomeEmpty, OutcomeDuplicate, OutcomeUnavailable,
		OutcomeTimeout, OutcomeRejected, OutcomeAmbiguous, OutcomeError:
		return true
	default:
		return false
	}
}

func validEventCode(value EventCode) bool {
	switch value {
	case CodeMemoryNotChecked, CodeDocumentsNotSearched, CodeTaskNotCreated,
		CodeTaskCreated, CodeWriteConfirmed, CodeWriteDuplicate, CodeWriteUnconfirmed,
		CodeFactFound, CodeNoRelevantFact, CodeDocumentEvidence, CodeDocumentRouted, CodePreferenceInvented,
		CodeCurrentFactUsed, CodeHistoricalFactUsed, CodeLifecycleUncertain,
		CodeSimilarityContradiction, CodeExplicitLifecycleChange, CodeUnverifiedFact,
		CodeCurrentInstructionUsed, CodeSecretRejected, CodeClarificationRequested,
		CodeOrdinaryResponse, CodeTelemetryAllowed, CodeTelemetryDisabled,
		CodeSensitiveDataCaptured:
		return true
	default:
		return false
	}
}
