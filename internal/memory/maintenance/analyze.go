package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

const (
	ManifestSchemaVersion = 1
	PolicyVersion         = "1.0.0"
)

type CandidateClass string

const (
	ClassExpired             CandidateClass = "expired"
	ClassSupersededRetention CandidateClass = "superseded_retention"
	ClassDuplicateCandidate  CandidateClass = "duplicate_candidate"
	ClassStaleUnused         CandidateClass = "stale_unused_candidate"
	ClassMalformedMetadata   CandidateClass = "malformed_or_orphaned_metadata"
	ClassProtected           CandidateClass = "protected"
	ClassAlreadyQuarantined  CandidateClass = "already_quarantined"
)

type Scanner interface {
	ScrollAll(context.Context, map[string]interface{}, bool) ([]qdrant.ScrollPoint, error)
}

type Options struct {
	Collection          string
	Namespace           string
	ReferenceTime       time.Time
	SupersededRetention time.Duration
	StaleAfter          time.Duration
	LowRecallThreshold  int
}

type PolicySnapshot struct {
	SupersededRetentionSeconds int64 `json:"superseded_retention_seconds"`
	StaleAfterSeconds          int64 `json:"stale_after_seconds"`
	LowRecallThreshold         int   `json:"low_recall_threshold"`
}

type Finding struct {
	PointID                 string           `json:"point_id"`
	Classes                 []CandidateClass `json:"classes"`
	LifecycleState          string           `json:"lifecycle_state,omitempty"`
	MaintenanceStatus       Status           `json:"maintenance_status,omitempty"`
	LifecycleTransitionedAt string           `json:"lifecycle_transitioned_at,omitempty"`
	CreatedAt               string           `json:"created_at,omitempty"`
	UpdatedAt               string           `json:"updated_at,omitempty"`
	ValidUntil              string           `json:"valid_until,omitempty"`
	QuarantinedAt           string           `json:"quarantined_at,omitempty"`
	Protected               bool             `json:"protected"`
	EligibleForQuarantine   bool             `json:"eligible_for_quarantine"`
	Fingerprint             string           `json:"fingerprint"`
}

type Manifest struct {
	SchemaVersion int            `json:"schema_version"`
	PolicyVersion string         `json:"policy_version"`
	BatchID       string         `json:"batch_id"`
	Collection    string         `json:"collection"`
	Namespace     string         `json:"namespace,omitempty"`
	ReferenceTime string         `json:"reference_time"`
	Complete      bool           `json:"complete"`
	Scanned       int            `json:"scanned"`
	Policy        PolicySnapshot `json:"policy"`
	Findings      []Finding      `json:"findings"`
}

func Analyze(ctx context.Context, scanner Scanner, options Options) (Manifest, error) {
	if err := ValidateOptions(options); err != nil {
		return Manifest{}, err
	}
	filters := map[string]interface{}(nil)
	if options.Namespace != "" {
		filters = map[string]interface{}{"must": []map[string]interface{}{{"key": "namespace", "match": map[string]interface{}{"value": options.Namespace}}}}
	}
	points, err := scanner.ScrollAll(ctx, filters, false)
	if err != nil {
		return Manifest{}, fmt.Errorf("scan memory collection: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, fmt.Errorf("scan memory collection: %w", err)
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, PolicyVersion: PolicyVersion,
		Collection: options.Collection, Namespace: options.Namespace,
		ReferenceTime: options.ReferenceTime.UTC().Format(time.RFC3339), Complete: true, Scanned: len(points),
		Policy:   PolicySnapshot{SupersededRetentionSeconds: int64(options.SupersededRetention.Seconds()), StaleAfterSeconds: int64(options.StaleAfter.Seconds()), LowRecallThreshold: options.LowRecallThreshold},
		Findings: []Finding{},
	}
	idSet := make(map[string]struct{}, len(points))
	textIDs := make(map[string][]string)
	for _, point := range points {
		idSet[point.ID] = struct{}{}
		if text, ok := point.Payload["text"].(string); ok && text != "" {
			key := stringValue(point.Payload["namespace"]) + "\x00" + text
			textIDs[key] = append(textIDs[key], point.ID)
		}
	}
	duplicateIDs := map[string]bool{}
	for _, ids := range textIDs {
		if len(ids) > 1 {
			for _, id := range ids {
				duplicateIDs[id] = true
			}
		}
	}
	for _, point := range points {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		finding := classify(point, idSet, options.Namespace == "", duplicateIDs[point.ID], options)
		if len(finding.Classes) > 0 {
			manifest.Findings = append(manifest.Findings, finding)
		}
	}
	sort.Slice(manifest.Findings, func(i, j int) bool { return manifest.Findings[i].PointID < manifest.Findings[j].PointID })
	manifest.BatchID = batchID(manifest)
	return manifest, nil
}

func classify(point qdrant.ScrollPoint, ids map[string]struct{}, checkOrphans, duplicate bool, options Options) Finding {
	payload := point.Payload
	view := Parse(payload)
	life, _ := lifecycle.Parse(payload, point.ID)
	f := Finding{PointID: point.ID, Fingerprint: fingerprint(point)}
	malformed := !view.Valid || !life.Valid
	for _, key := range []string{"namespace", "created_at", "updated_at", "valid_until", "lifecycle_transitioned_at"} {
		if raw, present := payload[key]; present {
			if value, ok := raw.(string); !ok || value == "" {
				malformed = true
			}
		}
	}
	if raw, present := payload["permanent"]; present {
		if _, ok := raw.(bool); !ok {
			malformed = true
		}
	}
	if raw, present := payload["recall_count"]; present {
		switch raw.(type) {
		case float64, int:
		default:
			malformed = true
		}
	}
	if view.Valid && view.Status == Quarantined {
		f.Classes = append(f.Classes, ClassAlreadyQuarantined)
	}
	createdAtText := stringValue(payload["created_at"])
	updatedAtText := stringValue(payload["updated_at"])
	validUntilText := stringValue(payload["valid_until"])
	transitionedAtText := stringValue(payload["lifecycle_transitioned_at"])
	validUntil, validUntilOK := parseDate(validUntilText)
	if validUntilText != "" && !validUntilOK {
		malformed = true
	}
	if validUntilOK && utcDate(options.ReferenceTime).After(validUntil) {
		f.Classes = append(f.Classes, ClassExpired)
	}
	if life.Valid && life.State == lifecycle.Superseded {
		transitionedAt, ok := parseRFC3339(transitionedAtText)
		if !ok {
			malformed = true
		} else if !options.ReferenceTime.Before(transitionedAt.Add(options.SupersededRetention)) {
			f.Classes = append(f.Classes, ClassSupersededRetention)
		}
	}
	if createdAt, ok := parseRFC3339(createdAtText); createdAtText != "" && !ok {
		malformed = true
	} else if ok && !options.ReferenceTime.Before(createdAt.Add(options.StaleAfter)) && recallCount(payload["recall_count"]) <= options.LowRecallThreshold {
		f.Classes = append(f.Classes, ClassStaleUnused)
	}
	if _, ok := parseRFC3339(updatedAtText); updatedAtText != "" && !ok {
		malformed = true
	}
	if _, ok := parseRFC3339(transitionedAtText); transitionedAtText != "" && !ok {
		malformed = true
	}
	if duplicate {
		f.Classes = append(f.Classes, ClassDuplicateCandidate)
	}
	if checkOrphans && life.Valid {
		for _, related := range append(append([]string{}, life.Supersedes...), life.SupersededBy...) {
			if _, ok := ids[related]; !ok {
				malformed = true
			}
		}
	}
	if malformed {
		f.Classes = append(f.Classes, ClassMalformedMetadata)
	} else {
		f.LifecycleState = string(life.State)
		f.MaintenanceStatus = view.Status
		f.LifecycleTransitionedAt = transitionedAtText
		f.CreatedAt = createdAtText
		f.UpdatedAt = updatedAtText
		f.ValidUntil = validUntilText
		f.QuarantinedAt = view.QuarantinedAt
	}
	f.Protected = boolValue(payload["permanent"]) || (life.Valid && (life.State == lifecycle.Disputed || (life.State == lifecycle.Current && life.Canonical)))
	if f.Protected {
		f.Classes = append(f.Classes, ClassProtected)
	}
	f.Classes = uniqueSorted(f.Classes)
	f.EligibleForQuarantine = !f.Protected && view.Valid && view.Status == Active && !malformed && (contains(f.Classes, ClassExpired) || contains(f.Classes, ClassSupersededRetention))
	return f
}

func batchID(manifest Manifest) string {
	copy := manifest
	copy.BatchID = ""
	data, _ := json.Marshal(copy)
	sum := sha256.Sum256(data)
	return "maintenance-" + hex.EncodeToString(sum[:16])
}

func fingerprint(point qdrant.ScrollPoint) string {
	data, _ := json.Marshal(struct {
		ID      string                 `json:"id"`
		Payload map[string]interface{} `json:"payload"`
	}{point.ID, point.Payload})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func uniqueSorted(in []CandidateClass) []CandidateClass {
	seen := map[CandidateClass]bool{}
	out := []CandidateClass{}
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
func contains(in []CandidateClass, want CandidateClass) bool {
	for _, v := range in {
		if v == want {
			return true
		}
	}
	return false
}
func stringValue(v interface{}) string { s, _ := v.(string); return s }
func boolValue(v interface{}) bool     { b, _ := v.(bool); return b }
func recallCount(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}
func parseRFC3339(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, e := time.Parse(time.RFC3339, v)
	return t, e == nil
}
func parseDate(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, e := time.Parse("2006-01-02", v)
	return t, e == nil
}
func utcDate(v time.Time) time.Time {
	y, m, d := v.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
