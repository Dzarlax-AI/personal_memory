package conformance

const CurrentSchemaVersion = 1

type ClientFamily string

const (
	ClientCodex      ClientFamily = "codex"
	ClientClaude     ClientFamily = "claude"
	ClientChatGPT    ClientFamily = "chatgpt"
	ClientGenericMCP ClientFamily = "generic_mcp"
	ClientSynthetic  ClientFamily = "synthetic"
)

type Capability string

const (
	CapabilityMemory          Capability = "memory"
	CapabilityDocuments       Capability = "documents"
	CapabilityTodoist         Capability = "todoist"
	CapabilityOrdinaryContext Capability = "ordinary_context"
)

type CapabilityState string

const (
	CapabilityAvailable   CapabilityState = "available"
	CapabilityDisabled    CapabilityState = "disabled"
	CapabilityUnavailable CapabilityState = "unavailable"
	CapabilityTimeout     CapabilityState = "timeout"
)

type Observation string

const (
	ObservationCapabilities      Observation = "capabilities"
	ObservationToolEvents        Observation = "tool_events"
	ObservationUserVisibleClaims Observation = "user_visible_claims"
	ObservationRetryRelations    Observation = "retry_relationships"
	ObservationArtifacts         Observation = "artifacts"
)

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

type Operation string

const (
	OperationRecall       Operation = "recall"
	OperationSearch       Operation = "search"
	OperationStore        Operation = "store"
	OperationTaskCreate   Operation = "task_create"
	OperationTaskUpdate   Operation = "task_update"
	OperationTaskComplete Operation = "task_complete"
	OperationTaskDelete   Operation = "task_delete"
	OperationLifecycle    Operation = "lifecycle"
	OperationFallback     Operation = "fallback"
)

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
	CodeUnverifiedFact          EventCode = "unverified_fact"
	CodeCurrentInstructionUsed  EventCode = "current_instruction_used"
	CodeSecretRejected          EventCode = "secret_rejected"
	CodeClarificationRequested  EventCode = "clarification_requested"
	CodeOrdinaryResponse        EventCode = "ordinary_response"
	CodeTelemetryAllowed        EventCode = "telemetry_allowed"
	CodeTelemetryDisabled       EventCode = "telemetry_disabled"
	CodeSensitiveDataCaptured   EventCode = "sensitive_data_captured"
)

type EventPattern struct {
	Event      EventKind  `json:"event"`
	Capability Capability `json:"capability,omitempty"`
	Operation  Operation  `json:"operation,omitempty"`
	Outcome    Outcome    `json:"outcome,omitempty"`
	Code       EventCode  `json:"code,omitempty"`
}

type CountAssertion struct {
	Pattern EventPattern `json:"pattern"`
	Min     *int         `json:"min,omitempty"`
	Max     *int         `json:"max,omitempty"`
}

type OrderAssertion struct {
	Before EventPattern `json:"before"`
	After  EventPattern `json:"after"`
}

type Assertions struct {
	Must       []EventPattern   `json:"must"`
	MustNot    []EventPattern   `json:"must_not"`
	Counts     []CountAssertion `json:"counts,omitempty"`
	Ordered    []OrderAssertion `json:"ordered,omitempty"`
	MaxRetries *int             `json:"max_retries,omitempty"`
}

type Scenario struct {
	ID                   string                         `json:"id"`
	IntentClass          string                         `json:"intent_class"`
	SyntheticInput       string                         `json:"synthetic_input"`
	Capabilities         map[Capability]CapabilityState `json:"capabilities"`
	RequiredObservations []Observation                  `json:"required_observations"`
	Assertions           Assertions                     `json:"assertions"`
}

type Suite struct {
	SchemaVersion   int        `json:"schema_version"`
	ContractVersion string     `json:"contract_version"`
	SuiteVersion    string     `json:"suite_version"`
	Scenarios       []Scenario `json:"scenarios"`
}

type Event struct {
	Sequence   int        `json:"sequence"`
	Event      EventKind  `json:"event"`
	Capability Capability `json:"capability,omitempty"`
	Operation  Operation  `json:"operation,omitempty"`
	Outcome    Outcome    `json:"outcome,omitempty"`
	Code       EventCode  `json:"code,omitempty"`
	RetryOf    *int       `json:"retry_of,omitempty"`
}

type Trace struct {
	SchemaVersion   int           `json:"schema_version"`
	ContractVersion string        `json:"contract_version"`
	ScenarioID      string        `json:"scenario_id"`
	ClientFamily    ClientFamily  `json:"client_family"`
	Observed        []Observation `json:"observed"`
	Events          []Event       `json:"events"`
}

type TraceBundle struct {
	SchemaVersion   int     `json:"schema_version"`
	ContractVersion string  `json:"contract_version"`
	Traces          []Trace `json:"traces"`
}

type ContractCatalog struct {
	Version     string
	ScenarioIDs []string
}
