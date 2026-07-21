package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// State describes whether a stored fact is suitable as current context.
type State string

const (
	Current    State = "current"
	Historical State = "historical"
	Superseded State = "superseded"
	Disputed   State = "disputed"
)

// Provenance records where a fact came from. It is not an authority score.
type Provenance struct {
	Source    string `json:"source"`
	Reference string `json:"reference,omitempty"`
}

// View is the normalized lifecycle metadata for a fact payload.
// Valid is false when explicit lifecycle metadata is malformed. InvalidReason
// intentionally describes metadata only and must never include fact text.
type View struct {
	State         State       `json:"state"`
	Legacy        bool        `json:"legacy"`
	Canonical     bool        `json:"canonical"`
	Provenance    *Provenance `json:"provenance,omitempty"`
	VerifiedAt    string      `json:"verified_at,omitempty"`
	Supersedes    []string    `json:"supersedes"`
	SupersededBy  []string    `json:"superseded_by"`
	Valid         bool        `json:"valid"`
	InvalidReason string      `json:"invalid_reason,omitempty"`
}

// Candidate couples lifecycle metadata with an unmodified vector score.
type Candidate struct {
	PointID string
	Score   float64
	View    View
}

func (s State) Valid() bool {
	switch s {
	case Current, Historical, Superseded, Disputed:
		return true
	default:
		return false
	}
}

// Parse normalizes lifecycle metadata from a Qdrant payload. A payload with no
// lifecycle fields is the only legacy-current case; an explicit unknown or
// malformed value is returned as an invalid view so callers can inspect it
// without treating it as current truth.
func Parse(payload map[string]interface{}, pointID string) (View, error) {
	view := View{
		State:        Current,
		Legacy:       true,
		Supersedes:   []string{},
		SupersededBy: []string{},
		Valid:        true,
	}
	for _, key := range []string{"lifecycle_state", "canonical", "provenance", "verified_at", "supersedes", "superseded_by"} {
		if _, exists := payload[key]; exists {
			view.Legacy = false
			break
		}
	}

	if raw, exists := payload["lifecycle_state"]; exists {
		state, ok := raw.(string)
		if !ok || strings.TrimSpace(state) != state || !State(state).Valid() {
			return invalid(view, "lifecycle_state must be current, historical, superseded, or disputed")
		}
		view.State = State(state)
	}

	if raw, exists := payload["canonical"]; exists {
		canonical, ok := raw.(bool)
		if !ok {
			return invalid(view, "canonical must be a boolean")
		}
		view.Canonical = canonical
	}

	if raw, exists := payload["provenance"]; exists {
		provenance, err := parseProvenance(raw)
		if err != nil {
			return invalid(view, err.Error())
		}
		view.Provenance = provenance
	}

	if raw, exists := payload["verified_at"]; exists {
		verifiedAt, ok := raw.(string)
		if !ok || verifiedAt == "" {
			return invalid(view, "verified_at must be an RFC3339 string")
		}
		if _, err := time.Parse(time.RFC3339, verifiedAt); err != nil {
			return invalid(view, "verified_at must use RFC3339 format")
		}
		view.VerifiedAt = verifiedAt
	}

	var err error
	if raw, exists := payload["supersedes"]; exists {
		view.Supersedes, err = parseRelationships(raw, pointID, "supersedes")
		if err != nil {
			return invalid(view, err.Error())
		}
	}
	if raw, exists := payload["superseded_by"]; exists {
		view.SupersededBy, err = parseRelationships(raw, pointID, "superseded_by")
		if err != nil {
			return invalid(view, err.Error())
		}
	}

	if err := Validate(view); err != nil {
		return invalid(view, err.Error())
	}
	return view, nil
}

func invalid(view View, reason string) (View, error) {
	view.Valid = false
	view.InvalidReason = reason
	return view, errors.New(reason)
}

func parseProvenance(raw interface{}) (*Provenance, error) {
	value, ok := raw.(map[string]interface{})
	if !ok {
		return nil, errors.New("provenance must be an object")
	}
	source, ok := value["source"].(string)
	if !ok || strings.TrimSpace(source) == "" {
		return nil, errors.New("provenance.source must be a non-empty string")
	}
	provenance := &Provenance{Source: strings.TrimSpace(source)}
	if rawReference, exists := value["reference"]; exists {
		reference, ok := rawReference.(string)
		if !ok {
			return nil, errors.New("provenance.reference must be a string")
		}
		provenance.Reference = reference
	}
	return provenance, nil
}

func parseRelationships(raw interface{}, pointID, field string) ([]string, error) {
	var values []interface{}
	switch typed := raw.(type) {
	case []interface{}:
		values = typed
	case []string:
		values = make([]interface{}, len(typed))
		for i := range typed {
			values[i] = typed[i]
		}
	default:
		return nil, fmt.Errorf("%s must be an array of point IDs", field)
	}

	normalizedPointID := strings.TrimSpace(pointID)
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		id, err := normalizePointID(value)
		if err != nil {
			return nil, fmt.Errorf("%s contains an invalid point ID: %w", field, err)
		}
		if normalizedPointID != "" && id == normalizedPointID {
			return nil, fmt.Errorf("%s cannot reference the fact itself", field)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}

func normalizePointID(raw interface{}) (string, error) {
	switch value := raw.(type) {
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			return "", errors.New("ID must not be empty")
		}
		return value, nil
	case json.Number:
		parsed, err := strconv.ParseUint(string(value), 10, 64)
		if err != nil {
			return "", errors.New("numeric ID must be a non-negative integer")
		}
		return strconv.FormatUint(parsed, 10), nil
	case float64:
		const maxSafeJSONInteger = float64(1<<53 - 1)
		if value < 0 || value > maxSafeJSONInteger || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
			return "", errors.New("numeric ID must be a non-negative integer")
		}
		return strconv.FormatFloat(value, 'f', 0, 64), nil
	default:
		return "", errors.New("ID must be a string or integer")
	}
}

// Validate checks lifecycle invariants without performing storage I/O.
func Validate(view View) error {
	if !view.Valid {
		return errors.New("lifecycle metadata is invalid")
	}
	if !view.State.Valid() {
		return errors.New("lifecycle state is invalid")
	}
	if view.Provenance != nil && strings.TrimSpace(view.Provenance.Source) == "" {
		return errors.New("provenance.source must be a non-empty string")
	}
	if view.VerifiedAt != "" {
		if _, err := time.Parse(time.RFC3339, view.VerifiedAt); err != nil {
			return errors.New("verified_at must use RFC3339 format")
		}
	}
	if err := validateNormalizedRelationships(view.Supersedes, "supersedes"); err != nil {
		return err
	}
	if err := validateNormalizedRelationships(view.SupersededBy, "superseded_by"); err != nil {
		return err
	}
	if view.Canonical && view.State != Current {
		return errors.New("canonical facts must be current")
	}
	if view.State == Superseded && len(view.SupersededBy) == 0 {
		return errors.New("superseded facts require superseded_by")
	}
	if view.State == Current && len(view.SupersededBy) != 0 {
		return errors.New("current facts cannot have superseded_by")
	}
	return nil
}

func validateNormalizedRelationships(ids []string, field string) error {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			return fmt.Errorf("%s contains an empty point ID", field)
		}
		if id != trimmed {
			return fmt.Errorf("%s contains a non-normalized point ID", field)
		}
		if _, exists := seen[trimmed]; exists {
			return fmt.Errorf("%s contains a duplicate point ID", field)
		}
		seen[trimmed] = struct{}{}
	}
	return nil
}

// ValidateTransition validates an explicit target classification. Every known
// state can be corrected to another known state; state-specific invariants are
// what make transitions valid or invalid. Repeating a valid state is safe.
func ValidateTransition(pointID string, _ View, target View) error {
	if err := Validate(target); err != nil {
		return err
	}
	pointID = strings.TrimSpace(pointID)
	if pointID == "" {
		return errors.New("point ID is required to validate a lifecycle transition")
	}
	for _, relationship := range append(append([]string{}, target.Supersedes...), target.SupersededBy...) {
		if relationship == pointID {
			return errors.New("lifecycle relationships cannot reference the fact itself")
		}
	}
	return nil
}

// IsCurrentTruth reports whether a fact is safe for default model context.
// Retention fields such as permanent deliberately do not participate.
func IsCurrentTruth(view View, expired bool) bool {
	return view.Valid && !expired && view.State == Current
}

// SortCandidates applies lifecycle authority tiers without changing scores.
func SortCandidates(candidates []Candidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		leftFinite := !math.IsNaN(candidates[i].Score) && !math.IsInf(candidates[i].Score, 0)
		rightFinite := !math.IsNaN(candidates[j].Score) && !math.IsInf(candidates[j].Score, 0)
		if leftFinite != rightFinite {
			return leftFinite
		}
		leftTier := rankTier(candidates[i].View)
		rightTier := rankTier(candidates[j].View)
		if leftTier != rightTier {
			return leftTier < rightTier
		}
		if leftFinite && candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].PointID < candidates[j].PointID
	})
}

func rankTier(view View) int {
	if !view.Valid {
		return 5
	}
	switch view.State {
	case Current:
		if view.Canonical {
			return 0
		}
		return 1
	case Disputed:
		return 2
	case Historical:
		return 3
	case Superseded:
		return 4
	default:
		return 5
	}
}
