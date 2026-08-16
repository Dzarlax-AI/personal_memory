package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

const maxPurgeQuarantineAge = 36500 * 24 * time.Hour

type PurgeRequest struct {
	Manifest             Manifest      `json:"manifest"`
	Selection            Selection     `json:"selection"`
	JournalPath          string        `json:"journal_path,omitempty"`
	SnapshotArchivePath  string        `json:"snapshot_archive_path,omitempty"`
	MinimumQuarantineAge time.Duration `json:"-"`
}

type purgePointStore interface {
	PointStore
	CreateSnapshotIdentity(context.Context) (qdrant.SnapshotIdentity, error)
	ListSnapshotIdentities(context.Context) ([]qdrant.SnapshotIdentity, error)
	EnsureSnapshotArchive(context.Context, qdrant.SnapshotIdentity, string, string) (string, error)
	DeleteExactStrong(context.Context, string) error
}

// Purge permanently removes only explicitly selected, manifest-bound points.
// It is intentionally unavailable through MCP and requires an operator-created
// fresh snapshot proof in the same invocation before any exact-ID deletion.
func (s *Service) Purge(ctx context.Context, request PurgeRequest) (Result, error) {
	if s == nil || s.points == nil {
		return Result{}, fmt.Errorf("purge point store is not configured")
	}
	store, ok := s.points.(purgePointStore)
	if !ok {
		return Result{}, fmt.Errorf("purge point store is not configured")
	}
	if request.MinimumQuarantineAge <= 0 || request.MinimumQuarantineAge > maxPurgeQuarantineAge {
		return Result{}, fmt.Errorf("minimum quarantine age must be positive and bounded")
	}
	if request.Selection.IncludeEligibleFindings {
		return Result{}, fmt.Errorf("purge requires explicit point IDs")
	}
	if strings.TrimSpace(request.JournalPath) == "" {
		return Result{}, fmt.Errorf("purge journal path is required")
	}
	if strings.TrimSpace(request.SnapshotArchivePath) == "" {
		return Result{}, fmt.Errorf("purge snapshot archive path is required")
	}
	if filepath.Clean(request.JournalPath) == filepath.Clean(request.SnapshotArchivePath) {
		return Result{}, fmt.Errorf("purge journal and snapshot archive paths must differ")
	}
	findings, ids, err := validateRequest(Request{Manifest: request.Manifest, Selection: request.Selection}, s.collection)
	if err != nil {
		return Result{}, err
	}
	prior, resuming, err := readCompatiblePurgeJournal(request.JournalPath, request.Manifest, ids)
	if err != nil {
		return Result{}, err
	}
	resumeEvidence := map[string]bool{}
	priorStatuses := map[string]OutcomeStatus{}
	hasResumeEvidence := false
	if resuming {
		for _, outcome := range prior.Outcomes {
			priorStatuses[outcome.PointID] = outcome.Status
			switch outcome.Status {
			case OutcomeDispatching, OutcomeDeleted, OutcomeAmbiguous, OutcomeAlreadyApplied:
				resumeEvidence[outcome.PointID] = true
				hasResumeEvidence = true
			}
		}
	}
	now := s.now().UTC()
	result := Result{
		SchemaVersion: ManifestSchemaVersion,
		PolicyVersion: PolicyVersion,
		BatchID:       request.Manifest.BatchID,
		Operation:     OperationPurge,
		Outcomes:      make([]PointOutcome, 0, len(ids)),
		Timestamp:     now.Format(time.RFC3339),
	}

	var snapshot qdrant.SnapshotIdentity
	mutationDispatched := false
	if resuming {
		// A retry remains bound to the original pre-delete recovery point. It must
		// never replace that evidence with a snapshot taken after partial deletion.
		snapshot = qdrant.SnapshotIdentity{Name: prior.SnapshotName}
	} else {
		// Snapshot creation is itself a dispatched mutation. From this point on a
		// cancellation cannot safely erase the resulting audit record.
		var snapshotErr error
		snapshot, snapshotErr = store.CreateSnapshotIdentity(ctx)
		mutationDispatched = true
		result.SnapshotName = snapshot.Name
		if snapshotErr != nil || strings.TrimSpace(snapshot.Name) == "" {
			appendStatusForIDs(&result, ids, OutcomeFailed)
			return s.finishPurge(ctx, request.JournalPath, result, mutationDispatched)
		}
	}
	result.SnapshotName = snapshot.Name
	if resuming {
		// Bind every recovery attempt to the original archive before any remote
		// proof. A failed preflight must never erase this durable identity.
		result.SnapshotArchiveSHA256 = prior.SnapshotArchiveSHA256
	}
	for _, id := range ids {
		status := OutcomePending
		if priorStatus, ok := priorStatuses[id]; ok {
			status = priorStatus
		}
		result.Outcomes = append(result.Outcomes, PointOutcome{PointID: id, Status: status})
	}
	listed, err := store.ListSnapshotIdentities(ctx)
	if err != nil || !containsSnapshot(listed, snapshot) {
		if resuming {
			if checkpointErr := s.checkpointPurge(ctx, request.JournalPath, result, mutationDispatched || hasResumeEvidence); checkpointErr != nil {
				return result, checkpointErr
			}
			return result, fmt.Errorf("original purge snapshot could not be proved")
		}
		for index := range result.Outcomes {
			result.Outcomes[index].Status = OutcomeFailed
		}
		return s.finishPurge(ctx, request.JournalPath, result, mutationDispatched)
	}
	expectedArchiveSHA256 := result.SnapshotArchiveSHA256
	archiveSHA256, err := store.EnsureSnapshotArchive(ctx, snapshot, request.SnapshotArchivePath, expectedArchiveSHA256)
	if err != nil {
		if !resuming {
			for index := range result.Outcomes {
				result.Outcomes[index].Status = OutcomeFailed
			}
		}
		return s.finishPurge(ctx, request.JournalPath, result, mutationDispatched || hasResumeEvidence)
	}
	result.SnapshotArchiveSHA256 = archiveSHA256
	// Persist the original snapshot and exact complete selection before the
	// first destructive dispatch. The immutable archive lives outside Qdrant's
	// ordinary snapshot rotation; failure here guarantees zero deletes.
	if err := s.checkpointPurge(ctx, request.JournalPath, result, mutationDispatched || hasResumeEvidence); err != nil {
		return result, err
	}

	deleteDispatched := false
	for index, id := range ids {
		finding := findings[id]
		if ctx.Err() != nil {
			if !resumeEvidence[id] {
				result.Outcomes[index].Status = OutcomeFailed
			}
			if err := s.checkpointPurge(ctx, request.JournalPath, result, mutationDispatched || deleteDispatched || hasResumeEvidence); err != nil {
				return result, err
			}
			continue
		}
		beforeDelete := func() error {
			result.Outcomes[index].Status = OutcomeDispatching
			return s.checkpointPurge(ctx, request.JournalPath, result, mutationDispatched || deleteDispatched)
		}
		outcome, didDelete, err := s.purgeOne(ctx, store, finding, request.Manifest.BatchID, snapshot, now, request.MinimumQuarantineAge, resumeEvidence[id], beforeDelete)
		if err != nil {
			return result, err
		}
		deleteDispatched = deleteDispatched || didDelete
		result.Outcomes[index].Status = outcome
		if err := s.checkpointPurge(ctx, request.JournalPath, result, mutationDispatched || deleteDispatched); err != nil {
			if deleteDispatched && s.invalidate != nil {
				s.invalidate()
			}
			return result, err
		}
	}
	if deleteDispatched && s.invalidate != nil {
		s.invalidate()
	}
	return result, nil
}

func (s *Service) purgeOne(ctx context.Context, store purgePointStore, finding Finding, batchID string, snapshot qdrant.SnapshotIdentity, now time.Time, minimumAge time.Duration, resumeEvidence bool, beforeDelete func() error) (OutcomeStatus, bool, error) {
	if !isEligibleFinding(finding) {
		return OutcomeProtectedOrIneligible, false, nil
	}
	point, found, err := store.Get(ctx, finding.PointID)
	if err != nil {
		return OutcomeFailed, false, nil //nolint:nilerr // Closed outcomes intentionally hide store details.
	}
	if !found {
		if resumeEvidence {
			return OutcomeAlreadyApplied, false, nil
		}
		return OutcomeNotFound, false, nil
	}
	if point.ID != finding.PointID {
		return OutcomeConflict, false, nil
	}
	view := Parse(point.Payload)
	if !view.Valid || view.Status != Quarantined || view.QuarantineBatchID != batchID || view.QuarantineReason != quarantineReason(finding) {
		return OutcomeConflict, false, nil
	}
	quarantinedAt, err := time.Parse(time.RFC3339, view.QuarantinedAt)
	if err != nil || quarantinedAt.After(now) || now.Sub(quarantinedAt) < minimumAge {
		return OutcomeProtectedOrIneligible, false, nil //nolint:nilerr // Invalid metadata is a closed ineligible outcome.
	}
	if !matchesFinding(point, finding, true) {
		return OutcomeConflict, false, nil
	}

	// Re-prove the exact snapshot identity immediately before every deletion.
	// A failed or missing proof is a closed refusal and never reaches Delete.
	snapshots, err := store.ListSnapshotIdentities(ctx)
	if err != nil || !containsSnapshot(snapshots, snapshot) {
		return OutcomeFailed, false, nil //nolint:nilerr // Snapshot proof failures stay content-free.
	}
	// Re-read after the proof so payload drift cannot silently bypass the gates.
	point, found, err = store.Get(ctx, finding.PointID)
	if err != nil {
		return OutcomeFailed, false, nil //nolint:nilerr // Closed outcomes intentionally hide store details.
	}
	if !found {
		if resumeEvidence {
			return OutcomeAlreadyApplied, false, nil
		}
		return OutcomeNotFound, false, nil
	}
	view = Parse(point.Payload)
	if point.ID != finding.PointID || !view.Valid || view.Status != Quarantined || view.QuarantineBatchID != batchID || view.QuarantineReason != quarantineReason(finding) || !matchesFinding(point, finding, true) {
		return OutcomeConflict, false, nil
	}
	quarantinedAt, err = time.Parse(time.RFC3339, view.QuarantinedAt)
	if err != nil || quarantinedAt.After(now) || now.Sub(quarantinedAt) < minimumAge {
		return OutcomeProtectedOrIneligible, false, nil //nolint:nilerr // Invalid metadata is a closed ineligible outcome.
	}
	if err := beforeDelete(); err != nil {
		return OutcomePending, false, err
	}
	if err := store.DeleteExactStrong(ctx, finding.PointID); err != nil {
		return OutcomeAmbiguous, true, nil //nolint:nilerr // A dispatched delete is intentionally reported as ambiguous.
	}
	_, found, err = store.Get(ctx, finding.PointID)
	if err != nil || found {
		return OutcomeAmbiguous, true, nil //nolint:nilerr // Post-delete verification is intentionally content-free.
	}
	return OutcomeDeleted, true, nil
}

// readCompatiblePurgeJournal treats an existing journal as recovery state, not
// disposable output. Unknown, malformed, or differently scoped evidence fails
// before snapshot creation or deletion.
func readCompatiblePurgeJournal(path string, manifest Manifest, selectedIDs []string) (Result, bool, error) {
	if strings.TrimSpace(path) == "" {
		return Result{}, false, nil
	}
	file, err := os.Open(filepath.Clean(path))
	if os.IsNotExist(err) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, fmt.Errorf("open purge journal")
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, MaxManifestBytes+1))
	if err != nil || int64(len(data)) > MaxManifestBytes {
		return Result{}, false, fmt.Errorf("read purge journal")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result Result
	if err := decoder.Decode(&result); err != nil {
		return Result{}, false, fmt.Errorf("decode purge journal")
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Result{}, false, fmt.Errorf("decode purge journal")
	}
	if result.SchemaVersion != ManifestSchemaVersion || result.PolicyVersion != PolicyVersion || result.BatchID != manifest.BatchID || result.Operation != OperationPurge {
		return Result{}, false, fmt.Errorf("purge journal is incompatible")
	}
	if _, err := time.Parse(time.RFC3339, result.Timestamp); err != nil || len(result.Outcomes) != len(selectedIDs) {
		return Result{}, false, fmt.Errorf("purge journal is incompatible")
	}
	selected := make(map[string]struct{}, len(selectedIDs))
	for _, id := range selectedIDs {
		selected[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(result.Outcomes))
	for _, outcome := range result.Outcomes {
		if _, ok := selected[outcome.PointID]; !ok {
			return Result{}, false, fmt.Errorf("purge journal is incompatible")
		}
		if _, duplicate := seen[outcome.PointID]; duplicate {
			return Result{}, false, fmt.Errorf("purge journal is incompatible")
		}
		seen[outcome.PointID] = struct{}{}
		switch outcome.Status {
		case OutcomePending, OutcomeDispatching, OutcomeDeleted, OutcomeAlreadyApplied, OutcomeNotFound, OutcomeProtectedOrIneligible, OutcomeConflict, OutcomeFailed, OutcomeAmbiguous:
		default:
			return Result{}, false, fmt.Errorf("purge journal is incompatible")
		}
	}
	if strings.TrimSpace(result.SnapshotName) == "" {
		for _, outcome := range result.Outcomes {
			if outcome.Status != OutcomeFailed {
				return Result{}, false, fmt.Errorf("purge journal is incompatible")
			}
		}
		return result, false, nil
	}
	if result.SnapshotArchiveSHA256 == "" {
		for _, outcome := range result.Outcomes {
			if outcome.Status != OutcomeFailed {
				return Result{}, false, fmt.Errorf("purge journal is incompatible")
			}
		}
		return result, true, nil
	}
	if len(result.SnapshotArchiveSHA256) != 64 || strings.ToLower(result.SnapshotArchiveSHA256) != result.SnapshotArchiveSHA256 {
		return Result{}, false, fmt.Errorf("purge journal is incompatible")
	}
	return result, true, nil
}

func (s *Service) finishPurge(ctx context.Context, journalPath string, result Result, dispatched bool) (Result, error) {
	if err := s.checkpointPurge(ctx, journalPath, result, dispatched); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) checkpointPurge(ctx context.Context, journalPath string, result Result, dispatched bool) error {
	if journalPath == "" {
		return nil
	}
	journalCtx := ctx
	if dispatched {
		var cancel context.CancelFunc
		journalCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), resultJournalTimeout)
		defer cancel()
	}
	writer := s.writeJournal
	if writer == nil {
		writer = WriteResultJournal
	}
	if err := writer(journalCtx, journalPath, result); err != nil {
		return fmt.Errorf("write maintenance result journal: %w", err)
	}
	return nil
}

func appendStatusForIDs(result *Result, ids []string, status OutcomeStatus) {
	for _, id := range ids {
		result.Outcomes = append(result.Outcomes, PointOutcome{PointID: id, Status: status})
	}
}

func containsSnapshot(snapshots []qdrant.SnapshotIdentity, want qdrant.SnapshotIdentity) bool {
	for _, snapshot := range snapshots {
		if snapshot.Name == want.Name && snapshot.Name != "" {
			return true
		}
	}
	return false
}
