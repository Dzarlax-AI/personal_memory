package conformance

// CurrentSchemaVersion is the supported normalized suite, trace, and report schema.
const CurrentSchemaVersion = 1

// ClientFamily identifies a supported client trace producer.
type ClientFamily string

const (
	ClientCodex      ClientFamily = "codex"
	ClientClaude     ClientFamily = "claude"
	ClientChatGPT    ClientFamily = "chatgpt"
	ClientGenericMCP ClientFamily = "generic_mcp"
	ClientSynthetic  ClientFamily = "synthetic"
)

// Capability identifies an optional client capability observed by a scenario.
type Capability string

const (
	CapabilityMemory          Capability = "memory"
	CapabilityDocuments       Capability = "documents"
	CapabilityTodoist         Capability = "todoist"
	CapabilityOrdinaryContext Capability = "ordinary_context"
)

// CapabilityState describes whether a capability can be used in a scenario.
type CapabilityState string

const (
	CapabilityAvailable   CapabilityState = "available"
	CapabilityDisabled    CapabilityState = "disabled"
	CapabilityUnavailable CapabilityState = "unavailable"
	CapabilityTimeout     CapabilityState = "timeout"
)

// Observation identifies a privacy-safe category present in a trace.
type Observation string

const (
	ObservationCapabilities      Observation = "capabilities"
	ObservationToolEvents        Observation = "tool_events"
	ObservationUserVisibleClaims Observation = "user_visible_claims"
	ObservationRetryRelations    Observation = "retry_relationships"
	ObservationArtifacts         Observation = "artifacts"
)

// EventKind identifies a normalized observable event.
type EventKind string

const (
	EventCapability EventKind = "capability"
	EventToolCall   EventKind = "tool_call"
	EventToolResult EventKind = "tool_result"
	EventDisclosure EventKind = "disclosure"
	EventClaim      EventKind = "claim"
	EventFallback   EventKind = "fallback"
	EventArtifact   EventKind = "artifact"
)

// Operation identifies a normalized tool operation.
type Operation string

const (
	OperationRecall       Operation = "recall"
	OperationSearch       Operation = "search"
	OperationStore        Operation = "store"
	OperationTaskList     Operation = "task_list"
	OperationTaskCreate   Operation = "task_create"
	OperationTaskUpdate   Operation = "task_update"
	OperationTaskComplete Operation = "task_complete"
	OperationTaskDelete   Operation = "task_delete"
	OperationLifecycle    Operation = "lifecycle"
	OperationFallback     Operation = "fallback"
)

// Outcome identifies a normalized capability or tool result.
type Outcome string

const (
	OutcomeSuccess     Outcome = "success"
	OutcomeEmpty       Outcome = "empty"
	OutcomeDuplicate   Outcome = "duplicate"
	OutcomeUnavailable Outcome = "unavailable"
	OutcomeTimeout     Outcome = "timeout"
	OutcomeRejected    Outcome = "rejected"
	OutcomeAmbiguous   Outcome = "ambiguous"
	OutcomeError       Outcome = "error"
)

// EventCode identifies a privacy-safe user-visible or artifact event.
type EventCode string

const (
	CodeMemoryNotChecked        EventCode = "memory_not_checked"
	CodeDocumentsNotSearched    EventCode = "documents_not_searched"
	CodeTaskNotCreated          EventCode = "task_not_created"
	CodeTaskCreated             EventCode = "task_created"
	CodeWriteConfirmed          EventCode = "write_confirmed"
	CodeWriteDuplicate          EventCode = "write_duplicate"
	CodeWriteUnconfirmed        EventCode = "write_unconfirmed"
	CodeFactFound               EventCode = "fact_found"
	CodeNoRelevantFact          EventCode = "no_relevant_fact"
	CodeDocumentEvidence        EventCode = "document_evidence"
	CodeDocumentRouted          EventCode = "document_routed"
	CodePreferenceInvented      EventCode = "preference_invented"
	CodeCurrentFactUsed         EventCode = "current_fact_used"
	CodeHistoricalFactUsed      EventCode = "historical_fact_used"
	CodeLifecycleUncertain      EventCode = "lifecycle_uncertain"
	CodeSimilarityContradiction EventCode = "similarity_contradiction"
	CodeExplicitLifecycleChange EventCode = "explicit_lifecycle_change"
	CodeFactVerified            EventCode = "fact_verified"
	CodeUnverifiedFact          EventCode = "unverified_fact"
	CodeCurrentInstructionUsed  EventCode = "current_instruction_used"
	CodeSecretRejected          EventCode = "secret_rejected"
	CodeClarificationRequested  EventCode = "clarification_requested"
	CodeOrdinaryResponse        EventCode = "ordinary_response"
	CodeTelemetryAllowed        EventCode = "telemetry_allowed"
	CodeTelemetryDisabled       EventCode = "telemetry_disabled"
	CodeSensitiveDataCaptured   EventCode = "sensitive_data_captured"
)

// EventPattern selects normalized events for an assertion.
type EventPattern struct {
	Event      EventKind  `json:"event"`
	Capability Capability `json:"capability,omitempty"`
	Operation  Operation  `json:"operation,omitempty"`
	Outcome    Outcome    `json:"outcome,omitempty"`
	Code       EventCode  `json:"code,omitempty"`
}

// CountAssertion constrains the number of matching events.
type CountAssertion struct {
	Pattern EventPattern `json:"pattern"`
	Min     *int         `json:"min,omitempty"`
	Max     *int         `json:"max,omitempty"`
}

// OrderAssertion requires one matching event to precede another.
type OrderAssertion struct {
	Before EventPattern `json:"before"`
	After  EventPattern `json:"after"`
}

// AssertionAlternative describes one conforming branch of an any_of assertion.
type AssertionAlternative struct {
	Must    []EventPattern   `json:"must"`
	Ordered []OrderAssertion `json:"ordered,omitempty"`
}

// Assertions contains the behavioral requirements for a scenario.
type Assertions struct {
	Must       []EventPattern         `json:"must"`
	MustNot    []EventPattern         `json:"must_not"`
	AnyOf      []AssertionAlternative `json:"any_of,omitempty"`
	Counts     []CountAssertion       `json:"counts,omitempty"`
	Ordered    []OrderAssertion       `json:"ordered,omitempty"`
	MaxRetries *int                   `json:"max_retries,omitempty"`
}

// Scenario defines one synthetic conformance case.
type Scenario struct {
	ID                   string                         `json:"id"`
	IntentClass          string                         `json:"intent_class"`
	SyntheticInput       string                         `json:"synthetic_input"`
	Capabilities         map[Capability]CapabilityState `json:"capabilities"`
	RequiredObservations []Observation                  `json:"required_observations"`
	Assertions           Assertions                     `json:"assertions"`
}

// Suite is a versioned collection of conformance scenarios.
type Suite struct {
	SchemaVersion   int        `json:"schema_version"`
	ContractVersion string     `json:"contract_version"`
	SuiteVersion    string     `json:"suite_version"`
	Scenarios       []Scenario `json:"scenarios"`
}

// Event is one normalized, privacy-safe observation in a trace.
type Event struct {
	Sequence   int        `json:"sequence"`
	Event      EventKind  `json:"event"`
	Capability Capability `json:"capability,omitempty"`
	Operation  Operation  `json:"operation,omitempty"`
	Outcome    Outcome    `json:"outcome,omitempty"`
	Code       EventCode  `json:"code,omitempty"`
	RetryOf    *int       `json:"retry_of,omitempty"`
}

// Trace contains the observations for one client and scenario.
type Trace struct {
	SchemaVersion   int           `json:"schema_version"`
	ContractVersion string        `json:"contract_version"`
	ScenarioID      string        `json:"scenario_id"`
	ClientFamily    ClientFamily  `json:"client_family"`
	Observed        []Observation `json:"observed"`
	Events          []Event       `json:"events"`
}

// TraceBundle groups normalized traces under one contract version.
type TraceBundle struct {
	SchemaVersion   int     `json:"schema_version"`
	ContractVersion string  `json:"contract_version"`
	Traces          []Trace `json:"traces"`
}

// ContractCatalog contains the stable scenario IDs published by the contract.
type ContractCatalog struct {
	Version     string
	ScenarioIDs []string
}
