package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const SchemaVersion = 1

// PointID preserves whether a fixture ID was encoded as a JSON number or
// string while exposing one normalized string form for relevance scoring.
type PointID struct {
	value   string
	numeric bool
}

func (id PointID) String() string  { return id.value }
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

func (id PointID) MarshalJSON() ([]byte, error) {
	if id.numeric {
		return []byte(id.value), nil
	}
	return json.Marshal(id.value)
}

type EmbeddingIdentity struct {
	Provider      string `json:"provider"`
	ModelID       string `json:"model_id"`
	ModelRevision string `json:"model_revision"`
	DType         string `json:"dtype"`
	Pooling       string `json:"pooling"`
	VectorSize    int    `json:"vector_size"`
}

type Configuration struct {
	Name             string  `json:"name"`
	FactCollection   string  `json:"fact_collection"`
	ChunkCollection  string  `json:"chunk_collection"`
	FolderCollection string  `json:"folder_collection"`
	FolderTopK       int     `json:"folder_top_k"`
	FolderThreshold  float64 `json:"folder_threshold"`
	TopK             []int   `json:"top_k"`
}

type FixturePoint struct {
	ID      PointID        `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload"`
}

type ExpectedItem struct {
	ID    string `json:"id"`
	Grade int    `json:"grade"`
}

type Query struct {
	ID           string         `json:"id"`
	Target       string         `json:"target"`
	Mode         string         `json:"mode"`
	Text         string         `json:"text"`
	Vector       []float32      `json:"vector,omitempty"`
	Expected     []ExpectedItem `json:"expected"`
	ForbiddenIDs []string       `json:"forbidden_ids,omitempty"`
}

type Gates struct {
	ForbidInvariantViolations bool               `json:"forbid_invariant_violations"`
	MinimumHitAt              map[string]float64 `json:"minimum_hit_at,omitempty"`
	MinimumMRR                *float64           `json:"minimum_mrr,omitempty"`
	MinimumNDCGAt             map[string]float64 `json:"minimum_ndcg_at,omitempty"`
}

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

type RetrievedItem struct {
	ID          string  `json:"id"`
	Score       float64 `json:"score"`
	MissingText bool    `json:"missing_text,omitempty"`
}

type QueryMetrics struct {
	HitAt                map[int]float64 `json:"hit_at"`
	MRR                  float64         `json:"mrr"`
	NDCGAt               map[int]float64 `json:"ndcg_at"`
	InvariantViolations  []string        `json:"invariant_violations,omitempty"`
	MissingTextResultIDs []string        `json:"missing_text_result_ids,omitempty"`
}

type AggregateMetrics struct {
	HitAt               map[int]float64 `json:"hit_at"`
	MRR                 float64         `json:"mrr"`
	NDCGAt              map[int]float64 `json:"ndcg_at"`
	InvariantViolations int             `json:"invariant_violations"`
}

type QueryReport struct {
	ID      string          `json:"id"`
	Target  string          `json:"target"`
	Mode    string          `json:"mode"`
	Results []RetrievedItem `json:"results"`
	Metrics QueryMetrics    `json:"metrics"`
}

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
