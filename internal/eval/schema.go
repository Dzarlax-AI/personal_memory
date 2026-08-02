package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/google/uuid"
)

// SchemaVersion is the original dataset and report schema version.
const SchemaVersion = 1

// LifecycleSchemaVersion added lifecycle evaluation contracts.
const LifecycleSchemaVersion = 2

// CurrentDatasetSchemaVersion is the latest accepted dataset schema.
const CurrentDatasetSchemaVersion = 3

// CurrentReportSchemaVersion is the latest emitted report schema.
const CurrentReportSchemaVersion = 3

// InputProfile is the visible, versioned transformation applied before
// embedding. These IDs intentionally mirror the production contract without
// coupling immutable evaluation schemas to runtime configuration.
type InputProfile string

const (
	LegacyRawV1      InputProfile = "legacy-raw-v1"
	MultilingualE5V1 InputProfile = "multilingual-e5-v1"
)

const multilingualE5SmallModelID = "intfloat/multilingual-e5-small"

// RetrievalStrategy identifies the ranking strategy under evaluation.
type RetrievalStrategy string

const (
	RetrievalVectorOnly RetrievalStrategy = "vector-only"
	RetrievalHybridRRF  RetrievalStrategy = "hybrid-rrf"
)

// PointID preserves whether a fixture ID was encoded as a JSON number or
// string while exposing one normalized string form for relevance scoring.
type PointID struct {
	value   string
	numeric bool
}

// String returns the normalized ID used for result matching.
func (id PointID) String() string { return id.value }

// IsNumeric reports whether the fixture encoded the ID as a JSON integer.
func (id PointID) IsNumeric() bool { return id.numeric }

func (id *PointID) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return fmt.Errorf("point ID is empty")
	}
	if data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("decode string point ID: %w", err)
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("point ID must not be empty")
		}
		parsed, err := uuid.Parse(value)
		if err != nil {
			return fmt.Errorf("string point ID %q must be a UUID: %w", value, err)
		}
		if value != parsed.String() {
			return fmt.Errorf("string point ID %q must use canonical lowercase UUID format", value)
		}
		id.value = value
		id.numeric = false
		return nil
	}
	value := string(data)
	if _, err := strconv.ParseUint(value, 10, 64); err != nil {
		return fmt.Errorf("numeric point ID %q must be an unsigned integer: %w", value, err)
	}
	id.value = value
	id.numeric = true
	return nil
}

// Vector decodes fixture vector elements without relying on encoding/json
// error text to identify values outside float32's finite range.
type Vector []float32

func (vector *Vector) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var values []json.Number
	if err := decoder.Decode(&values); err != nil {
		return fmt.Errorf("decode vector: %w", err)
	}
	decoded := make(Vector, len(values))
	for i, value := range values {
		parsed, err := strconv.ParseFloat(value.String(), 32)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return fmt.Errorf("vector values must be finite")
		}
		decoded[i] = float32(parsed)
	}
	*vector = decoded
	return nil
}

func (id PointID) MarshalJSON() ([]byte, error) {
	if id.numeric {
		return []byte(id.value), nil
	}
	return json.Marshal(id.value)
}

// EmbeddingIdentity identifies the vector space used by a dataset.
type EmbeddingIdentity struct {
	Provider      string       `json:"provider"`
	ModelID       string       `json:"model_id"`
	ModelRevision string       `json:"model_revision"`
	DType         string       `json:"dtype"`
	Pooling       string       `json:"pooling"`
	VectorSize    int          `json:"vector_size"`
	InputProfile  InputProfile `json:"input_profile,omitempty"`

	inputProfilePresent bool
}

func (identity *EmbeddingIdentity) UnmarshalJSON(data []byte) error {
	type wire EmbeddingIdentity
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded wire
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, exists := fields["input_profile"]; exists &&
		bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("input_profile must be a string")
	}
	*identity = EmbeddingIdentity(decoded)
	_, identity.inputProfilePresent = fields["input_profile"]
	return nil
}

// Configuration captures retrieval settings that participate in report identity.
type Configuration struct {
	Name                string            `json:"name"`
	FactCollection      string            `json:"fact_collection"`
	ChunkCollection     string            `json:"chunk_collection"`
	FolderCollection    string            `json:"folder_collection"`
	FolderTopK          int               `json:"folder_top_k"`
	FolderThreshold     float64           `json:"folder_threshold"`
	TopK                []int             `json:"top_k"`
	RetrievalStrategy   RetrievalStrategy `json:"retrieval_strategy,omitempty"`
	DenseCandidateLimit int               `json:"dense_candidate_limit,omitempty"`
	RRFConstant         int               `json:"rrf_constant,omitempty"`

	present map[string]bool
}

func (configuration *Configuration) UnmarshalJSON(data []byte) error {
	type wire Configuration
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded wire
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, field := range []string{"retrieval_strategy", "dense_candidate_limit", "rrf_constant"} {
		if raw, exists := fields[field]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s must not be null", field)
		}
	}
	*configuration = Configuration(decoded)
	configuration.present = make(map[string]bool, len(fields))
	for field := range fields {
		configuration.present[field] = true
	}
	return nil
}

func (configuration Configuration) MarshalJSON() ([]byte, error) {
	type wire struct {
		Name                string             `json:"name"`
		FactCollection      string             `json:"fact_collection"`
		ChunkCollection     string             `json:"chunk_collection"`
		FolderCollection    string             `json:"folder_collection"`
		FolderTopK          int                `json:"folder_top_k"`
		FolderThreshold     float64            `json:"folder_threshold"`
		TopK                []int              `json:"top_k"`
		RetrievalStrategy   *RetrievalStrategy `json:"retrieval_strategy,omitempty"`
		DenseCandidateLimit *int               `json:"dense_candidate_limit,omitempty"`
		RRFConstant         *int               `json:"rrf_constant,omitempty"`
	}
	encoded := wire{
		Name: configuration.Name, FactCollection: configuration.FactCollection,
		ChunkCollection:  configuration.ChunkCollection,
		FolderCollection: configuration.FolderCollection,
		FolderTopK:       configuration.FolderTopK, FolderThreshold: configuration.FolderThreshold,
		TopK: configuration.TopK,
	}
	if configuration.RetrievalStrategy != "" ||
		configuration.present["retrieval_strategy"] ||
		configuration.present["dense_candidate_limit"] ||
		configuration.present["rrf_constant"] {
		encoded.RetrievalStrategy = &configuration.RetrievalStrategy
		encoded.DenseCandidateLimit = &configuration.DenseCandidateLimit
		encoded.RRFConstant = &configuration.RRFConstant
	}
	return json.Marshal(encoded)
}

// FixturePoint is a synthetic Qdrant point used by fixture mode.
type FixturePoint struct {
	ID      PointID        `json:"id"`
	Vector  Vector         `json:"vector"`
	Payload map[string]any `json:"payload"`
}

// ExpectedItem assigns a graded relevance score to a result ID.
type ExpectedItem struct {
	ID    string `json:"id"`
	Grade int    `json:"grade"`
}

// QueryIntent identifies the lifecycle view requested by a fact query.
type QueryIntent string

const (
	QueryIntentCurrent     QueryIntent = "current"
	QueryIntentHistory     QueryIntent = "history"
	QueryIntentAsOf        QueryIntent = "as_of"
	QueryIntentUncertainty QueryIntent = "uncertainty"
)

func (intent QueryIntent) valid() bool {
	switch intent {
	case QueryIntentCurrent, QueryIntentHistory, QueryIntentAsOf, QueryIntentUncertainty:
		return true
	default:
		return false
	}
}

func (intent *QueryIntent) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("query intent must be a string: %w", err)
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("query intent must be a string")
	}
	*intent = QueryIntent(value)
	return nil
}

// PresentationDecision is the expected lifecycle treatment of a result.
type PresentationDecision string

const (
	PresentationInclude   PresentationDecision = "include"
	PresentationSuppress  PresentationDecision = "suppress"
	PresentationDemote    PresentationDecision = "demote"
	PresentationUncertain PresentationDecision = "uncertain"
)

func (decision PresentationDecision) valid() bool {
	switch decision {
	case PresentationInclude, PresentationSuppress, PresentationDemote, PresentationUncertain:
		return true
	default:
		return false
	}
}

func (decision *PresentationDecision) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("presentation decision must be a string: %w", err)
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("presentation decision must be a string")
	}
	*decision = PresentationDecision(value)
	return nil
}

// LifecycleExpectation describes lifecycle presentation independently from a
// result's graded semantic relevance.
type LifecycleExpectation struct {
	ID          string               `json:"id"`
	State       lifecycle.State      `json:"state,omitempty"`
	Decision    PresentationDecision `json:"decision"`
	ReasonCodes []string             `json:"reason_codes,omitempty"`

	statePresent       bool
	reasonCodesPresent bool
}

func (expectation *LifecycleExpectation) UnmarshalJSON(data []byte) error {
	type wire LifecycleExpectation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded wire
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("lifecycle expectation contains trailing JSON")
		}
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, exists := fields["state"]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("lifecycle expectation state must be a string")
	}
	if raw, exists := fields["reason_codes"]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("lifecycle expectation reason_codes must be an array")
	}
	*expectation = LifecycleExpectation(decoded)
	_, expectation.statePresent = fields["state"]
	_, expectation.reasonCodesPresent = fields["reason_codes"]
	return nil
}

func (expectation LifecycleExpectation) assertsReasonCodes() bool {
	return expectation.reasonCodesPresent || expectation.ReasonCodes != nil
}

func (expectation LifecycleExpectation) MarshalJSON() ([]byte, error) {
	fields := map[string]any{
		"id":       expectation.ID,
		"decision": expectation.Decision,
	}
	if expectation.statePresent || expectation.State != "" {
		fields["state"] = expectation.State
	}
	if expectation.assertsReasonCodes() {
		fields["reason_codes"] = append([]string{}, expectation.ReasonCodes...)
	}
	return json.Marshal(fields)
}

// Query describes one fact or document retrieval evaluation.
type Query struct {
	ID                    string                 `json:"id"`
	Target                string                 `json:"target"`
	Mode                  string                 `json:"mode"`
	Text                  string                 `json:"text"`
	Vector                Vector                 `json:"vector,omitempty"`
	Expected              []ExpectedItem         `json:"expected"`
	ForbiddenIDs          []string               `json:"forbidden_ids,omitempty"`
	Intent                QueryIntent            `json:"intent,omitempty"`
	AsOf                  string                 `json:"as_of,omitempty"`
	LifecycleExpectations []LifecycleExpectation `json:"lifecycle_expectations,omitempty"`
	Cohorts               []QueryCohort          `json:"cohorts,omitempty"`

	intentPresent                bool
	asOfPresent                  bool
	lifecycleExpectationsPresent bool
	cohortsPresent               bool
}

// EffectiveIntent returns the normalized query intent without changing the
// serialized representation. Omitted intent is current for v1 compatibility.
func (query Query) EffectiveIntent() QueryIntent {
	if query.Intent == "" {
		return QueryIntentCurrent
	}
	return query.Intent
}

func (query *Query) UnmarshalJSON(data []byte) error {
	type wire Query
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded wire
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("query contains trailing JSON")
		}
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, exists := fields["as_of"]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("query as_of must be a string")
	}
	if raw, exists := fields["lifecycle_expectations"]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("query lifecycle_expectations must be an array")
	}
	if raw, exists := fields["cohorts"]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("query cohorts must be an array")
	}
	*query = Query(decoded)
	_, query.intentPresent = fields["intent"]
	_, query.asOfPresent = fields["as_of"]
	_, query.lifecycleExpectationsPresent = fields["lifecycle_expectations"]
	_, query.cohortsPresent = fields["cohorts"]
	return nil
}

// QueryCohort identifies a stable query slice. Cohorts describe retrieval
// shape only; lifecycle state remains a separate evaluation dimension.
type QueryCohort string

const (
	CohortGeneralSemantic QueryCohort = "general-semantic"
	CohortMultilingual    QueryCohort = "multilingual"
	CohortExactName       QueryCohort = "exact-name"
	CohortIdentifierPath  QueryCohort = "identifier-path"
	CohortLifecycle       QueryCohort = "lifecycle"
)

// LifecyclePayload is the strict evaluation representation of lifecycle.Input.
// Presence metadata lets validation require complete transition targets without
// changing the public value types later execution will consume.
type LifecyclePayload struct {
	State        lifecycle.State       `json:"state"`
	Canonical    bool                  `json:"canonical"`
	Provenance   *lifecycle.Provenance `json:"provenance,omitempty"`
	VerifiedAt   string                `json:"verified_at,omitempty"`
	Supersedes   []string              `json:"supersedes"`
	SupersededBy []string              `json:"superseded_by"`

	present map[string]bool
}

func (payload *LifecyclePayload) UnmarshalJSON(data []byte) error {
	type wire LifecyclePayload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded wire
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("lifecycle payload contains trailing JSON")
		}
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*payload = LifecyclePayload(decoded)
	payload.present = make(map[string]bool, len(fields))
	for field := range fields {
		payload.present[field] = true
	}
	if raw, exists := fields["canonical"]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("canonical must be a boolean")
	}
	return nil
}

// TransitionScenario is a declarative lifecycle transition case. Execution and
// scoring are intentionally handled by later evaluator tasks.
type TransitionScenario struct {
	ID                 string           `json:"id"`
	PointID            PointID          `json:"point_id"`
	SourceLifecycle    LifecyclePayload `json:"source_lifecycle"`
	TargetLifecycle    LifecyclePayload `json:"target_lifecycle"`
	ExpectedValid      bool             `json:"expected_valid"`
	ExpectedReasonCode string           `json:"expected_reason_code,omitempty"`

	expectedValidPresent bool
	reasonCodePresent    bool
}

func (scenario *TransitionScenario) UnmarshalJSON(data []byte) error {
	type wire TransitionScenario
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded wire
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("transition scenario contains trailing JSON")
		}
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*scenario = TransitionScenario(decoded)
	_, scenario.expectedValidPresent = fields["expected_valid"]
	_, scenario.reasonCodePresent = fields["expected_reason_code"]
	if raw, exists := fields["expected_valid"]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("expected_valid must be a boolean")
	}
	return nil
}

// Gates contains explicit thresholds that may fail an evaluation.
type Gates struct {
	ForbidInvariantViolations bool               `json:"forbid_invariant_violations"`
	ForbidLifecycleViolations bool               `json:"forbid_lifecycle_violations,omitempty"`
	MinimumHitAt              map[string]float64 `json:"minimum_hit_at,omitempty"`
	MinimumMRR                *float64           `json:"minimum_mrr,omitempty"`
	MinimumNDCGAt             map[string]float64 `json:"minimum_ndcg_at,omitempty"`

	forbidLifecycleViolationsPresent bool
}

func (gates *Gates) UnmarshalJSON(data []byte) error {
	type wire Gates
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded wire
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, exists := fields["forbid_lifecycle_violations"]; exists &&
		bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("forbid_lifecycle_violations must be a boolean")
	}
	*gates = Gates(decoded)
	_, gates.forbidLifecycleViolationsPresent = fields["forbid_lifecycle_violations"]
	return nil
}

// Dataset is the versioned input contract for fixture and live evaluation.
type Dataset struct {
	SchemaVersion       int                  `json:"schema_version"`
	DatasetVersion      string               `json:"dataset_version"`
	Embedding           EmbeddingIdentity    `json:"embedding"`
	Configuration       Configuration        `json:"configuration"`
	Facts               []FixturePoint       `json:"facts"`
	Chunks              []FixturePoint       `json:"chunks"`
	Folders             []FixturePoint       `json:"folders"`
	Queries             []Query              `json:"queries"`
	Gates               Gates                `json:"gates"`
	TransitionScenarios []TransitionScenario `json:"transition_scenarios,omitempty"`

	transitionScenariosPresent bool
}

func (dataset *Dataset) UnmarshalJSON(data []byte) error {
	type wire Dataset
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded wire
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("dataset contains trailing JSON")
		}
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, exists := fields["transition_scenarios"]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("transition_scenarios must be an array")
	}
	*dataset = Dataset(decoded)
	_, dataset.transitionScenariosPresent = fields["transition_scenarios"]
	return nil
}

// RetrievedItem is the non-sensitive result representation stored in reports.
type RetrievedItem struct {
	ID          string  `json:"id"`
	Score       float64 `json:"score"`
	MissingText bool    `json:"missing_text,omitempty"`
}

// QueryMetrics contains ranking and invariant metrics for one query.
type QueryMetrics struct {
	HitAt                map[int]float64 `json:"hit_at"`
	MRR                  float64         `json:"mrr"`
	NDCGAt               map[int]float64 `json:"ndcg_at"`
	InvariantViolations  []string        `json:"invariant_violations,omitempty"`
	MissingTextResultIDs []string        `json:"missing_text_result_ids,omitempty"`
}

// AggregateMetrics averages ranking metrics and counts invariant violations.
type AggregateMetrics struct {
	HitAt               map[int]float64 `json:"hit_at"`
	MRR                 float64         `json:"mrr"`
	NDCGAt              map[int]float64 `json:"ndcg_at"`
	InvariantViolations int             `json:"invariant_violations"`
}

// QueryReport combines normalized results and metrics for one query.
type QueryReport struct {
	ID        string                `json:"id"`
	Target    string                `json:"target"`
	Mode      string                `json:"mode"`
	Cohorts   []QueryCohort         `json:"cohorts,omitempty"`
	Results   []RetrievedItem       `json:"results"`
	Metrics   QueryMetrics          `json:"metrics"`
	Lifecycle *QueryLifecycleReport `json:"lifecycle,omitempty"`
}

// CohortAggregateMetrics is a deterministic ranking aggregate for one cohort.
type CohortAggregateMetrics struct {
	Cohort              QueryCohort     `json:"cohort"`
	QueryCount          int             `json:"query_count"`
	HitAt               map[int]float64 `json:"hit_at"`
	MRR                 float64         `json:"mrr"`
	NDCGAt              map[int]float64 `json:"ndcg_at"`
	InvariantViolations int             `json:"invariant_violations"`

	present map[string]bool
}

func (metrics *CohortAggregateMetrics) UnmarshalJSON(data []byte) error {
	type wire CohortAggregateMetrics
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded wire
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, field := range []string{
		"cohort", "query_count", "hit_at", "mrr", "ndcg_at", "invariant_violations",
	} {
		raw, exists := fields[field]
		if !exists {
			return fmt.Errorf("cohort aggregate field %s is required", field)
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("cohort aggregate field %s must not be null", field)
		}
	}
	*metrics = CohortAggregateMetrics(decoded)
	metrics.present = make(map[string]bool, len(fields))
	for field := range fields {
		metrics.present[field] = true
	}
	return nil
}

// Report is the deterministic canonical evaluation output.
type Report struct {
	SchemaVersion  int                      `json:"schema_version"`
	DatasetVersion string                   `json:"dataset_version"`
	Mode           string                   `json:"mode"`
	Embedding      EmbeddingIdentity        `json:"embedding"`
	Configuration  Configuration            `json:"configuration"`
	TopK           []int                    `json:"top_k"`
	Aggregate      AggregateMetrics         `json:"aggregate"`
	Cohorts        []CohortAggregateMetrics `json:"cohorts,omitempty"`
	Queries        []QueryReport            `json:"queries"`
	Lifecycle      *LifecycleReport         `json:"lifecycle,omitempty"`
	GatesPassed    bool                     `json:"gates_passed"`
	GateFailures   []string                 `json:"gate_failures,omitempty"`
}
