package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

const (
	// MaxSelectionSize bounds a one-off maintenance mutation and its journal.
	MaxSelectionSize     = 100
	maxManifestFindings  = 10_000
	resultJournalTimeout = 5 * time.Second
)

type Operation string

const (
	OperationQuarantine Operation = "quarantine"
	OperationRestore    Operation = "restore"
	OperationPurge      Operation = "purge"
)

type OutcomeStatus string

const (
	OutcomeUpdated               OutcomeStatus = "updated"
	OutcomeDeleted               OutcomeStatus = "deleted"
	OutcomePending               OutcomeStatus = "pending"
	OutcomeDispatching           OutcomeStatus = "dispatching"
	OutcomeAlreadyApplied        OutcomeStatus = "already_applied"
	OutcomeNotFound              OutcomeStatus = "not_found"
	OutcomeProtectedOrIneligible OutcomeStatus = "protected_or_ineligible"
	OutcomeConflict              OutcomeStatus = "conflict"
	OutcomeFailed                OutcomeStatus = "failed"
	OutcomeAmbiguous             OutcomeStatus = "ambiguous"
)

// Selection is intentionally constrained to IDs already named by the saved
// manifest. IncludeEligibleFindings selects the manifest's eligible findings;
// PointIDs can narrow or add individual manifest findings.
type Selection struct {
	PointIDs                []string `json:"point_ids,omitempty"`
	IncludeEligibleFindings bool     `json:"include_eligible_findings,omitempty"`
}

type Request struct {
	Manifest    Manifest  `json:"manifest"`
	Selection   Selection `json:"selection"`
	JournalPath string    `json:"journal_path,omitempty"`
}

type PointOutcome struct {
	PointID string        `json:"point_id"`
	Status  OutcomeStatus `json:"status"`
}

// Result contains no source payload values. It is safe to persist as an audit
// journal and intentionally excludes collections, namespaces, vectors, and
// fact text.
type Result struct {
	SchemaVersion         int            `json:"schema_version"`
	PolicyVersion         string         `json:"policy_version"`
	BatchID               string         `json:"batch_id"`
	Operation             Operation      `json:"operation"`
	SnapshotName          string         `json:"snapshot_name,omitempty"`
	SnapshotArchiveSHA256 string         `json:"snapshot_archive_sha256,omitempty"`
	Outcomes              []PointOutcome `json:"outcomes"`
	Timestamp             string         `json:"timestamp"`
}

// PointStore is the narrowly scoped Qdrant surface required by this service.
// It deliberately has no vector or generic payload write operation.
type PointStore interface {
	CollectionName() string
	Get(context.Context, string) (qdrant.Point, bool, error)
	QuarantineMaintenance(context.Context, string, time.Time, qdrant.MaintenanceReason, string) error
	RestoreMaintenance(context.Context, string) error
}

type Service struct {
	points       PointStore
	collection   string
	now          func() time.Time
	invalidate   func()
	writeJournal func(context.Context, string, Result) error
}

func NewService(points PointStore, collection string, invalidate func()) (*Service, error) {
	if points == nil {
		return nil, fmt.Errorf("point store is required")
	}
	if strings.TrimSpace(collection) == "" {
		collection = points.CollectionName()
	}
	if strings.TrimSpace(collection) == "" {
		return nil, fmt.Errorf("collection is required")
	}
	if points.CollectionName() != "" && collection != points.CollectionName() {
		return nil, fmt.Errorf("configured collection does not match point store")
	}
	return &Service{points: points, collection: collection, now: time.Now, invalidate: invalidate, writeJournal: WriteResultJournal}, nil
}

func (s *Service) Quarantine(ctx context.Context, request Request) (Result, error) {
	return s.run(ctx, OperationQuarantine, request)
}

func (s *Service) Restore(ctx context.Context, request Request) (Result, error) {
	return s.run(ctx, OperationRestore, request)
}

func (s *Service) run(ctx context.Context, operation Operation, request Request) (Result, error) {
	if s == nil || s.points == nil {
		return Result{}, fmt.Errorf("maintenance service is not configured")
	}
	findings, ids, err := validateRequest(request, s.collection)
	if err != nil {
		return Result{}, err
	}
	now := s.now().UTC().Format(time.RFC3339)
	result := Result{
		SchemaVersion: ManifestSchemaVersion,
		PolicyVersion: PolicyVersion,
		BatchID:       request.Manifest.BatchID,
		Operation:     operation,
		Outcomes:      make([]PointOutcome, 0, len(ids)),
		Timestamp:     now,
	}
	dispatched := false
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			result.Outcomes = append(result.Outcomes, PointOutcome{PointID: id, Status: OutcomeFailed})
			continue
		}
		finding := findings[id]
		outcome, mutationDispatched := s.apply(ctx, operation, finding, request.Manifest.BatchID, now)
		dispatched = dispatched || mutationDispatched
		result.Outcomes = append(result.Outcomes, PointOutcome{PointID: id, Status: outcome})
	}
	// A response failure after dispatch is ambiguous; invalidating is safe and
	// required because Qdrant could have committed the payload batch.
	if dispatched && s.invalidate != nil {
		s.invalidate()
	}
	if request.JournalPath != "" {
		journalCtx := ctx
		if dispatched {
			// Once a mutation was dispatched, cancellation makes its outcome
			// ambiguous. Preserve that audit record with a separate bounded
			// cleanup context instead of inheriting the canceled request.
			var cancel context.CancelFunc
			journalCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), resultJournalTimeout)
			defer cancel()
		}
		writer := s.writeJournal
		if writer == nil {
			writer = WriteResultJournal
		}
		if err := writer(journalCtx, request.JournalPath, result); err != nil {
			return result, fmt.Errorf("write maintenance result journal: %w", err)
		}
	}
	return result, nil
}

func (s *Service) apply(ctx context.Context, operation Operation, finding Finding, batchID, at string) (OutcomeStatus, bool) {
	if !isEligibleFinding(finding) {
		return OutcomeProtectedOrIneligible, false
	}
	point, found, err := s.points.Get(ctx, finding.PointID)
	if err != nil {
		return OutcomeFailed, false
	}
	if !found || point.ID != finding.PointID {
		return OutcomeNotFound, false
	}
	view := Parse(point.Payload)
	if !view.Valid {
		return OutcomeConflict, false
	}
	reason := quarantineReason(finding)
	switch operation {
	case OperationQuarantine:
		if view.Status == Quarantined {
			if view.QuarantineBatchID == batchID && view.QuarantineReason == reason && matchesFinding(point, finding, true) {
				return OutcomeAlreadyApplied, false
			}
			return OutcomeConflict, false
		}
		if !matchesFinding(point, finding, false) {
			return OutcomeConflict, false
		}
		atTime, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return OutcomeFailed, false
		}
		if err := s.points.QuarantineMaintenance(ctx, finding.PointID, atTime, qdrant.MaintenanceReason(reason), batchID); err != nil {
			return OutcomeAmbiguous, true
		}
		if !s.verifyQuarantine(ctx, finding, at, reason, batchID) {
			return OutcomeAmbiguous, true
		}
		return OutcomeUpdated, true
	case OperationRestore:
		if view.Status == Active {
			if matchesFinding(point, finding, true) {
				return OutcomeAlreadyApplied, false
			}
			return OutcomeConflict, false
		}
		if view.QuarantineBatchID != batchID || view.QuarantineReason != reason || !matchesFinding(point, finding, true) {
			return OutcomeConflict, false
		}
		if err := s.points.RestoreMaintenance(ctx, finding.PointID); err != nil {
			return OutcomeAmbiguous, true
		}
		if !s.verifyRestore(ctx, finding) {
			return OutcomeAmbiguous, true
		}
		return OutcomeUpdated, true
	default:
		return OutcomeFailed, false
	}
}

// Qdrant does not offer an atomic compare-and-set for an arbitrary payload
// fingerprint. A writer may therefore race this narrow payload batch; an
// exact post-write read turns that residual race into explicit ambiguity
// instead of claiming success. No source payload is surfaced to callers.
func (s *Service) verifyQuarantine(ctx context.Context, finding Finding, at, reason, batchID string) bool {
	point, found, err := s.points.Get(ctx, finding.PointID)
	if err != nil || !found || point.ID != finding.PointID || !matchesFinding(point, finding, true) {
		return false
	}
	view := Parse(point.Payload)
	return view.Valid && view.Status == Quarantined && view.QuarantinedAt == at && view.QuarantineReason == reason && view.QuarantineBatchID == batchID
}

func (s *Service) verifyRestore(ctx context.Context, finding Finding) bool {
	point, found, err := s.points.Get(ctx, finding.PointID)
	if err != nil || !found || point.ID != finding.PointID || !matchesFinding(point, finding, true) {
		return false
	}
	view := Parse(point.Payload)
	status, explicitStatus := point.Payload["maintenance_status"].(string)
	_, hasAt := point.Payload["quarantined_at"]
	_, hasReason := point.Payload["quarantine_reason"]
	_, hasBatch := point.Payload["quarantine_batch_id"]
	return view.Valid && view.Status == Active && explicitStatus && status == string(Active) && !hasAt && !hasReason && !hasBatch
}

func validateRequest(request Request, collection string) (map[string]Finding, []string, error) {
	manifest := request.Manifest
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.PolicyVersion != PolicyVersion {
		return nil, nil, fmt.Errorf("manifest schema or policy version is incompatible")
	}
	if !manifest.Complete || strings.TrimSpace(manifest.BatchID) == "" {
		return nil, nil, fmt.Errorf("manifest must be complete and actionable")
	}
	if manifest.Collection != collection {
		return nil, nil, fmt.Errorf("manifest collection is incompatible")
	}
	if len(manifest.Findings) == 0 || len(manifest.Findings) > maxManifestFindings {
		return nil, nil, fmt.Errorf("manifest findings must be non-empty and bounded")
	}
	if _, err := time.Parse(time.RFC3339, manifest.ReferenceTime); err != nil {
		return nil, nil, fmt.Errorf("manifest reference time is invalid")
	}
	if batchID(manifest) != manifest.BatchID {
		return nil, nil, fmt.Errorf("manifest batch ID does not match its contents")
	}
	findings := make(map[string]Finding, len(manifest.Findings))
	for _, finding := range manifest.Findings {
		if strings.TrimSpace(finding.PointID) == "" || !validFingerprint(finding.Fingerprint) {
			return nil, nil, fmt.Errorf("manifest finding is invalid")
		}
		if _, duplicate := findings[finding.PointID]; duplicate {
			return nil, nil, fmt.Errorf("manifest contains duplicate point IDs")
		}
		findings[finding.PointID] = finding
	}
	selected := make(map[string]struct{}, len(request.Selection.PointIDs))
	if request.Selection.IncludeEligibleFindings {
		for id, finding := range findings {
			if finding.EligibleForQuarantine {
				selected[id] = struct{}{}
			}
		}
	}
	explicit := make(map[string]struct{}, len(request.Selection.PointIDs))
	for _, id := range request.Selection.PointIDs {
		if strings.TrimSpace(id) == "" {
			return nil, nil, fmt.Errorf("selection point ID is required")
		}
		if _, found := findings[id]; !found {
			return nil, nil, fmt.Errorf("selection point ID is not in manifest")
		}
		if _, duplicate := explicit[id]; duplicate {
			return nil, nil, fmt.Errorf("selection contains duplicate point IDs")
		}
		explicit[id] = struct{}{}
		selected[id] = struct{}{}
	}
	if len(selected) == 0 || len(selected) > MaxSelectionSize {
		return nil, nil, fmt.Errorf("selection must be non-empty and bounded")
	}
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return findings, ids, nil
}

func isEligibleFinding(f Finding) bool {
	return f.EligibleForQuarantine && !f.Protected && !contains(f.Classes, ClassMalformedMetadata) && !contains(f.Classes, ClassAlreadyQuarantined) && (contains(f.Classes, ClassExpired) || contains(f.Classes, ClassSupersededRetention))
}

func quarantineReason(f Finding) string {
	if contains(f.Classes, ClassExpired) {
		return string(ClassExpired)
	}
	return string(ClassSupersededRetention)
}

// matchesFinding compares the original fingerprint. For a record that has
// already been transitioned by this service it additionally permits only the
// exact, expected maintenance-field projection; all other payload and
// lifecycle fields still participate in the PR-A fingerprint.
func matchesFinding(point qdrant.Point, finding Finding, allowMaintenanceProjection bool) bool {
	if fingerprint(qdrant.ScrollPoint{ID: point.ID, Payload: point.Payload}) == finding.Fingerprint {
		return true
	}
	if !allowMaintenanceProjection {
		return false
	}
	for _, legacy := range []bool{true, false} {
		payload := clonePayload(point.Payload)
		for key := range maintenancePayloadKeysForService {
			delete(payload, key)
		}
		if !legacy {
			payload["maintenance_status"] = string(Active)
		}
		if fingerprint(qdrant.ScrollPoint{ID: point.ID, Payload: payload}) == finding.Fingerprint {
			return true
		}
	}
	return false
}

var maintenancePayloadKeysForService = map[string]struct{}{
	"maintenance_status": {}, "quarantined_at": {}, "quarantine_reason": {}, "quarantine_batch_id": {},
}

func clonePayload(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func validFingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// WriteResultJournal atomically replaces a private result journal. The journal
// contains only the content-free Result shape and is bounded by the service.
func WriteResultJournal(ctx context.Context, path string, result Result) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("journal path is required")
	}
	if len(result.Outcomes) > MaxSelectionSize {
		return fmt.Errorf("journal outcomes exceed maximum")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode result journal: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(filepath.Clean(path))
	file, err := os.CreateTemp(dir, ".maintenance-result-*")
	if err != nil {
		return fmt.Errorf("create result journal temp file: %w", err)
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("set result journal permissions: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write result journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync result journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close result journal: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, filepath.Clean(path)); err != nil {
		return fmt.Errorf("replace result journal: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open result journal directory: %w", err)
	}
	err = directory.Sync()
	_ = directory.Close()
	if err != nil {
		return fmt.Errorf("sync result journal directory: %w", err)
	}
	return nil
}
