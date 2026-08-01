package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// SchemaVersion is the only dataset and report schema accepted by this package.
const SchemaVersion = 1

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
	Provider      string `json:"provider"`
	ModelID       string `json:"model_id"`
	ModelRevision string `json:"model_revision"`
	DType         string `json:"dtype"`
	Pooling       string `json:"pooling"`
	VectorSize    int    `json:"vector_size"`
}

// Configuration captures retrieval settings that participate in report identity.
type Configuration struct {
	Name             string  `json:"name"`
	FactCollection   string  `json:"fact_collection"`
	ChunkCollection  string  `json:"chunk_collection"`
	FolderCollection string  `json:"folder_collection"`
	FolderTopK       int     `json:"folder_top_k"`
	FolderThreshold  float64 `json:"folder_threshold"`
	TopK             []int   `json:"top_k"`
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

// Query describes one fact or document retrieval evaluation.
type Query struct {
	ID           string         `json:"id"`
	Target       string         `json:"target"`
	Mode         string         `json:"mode"`
	Text         string         `json:"text"`
	Vector       Vector         `json:"vector,omitempty"`
	Expected     []ExpectedItem `json:"expected"`
	ForbiddenIDs []string       `json:"forbidden_ids,omitempty"`
}

// Gates contains explicit thresholds that may fail an evaluation.
type Gates struct {
	ForbidInvariantViolations bool               `json:"forbid_invariant_violations"`
	MinimumHitAt              map[string]float64 `json:"minimum_hit_at,omitempty"`
	MinimumMRR                *float64           `json:"minimum_mrr,omitempty"`
	MinimumNDCGAt             map[string]float64 `json:"minimum_ndcg_at,omitempty"`
}

// Dataset is the versioned input contract for fixture and live evaluation.
type Dataset struct {
	SchemaVersion  int               `json:"schema_version"`
	DatasetVersion string            `json:"dataset_version"`
	Embedding      EmbeddingIdentity `json:"embedding"`
	Configuration  Configuration     `json:"configuration"`
	Facts          []FixturePoint    `json:"facts"`
	Chunks         []FixturePoint    `json:"chunks"`
	Folders        []FixturePoint    `json:"folders"`
	Queries        []Query           `json:"queries"`
	Gates          Gates             `json:"gates"`
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
	ID      string          `json:"id"`
	Target  string          `json:"target"`
	Mode    string          `json:"mode"`
	Results []RetrievedItem `json:"results"`
	Metrics QueryMetrics    `json:"metrics"`
}

// Report is the deterministic canonical evaluation output.
type Report struct {
	SchemaVersion  int               `json:"schema_version"`
	DatasetVersion string            `json:"dataset_version"`
	Mode           string            `json:"mode"`
	Embedding      EmbeddingIdentity `json:"embedding"`
	Configuration  Configuration     `json:"configuration"`
	TopK           []int             `json:"top_k"`
	Aggregate      AggregateMetrics  `json:"aggregate"`
	Queries        []QueryReport     `json:"queries"`
	GatesPassed    bool              `json:"gates_passed"`
	GateFailures   []string          `json:"gate_failures,omitempty"`
}
