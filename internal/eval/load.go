package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

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
	if d.SchemaVersion != SchemaVersion && d.SchemaVersion != CurrentDatasetSchemaVersion {
		return fmt.Errorf("schema_version must be %d or %d", SchemaVersion, CurrentDatasetSchemaVersion)
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
		if query.Intent == "" {
			query.Intent = QueryIntentCurrent
		}
		if !query.Intent.valid() {
			return fmt.Errorf("query %q intent must be current, history, as_of, or uncertainty", query.ID)
		}
		if query.Intent == QueryIntentAsOf {
			if !validISODate(query.AsOf) {
				return fmt.Errorf("query %q as_of intent requires an ISO YYYY-MM-DD date", query.ID)
			}
		} else if query.asOfPresent || query.AsOf != "" {
			return fmt.Errorf("query %q as_of is only valid for as_of intent", query.ID)
		}
		if query.Target == "documents" {
			if query.Intent != QueryIntentCurrent {
				return fmt.Errorf("query %q document queries support only current intent", query.ID)
			}
			if len(query.LifecycleExpectations) != 0 {
				return fmt.Errorf("query %q document queries do not support lifecycle expectations", query.ID)
			}
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
		expectationIDs := make(map[string]struct{}, len(query.LifecycleExpectations))
		for _, expectation := range query.LifecycleExpectations {
			if err := validateNormalizedPointID(expectation.ID); err != nil {
				return fmt.Errorf("query %q lifecycle expectation ID %q: %w", query.ID, expectation.ID, err)
			}
			if _, duplicate := expectationIDs[expectation.ID]; duplicate {
				return fmt.Errorf("query %q has duplicate lifecycle expectation ID %q", query.ID, expectation.ID)
			}
			expectationIDs[expectation.ID] = struct{}{}
			if expectation.State != "" && !expectation.State.Valid() {
				return fmt.Errorf("query %q lifecycle expectation state for %q is invalid", query.ID, expectation.ID)
			}
			if !expectation.Decision.valid() {
				return fmt.Errorf("query %q lifecycle expectation decision for %q must be include, suppress, demote, or uncertain", query.ID, expectation.ID)
			}
			reasonCodes := make(map[string]struct{}, len(expectation.ReasonCodes))
			for _, reasonCode := range expectation.ReasonCodes {
				if strings.TrimSpace(reasonCode) == "" || reasonCode != strings.TrimSpace(reasonCode) {
					return fmt.Errorf("query %q lifecycle expectation for %q contains an empty or non-normalized reason code", query.ID, expectation.ID)
				}
				if _, duplicate := reasonCodes[reasonCode]; duplicate {
					return fmt.Errorf("query %q lifecycle expectation for %q contains duplicate reason code %q", query.ID, expectation.ID, reasonCode)
				}
				reasonCodes[reasonCode] = struct{}{}
			}
		}
		if d.SchemaVersion == SchemaVersion &&
			(query.Intent != QueryIntentCurrent || query.AsOf != "" || len(query.LifecycleExpectations) != 0) {
			return fmt.Errorf("query %q lifecycle fields require schema_version %d", query.ID, CurrentDatasetSchemaVersion)
		}
	}
	transitionIDs := make(map[string]struct{}, len(d.TransitionScenarios))
	for i := range d.TransitionScenarios {
		scenario := &d.TransitionScenarios[i]
		if strings.TrimSpace(scenario.ID) == "" || scenario.ID != strings.TrimSpace(scenario.ID) {
			return fmt.Errorf("transition scenario ID must be a non-empty normalized string")
		}
		if _, duplicate := transitionIDs[scenario.ID]; duplicate {
			return fmt.Errorf("duplicate transition scenario ID %q", scenario.ID)
		}
		transitionIDs[scenario.ID] = struct{}{}
		if scenario.PointID.String() == "" {
			return fmt.Errorf("transition scenario %q point_id is required", scenario.ID)
		}
		if !scenario.expectedValidPresent {
			return fmt.Errorf("transition scenario %q expected_valid is required", scenario.ID)
		}
		if scenario.reasonCodePresent && (strings.TrimSpace(scenario.ExpectedReasonCode) == "" ||
			scenario.ExpectedReasonCode != strings.TrimSpace(scenario.ExpectedReasonCode)) {
			return fmt.Errorf("transition scenario %q expected_reason_code must be non-empty and normalized", scenario.ID)
		}
		if err := validateLifecyclePayload(scenario.SourceLifecycle, "source_lifecycle"); err != nil {
			return fmt.Errorf("transition scenario %q: %w", scenario.ID, err)
		}
		if err := validateLifecyclePayload(scenario.TargetLifecycle, "target_lifecycle"); err != nil {
			return fmt.Errorf("transition scenario %q: %w", scenario.ID, err)
		}
	}
	if d.SchemaVersion == SchemaVersion && len(d.TransitionScenarios) != 0 {
		return fmt.Errorf("transition_scenarios require schema_version %d", CurrentDatasetSchemaVersion)
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
		for _, expectation := range query.LifecycleExpectations {
			if _, exists := targetSet[expectation.ID]; !exists {
				return fmt.Errorf("query %q references unknown lifecycle expectation ID %q", query.ID, expectation.ID)
			}
		}
	}
	factIDs := sets["facts"]
	for _, scenario := range d.TransitionScenarios {
		if _, exists := factIDs[scenario.PointID.String()]; !exists {
			return fmt.Errorf("transition scenario %q references unknown point ID %q", scenario.ID, scenario.PointID.String())
		}
	}
	return nil
}

func validISODate(value string) bool {
	if len(value) != len("2006-01-02") {
		return false
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func validateLifecyclePayload(payload LifecyclePayload, field string) error {
	for _, required := range []string{"state", "canonical", "supersedes", "superseded_by"} {
		if !payload.present[required] {
			return fmt.Errorf("%s.%s is required", field, required)
		}
	}
	if !payload.State.Valid() {
		return fmt.Errorf("%s.state must be current, historical, superseded, or disputed", field)
	}
	if payload.Supersedes == nil {
		return fmt.Errorf("%s.supersedes must be an array", field)
	}
	if payload.SupersededBy == nil {
		return fmt.Errorf("%s.superseded_by must be an array", field)
	}
	if payload.Provenance != nil && strings.TrimSpace(payload.Provenance.Source) == "" {
		return fmt.Errorf("%s.provenance.source must be a non-empty string", field)
	}
	if payload.VerifiedAt != "" {
		if _, err := time.Parse(time.RFC3339, payload.VerifiedAt); err != nil {
			return fmt.Errorf("%s.verified_at must use RFC3339 format", field)
		}
	}
	for relationshipField, ids := range map[string][]string{
		"supersedes": payload.Supersedes, "superseded_by": payload.SupersededBy,
	} {
		seen := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if err := validateNormalizedPointID(id); err != nil {
				return fmt.Errorf("%s.%s ID %q: %w", field, relationshipField, id, err)
			}
			if _, duplicate := seen[id]; duplicate {
				return fmt.Errorf("%s.%s contains duplicate point ID %q", field, relationshipField, id)
			}
			seen[id] = struct{}{}
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
