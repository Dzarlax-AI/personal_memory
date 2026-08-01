package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const maxDatasetBytes int64 = 32 << 20

// Load decodes and source-neutrally validates a bounded dataset document.
func Load(reader io.Reader) (*Dataset, error) {
	limited := &io.LimitedReader{R: reader, N: maxDatasetBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var dataset Dataset
	if err := decoder.Decode(&dataset); err != nil {
		return nil, fmt.Errorf("decode fixture: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("fixture contains trailing JSON")
		}
		return nil, fmt.Errorf("decode trailing JSON: %w", err)
	}
	if limited.N <= 0 {
		return nil, fmt.Errorf("fixture exceeds %d bytes", maxDatasetBytes)
	}
	if err := dataset.Validate(); err != nil {
		return nil, err
	}
	return &dataset, nil
}

// Validate checks schema, vectors, IDs, queries, metrics, and gates without
// requiring live query IDs to exist in fixture point arrays.
func (d *Dataset) Validate() error {
	if d.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version must be %d", SchemaVersion)
	}
	if strings.TrimSpace(d.DatasetVersion) == "" {
		return fmt.Errorf("dataset_version is required")
	}
	identity := d.Embedding
	if strings.TrimSpace(identity.Provider) == "" || strings.TrimSpace(identity.ModelID) == "" ||
		strings.TrimSpace(identity.ModelRevision) == "" || strings.TrimSpace(identity.DType) == "" ||
		strings.TrimSpace(identity.Pooling) == "" || identity.VectorSize < 1 {
		return fmt.Errorf("complete embedding identity with positive vector_size is required")
	}
	cfg := &d.Configuration
	if strings.TrimSpace(cfg.Name) == "" || strings.TrimSpace(cfg.FactCollection) == "" ||
		strings.TrimSpace(cfg.ChunkCollection) == "" || strings.TrimSpace(cfg.FolderCollection) == "" {
		return fmt.Errorf("configuration name and logical collection names are required")
	}
	if cfg.FolderTopK < 1 || math.IsNaN(cfg.FolderThreshold) || math.IsInf(cfg.FolderThreshold, 0) {
		return fmt.Errorf("folder_top_k must be positive and folder_threshold must be finite")
	}
	if err := normalizeTopK(&cfg.TopK); err != nil {
		return err
	}

	for name, points := range map[string][]FixturePoint{
		"facts": d.Facts, "chunks": d.Chunks, "folders": d.Folders,
	} {
		ids := make(map[string]struct{}, len(points))
		for i, point := range points {
			if point.ID.String() == "" {
				return fmt.Errorf("%s point %d has empty ID", name, i)
			}
			if _, duplicate := ids[point.ID.String()]; duplicate {
				return fmt.Errorf("duplicate %s point ID %q", name, point.ID.String())
			}
			ids[point.ID.String()] = struct{}{}
			if err := validateVector(point.Vector, identity.VectorSize); err != nil {
				return fmt.Errorf("%s point %q: %w", name, point.ID.String(), err)
			}
		}
	}

	queryIDs := make(map[string]struct{}, len(d.Queries))
	for i := range d.Queries {
		query := &d.Queries[i]
		if strings.TrimSpace(query.ID) == "" {
			return fmt.Errorf("query ID is required")
		}
		if _, duplicate := queryIDs[query.ID]; duplicate {
			return fmt.Errorf("duplicate query ID %q", query.ID)
		}
		queryIDs[query.ID] = struct{}{}
		if query.Target != "facts" && query.Target != "documents" {
			return fmt.Errorf("query %q target must be facts or documents", query.ID)
		}
		if query.Mode != "flat" && query.Mode != "hierarchical" {
			return fmt.Errorf("query %q mode must be flat or hierarchical", query.ID)
		}
		if query.Target == "facts" && query.Mode != "flat" {
			return fmt.Errorf("query %q facts target supports only flat mode", query.ID)
		}
		if strings.TrimSpace(query.Text) == "" {
			return fmt.Errorf("query %q text is required", query.ID)
		}
		if len(query.Vector) > 0 {
			if err := validateVector(query.Vector, identity.VectorSize); err != nil {
				return fmt.Errorf("query %q: %w", query.ID, err)
			}
		}
		if len(query.Expected) == 0 {
			return fmt.Errorf("query %q expected items are required", query.ID)
		}
		expectedIDs := make(map[string]struct{}, len(query.Expected))
		for _, expected := range query.Expected {
			if expected.Grade < 1 || expected.Grade > 3 {
				return fmt.Errorf("query %q expected grade for %q must be 1..3", query.ID, expected.ID)
			}
			if err := validateNormalizedPointID(expected.ID); err != nil {
				return fmt.Errorf("query %q expected ID %q: %w", query.ID, expected.ID, err)
			}
			if _, duplicate := expectedIDs[expected.ID]; duplicate {
				return fmt.Errorf("query %q has duplicate expected ID %q", query.ID, expected.ID)
			}
			expectedIDs[expected.ID] = struct{}{}
		}
		forbiddenIDs := make(map[string]struct{}, len(query.ForbiddenIDs))
		for _, forbiddenID := range query.ForbiddenIDs {
			if err := validateNormalizedPointID(forbiddenID); err != nil {
				return fmt.Errorf("query %q forbidden ID %q: %w", query.ID, forbiddenID, err)
			}
			if _, duplicate := forbiddenIDs[forbiddenID]; duplicate {
				return fmt.Errorf("query %q has duplicate forbidden ID %q", query.ID, forbiddenID)
			}
			if _, expected := expectedIDs[forbiddenID]; expected {
				return fmt.Errorf("query %q ID %q is both expected and forbidden", query.ID, forbiddenID)
			}
			forbiddenIDs[forbiddenID] = struct{}{}
		}
	}
	if err := validateGateMap("minimum_hit_at", d.Gates.MinimumHitAt, cfg.TopK); err != nil {
		return err
	}
	if err := validateGateMap("minimum_ndcg_at", d.Gates.MinimumNDCGAt, cfg.TopK); err != nil {
		return err
	}
	if d.Gates.MinimumMRR != nil && (*d.Gates.MinimumMRR < 0 || *d.Gates.MinimumMRR > 1 || math.IsNaN(*d.Gates.MinimumMRR)) {
		return fmt.Errorf("minimum_mrr must be between 0 and 1")
	}
	return nil
}

// ValidateForSource adds execution-mode constraints to the source-neutral
// validation performed while loading a dataset.
func (d *Dataset) ValidateForSource(source string) error {
	if err := d.Validate(); err != nil {
		return err
	}
	switch source {
	case "live":
		return nil
	case "fixture":
	default:
		return fmt.Errorf("source must be fixture or live")
	}

	sets := map[string]map[string]struct{}{
		"facts":   pointIDSet(d.Facts),
		"chunks":  pointIDSet(d.Chunks),
		"folders": pointIDSet(d.Folders),
	}
	for _, query := range d.Queries {
		if len(query.Vector) == 0 {
			return fmt.Errorf("fixture query %q must include a precomputed vector", query.ID)
		}
		targetSet := sets["facts"]
		if query.Target == "documents" {
			targetSet = sets["chunks"]
		}
		for _, expected := range query.Expected {
			if _, exists := targetSet[expected.ID]; !exists {
				return fmt.Errorf("query %q references unknown expected ID %q", query.ID, expected.ID)
			}
		}
		for _, forbiddenID := range query.ForbiddenIDs {
			if _, exists := targetSet[forbiddenID]; !exists {
				return fmt.Errorf("query %q references unknown forbidden ID %q", query.ID, forbiddenID)
			}
		}
	}
	return nil
}

func pointIDSet(points []FixturePoint) map[string]struct{} {
	ids := make(map[string]struct{}, len(points))
	for _, point := range points {
		ids[point.ID.String()] = struct{}{}
	}
	return ids
}

func validateNormalizedPointID(id string) error {
	if _, err := strconv.ParseUint(id, 10, 64); err == nil {
		return nil
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("must be a Qdrant point ID (unsigned integer or UUID)")
	}
	if id != parsed.String() {
		return fmt.Errorf("must use canonical lowercase UUID format")
	}
	return nil
}

func validateVector(vector []float32, size int) error {
	if len(vector) != size {
		return fmt.Errorf("vector length is %d, want %d", len(vector), size)
	}
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("vector values must be finite")
		}
	}
	return nil
}

func normalizeTopK(values *[]int) error {
	if len(*values) == 0 {
		return fmt.Errorf("top_k must not be empty")
	}
	sort.Ints(*values)
	normalized := (*values)[:0]
	last := 0
	for _, value := range *values {
		if value < 1 || value > 100 {
			return fmt.Errorf("top_k values must be between 1 and 100")
		}
		if value != last {
			normalized = append(normalized, value)
			last = value
		}
	}
	*values = normalized
	return nil
}

func validateGateMap(name string, values map[string]float64, topK []int) error {
	allowed := make(map[int]struct{}, len(topK))
	for _, k := range topK {
		allowed[k] = struct{}{}
	}
	for rawK, value := range values {
		k, err := strconv.Atoi(rawK)
		if err != nil {
			return fmt.Errorf("%s key %q must be an integer", name, rawK)
		}
		if _, exists := allowed[k]; !exists {
			return fmt.Errorf("%s key %d is not present in top_k", name, k)
		}
		if value < 0 || value > 1 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%s[%d] must be between 0 and 1", name, k)
		}
	}
	return nil
}
