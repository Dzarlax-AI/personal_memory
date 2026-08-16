package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func TestPurgeRequiresVerifiedSnapshotBeforeAnyDelete(t *testing.T) {
	for _, tc := range []struct {
		name          string
		breakSnapshot func(*fakePurgeStore)
	}{
		{"creation failure", func(store *fakePurgeStore) { store.createErr = errors.New("unavailable") }},
		{"created identity absent", func(store *fakePurgeStore) { store.snapshots = nil }},
		{"list failure", func(store *fakePurgeStore) { store.listErr = errors.New("unavailable") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			point := quarantinedPoint("42", "2026-07-01T00:00:00Z")
			finding := eligibleExpired(point.ID, qdrant.Point{ID: point.ID, Payload: underlyingPayload(point.Payload)})
			manifest := manifestFor(finding)
			point.Payload["quarantine_batch_id"] = manifest.BatchID
			store := newFakePurgeStore(map[string]qdrant.Point{point.ID: point})
			tc.breakSnapshot(store)
			service, err := NewService(store, "memory", nil)
			if err != nil {
				t.Fatal(err)
			}
			journal := filepath.Join(t.TempDir(), "purge.json")
			result, err := service.Purge(context.Background(), purgeRequest(manifest, []string{point.ID}, journal))
			if err != nil {
				t.Fatal(err)
			}
			if len(store.deleted) != 0 || result.Outcomes[0].Status != OutcomeFailed {
				t.Fatalf("deleted=%v result=%#v", store.deleted, result)
			}
		})
	}
}

func TestPurgeRetriesPreSnapshotFailureWithSameJournal(t *testing.T) {
	point := quarantinedPoint("42", "2026-07-01T00:00:00Z")
	finding := eligibleExpired(point.ID, qdrant.Point{ID: point.ID, Payload: underlyingPayload(point.Payload)})
	manifest := manifestFor(finding)
	point.Payload["quarantine_batch_id"] = manifest.BatchID
	store := newFakePurgeStore(map[string]qdrant.Point{point.ID: point})
	created := store.created
	store.created = qdrant.SnapshotIdentity{}
	store.createErr = errors.New("temporarily unavailable")
	service, _ := NewService(store, "memory", nil)
	journal := filepath.Join(t.TempDir(), "purge.json")

	first, err := service.Purge(context.Background(), purgeRequest(manifest, []string{point.ID}, journal))
	if err != nil || first.Outcomes[0].Status != OutcomeFailed || len(store.deleted) != 0 {
		t.Fatalf("first=%#v deleted=%v err=%v", first, store.deleted, err)
	}
	store.created = created
	store.createErr = nil
	retry, err := service.Purge(context.Background(), purgeRequest(manifest, []string{point.ID}, journal))
	if err != nil || retry.Outcomes[0].Status != OutcomeDeleted {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
	if store.createCalls != 2 || store.archiveCalls != 1 {
		t.Fatalf("snapshot creates=%d archives=%d", store.createCalls, store.archiveCalls)
	}
}

func TestPurgeRetriesArchiveFailureWithoutReplacingSnapshot(t *testing.T) {
	point := quarantinedPoint("42", "2026-07-01T00:00:00Z")
	finding := eligibleExpired(point.ID, qdrant.Point{ID: point.ID, Payload: underlyingPayload(point.Payload)})
	manifest := manifestFor(finding)
	point.Payload["quarantine_batch_id"] = manifest.BatchID
	store := newFakePurgeStore(map[string]qdrant.Point{point.ID: point})
	store.archiveErr = errors.New("archive unavailable")
	service, _ := NewService(store, "memory", nil)
	journal := filepath.Join(t.TempDir(), "purge.json")

	first, err := service.Purge(context.Background(), purgeRequest(manifest, []string{point.ID}, journal))
	if err != nil || first.Outcomes[0].Status != OutcomeFailed || len(store.deleted) != 0 {
		t.Fatalf("first=%#v deleted=%v err=%v", first, store.deleted, err)
	}
	store.archiveErr = nil
	retry, err := service.Purge(context.Background(), purgeRequest(manifest, []string{point.ID}, journal))
	if err != nil || retry.Outcomes[0].Status != OutcomeDeleted {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
	if store.createCalls != 1 || store.archiveCalls != 2 {
		t.Fatalf("snapshot creates=%d archives=%d", store.createCalls, store.archiveCalls)
	}
}

func TestPurgePartialRetryAndPrivacy(t *testing.T) {
	first := quarantinedPoint("42", "2026-07-01T00:00:00Z")
	second := quarantinedPoint("legacy-text-id", "2026-07-01T00:00:00Z")
	findings := []Finding{
		eligibleExpired(first.ID, qdrant.Point{ID: first.ID, Payload: underlyingPayload(first.Payload)}),
		eligibleExpired(second.ID, qdrant.Point{ID: second.ID, Payload: underlyingPayload(second.Payload)}),
	}
	manifest := manifestWithFindings(findings)
	first.Payload["quarantine_batch_id"], second.Payload["quarantine_batch_id"] = manifest.BatchID, manifest.BatchID
	store := newFakePurgeStore(map[string]qdrant.Point{first.ID: first, second.ID: second})
	store.deleteErrors[second.ID] = errors.New("secret delete failure")
	journal := filepath.Join(t.TempDir(), "purge.json")
	service, _ := NewService(store, "memory", nil)
	result, err := service.Purge(context.Background(), purgeRequest(manifest, []string{first.ID, second.ID}, journal))
	if err != nil {
		t.Fatal(err)
	}
	got := outcomesByID(result)
	if got[first.ID] != OutcomeDeleted || got[second.ID] != OutcomeAmbiguous {
		t.Fatalf("outcomes=%v", got)
	}
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "fact text") {
		t.Fatalf("journal leaked content: %s", data)
	}
	if store.listCalls < 3 {
		t.Fatalf("snapshot was not re-proved before both deletes: list calls=%d", store.listCalls)
	}
	store.archiveErr = errors.New("archive temporarily unavailable")
	archiveFailure, err := service.Purge(context.Background(), purgeRequest(manifest, []string{first.ID, second.ID}, journal))
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomesByID(archiveFailure); got[first.ID] != OutcomeDeleted || got[second.ID] != OutcomeAmbiguous || archiveFailure.SnapshotArchiveSHA256 != result.SnapshotArchiveSHA256 {
		t.Fatalf("archive failure lost evidence: result=%#v", archiveFailure)
	}
	store.archiveErr = nil
	store.listErr = errors.New("snapshot list temporarily unavailable")
	if _, err := service.Purge(context.Background(), purgeRequest(manifest, []string{first.ID, second.ID}, journal)); err == nil {
		t.Fatal("snapshot list failure unexpectedly succeeded")
	}
	preserved, resuming, err := readCompatiblePurgeJournal(journal, manifest, []string{first.ID, second.ID})
	if err != nil || !resuming || outcomesByID(preserved)[first.ID] != OutcomeDeleted || outcomesByID(preserved)[second.ID] != OutcomeAmbiguous || preserved.SnapshotArchiveSHA256 != result.SnapshotArchiveSHA256 {
		t.Fatalf("snapshot list failure lost evidence: result=%#v resuming=%v err=%v", preserved, resuming, err)
	}
	store.listErr = nil
	delete(store.deleteErrors, second.ID)
	retry, err := service.Purge(context.Background(), purgeRequest(manifest, []string{first.ID, second.ID}, journal))
	if err != nil {
		t.Fatal(err)
	}
	got = outcomesByID(retry)
	if got[first.ID] != OutcomeAlreadyApplied || got[second.ID] != OutcomeDeleted {
		t.Fatalf("retry outcomes=%v", got)
	}
	if store.createCalls != 1 {
		t.Fatalf("retry created a post-delete snapshot: calls=%d", store.createCalls)
	}
	rewritten, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rewritten), `"snapshot_name": "fresh.snapshot"`) {
		t.Fatalf("original snapshot was not preserved: %s", rewritten)
	}
}

func TestPurgeDoesNotTreatArbitraryAbsenceAsAlreadyApplied(t *testing.T) {
	point := quarantinedPoint("42", "2026-07-01T00:00:00Z")
	finding := eligibleExpired(point.ID, qdrant.Point{ID: point.ID, Payload: underlyingPayload(point.Payload)})
	manifest := manifestFor(finding)
	store := newFakePurgeStore(map[string]qdrant.Point{})
	service, _ := NewService(store, "memory", nil)
	journal := filepath.Join(t.TempDir(), "purge.json")
	result, err := service.Purge(context.Background(), purgeRequest(manifest, []string{point.ID}, journal))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcomes[0].Status != OutcomeNotFound || len(store.deleted) != 0 {
		t.Fatalf("result=%#v deleted=%v", result, store.deleted)
	}
}

func TestPurgeRejectsIncompatibleExistingJournalBeforeSnapshotOrDelete(t *testing.T) {
	point := quarantinedPoint("42", "2026-07-01T00:00:00Z")
	finding := eligibleExpired(point.ID, qdrant.Point{ID: point.ID, Payload: underlyingPayload(point.Payload)})
	manifest := manifestFor(finding)
	point.Payload["quarantine_batch_id"] = manifest.BatchID
	store := newFakePurgeStore(map[string]qdrant.Point{point.ID: point})
	journal := filepath.Join(t.TempDir(), "purge.json")
	if err := os.WriteFile(journal, []byte(`{"schema_version":1,"policy_version":"wrong","batch_id":"wrong","operation":"purge","snapshot_name":"old.snapshot","outcomes":[{"point_id":"42","status":"ambiguous"}],"timestamp":"2026-08-14T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(store, "memory", nil)
	if _, err := service.Purge(context.Background(), purgeRequest(manifest, []string{point.ID}, journal)); err == nil {
		t.Fatal("accepted incompatible journal")
	}
	if store.createCalls != 0 || len(store.deleted) != 0 {
		t.Fatalf("create=%d deleted=%v", store.createCalls, store.deleted)
	}
}

func TestPurgeRejectsMalformedExistingJournalBeforeSnapshotOrDelete(t *testing.T) {
	point := quarantinedPoint("42", "2026-07-01T00:00:00Z")
	finding := eligibleExpired(point.ID, qdrant.Point{ID: point.ID, Payload: underlyingPayload(point.Payload)})
	manifest := manifestFor(finding)
	point.Payload["quarantine_batch_id"] = manifest.BatchID
	store := newFakePurgeStore(map[string]qdrant.Point{point.ID: point})
	journal := filepath.Join(t.TempDir(), "purge.json")
	if err := os.WriteFile(journal, []byte(`{"schema_version":1} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(store, "memory", nil)
	if _, err := service.Purge(context.Background(), purgeRequest(manifest, []string{point.ID}, journal)); err == nil {
		t.Fatal("accepted malformed journal")
	}
	if store.createCalls != 0 || len(store.deleted) != 0 {
		t.Fatalf("create=%d deleted=%v", store.createCalls, store.deleted)
	}
}

func TestPurgeCancellationPersistsAmbiguousJournalAndInvalidates(t *testing.T) {
	point := quarantinedPoint("42", "2026-07-01T00:00:00Z")
	finding := eligibleExpired(point.ID, qdrant.Point{ID: point.ID, Payload: underlyingPayload(point.Payload)})
	manifest := manifestFor(finding)
	point.Payload["quarantine_batch_id"] = manifest.BatchID
	store := newFakePurgeStore(map[string]qdrant.Point{point.ID: point})
	ctx, cancel := context.WithCancel(context.Background())
	store.onDelete = cancel
	store.deleteErrors[point.ID] = context.Canceled
	invalidations := 0
	service, _ := NewService(store, "memory", func() { invalidations++ })
	journal := filepath.Join(t.TempDir(), "purge.json")
	result, err := service.Purge(ctx, purgeRequest(manifest, []string{point.ID}, journal))
	if err != nil {
		t.Fatalf("cleanup journal failed: %v", err)
	}
	if result.Outcomes[0].Status != OutcomeAmbiguous || invalidations != 1 {
		t.Fatalf("result=%#v invalidations=%d", result, invalidations)
	}
	data, err := os.ReadFile(journal)
	if err != nil || !strings.Contains(string(data), `"status": "ambiguous"`) {
		t.Fatalf("journal=%s err=%v", data, err)
	}
	info, err := os.Stat(journal)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
}

func TestPurgeCheckpointsSnapshotSelectionAndDispatchBeforeDelete(t *testing.T) {
	point := quarantinedPoint("42", "2026-07-01T00:00:00Z")
	finding := eligibleExpired(point.ID, qdrant.Point{ID: point.ID, Payload: underlyingPayload(point.Payload)})
	manifest := manifestFor(finding)
	point.Payload["quarantine_batch_id"] = manifest.BatchID
	journal := filepath.Join(t.TempDir(), "purge.json")
	store := newFakePurgeStore(map[string]qdrant.Point{point.ID: point})
	store.onDelete = func() {
		data, err := os.ReadFile(journal)
		if err != nil {
			t.Errorf("read pre-delete journal: %v", err)
			return
		}
		if !strings.Contains(string(data), `"snapshot_name": "fresh.snapshot"`) || !strings.Contains(string(data), `"point_id": "42"`) || !strings.Contains(string(data), `"status": "dispatching"`) {
			t.Errorf("delete dispatched without durable binding: %s", data)
		}
	}
	service, _ := NewService(store, "memory", nil)
	result, err := service.Purge(context.Background(), purgeRequest(manifest, []string{point.ID}, journal))
	if err != nil || result.Outcomes[0].Status != OutcomeDeleted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestPurgeInitialJournalFailureDeletesNothing(t *testing.T) {
	point := quarantinedPoint("42", "2026-07-01T00:00:00Z")
	finding := eligibleExpired(point.ID, qdrant.Point{ID: point.ID, Payload: underlyingPayload(point.Payload)})
	manifest := manifestFor(finding)
	point.Payload["quarantine_batch_id"] = manifest.BatchID
	store := newFakePurgeStore(map[string]qdrant.Point{point.ID: point})
	service, _ := NewService(store, "memory", nil)
	service.writeJournal = func(context.Context, string, Result) error { return errors.New("disk unavailable") }
	journal := filepath.Join(t.TempDir(), "purge.json")
	_, err := service.Purge(context.Background(), purgeRequest(manifest, []string{point.ID}, journal))
	if err == nil || len(store.deleted) != 0 {
		t.Fatalf("err=%v deleted=%v", err, store.deleted)
	}
}

func TestPurgeFailedFinalCheckpointLeavesDispatchEvidenceForRetry(t *testing.T) {
	point := quarantinedPoint("42", "2026-07-01T00:00:00Z")
	finding := eligibleExpired(point.ID, qdrant.Point{ID: point.ID, Payload: underlyingPayload(point.Payload)})
	manifest := manifestFor(finding)
	point.Payload["quarantine_batch_id"] = manifest.BatchID
	store := newFakePurgeStore(map[string]qdrant.Point{point.ID: point})
	journal := filepath.Join(t.TempDir(), "purge.json")
	service, _ := NewService(store, "memory", nil)
	writes := 0
	service.writeJournal = func(ctx context.Context, path string, result Result) error {
		writes++
		if writes == 3 {
			return errors.New("crash before final checkpoint")
		}
		return WriteResultJournal(ctx, path, result)
	}
	_, err := service.Purge(context.Background(), purgeRequest(manifest, []string{point.ID}, journal))
	if err == nil || len(store.deleted) != 1 {
		t.Fatalf("err=%v deleted=%v writes=%d", err, store.deleted, writes)
	}
	data, readErr := os.ReadFile(journal)
	if readErr != nil || !strings.Contains(string(data), `"status": "dispatching"`) {
		t.Fatalf("journal=%s err=%v", data, readErr)
	}
	service.writeJournal = WriteResultJournal
	canceledCtx, cancel := context.WithCancel(context.Background())
	store.onList = cancel
	canceled, err := service.Purge(canceledCtx, purgeRequest(manifest, []string{point.ID}, journal))
	if err != nil || canceled.Outcomes[0].Status != OutcomeDispatching {
		t.Fatalf("canceled retry=%#v err=%v", canceled, err)
	}
	data, readErr = os.ReadFile(journal)
	if readErr != nil || !strings.Contains(string(data), `"status": "dispatching"`) {
		t.Fatalf("canceled journal=%s err=%v", data, readErr)
	}
	store.onList = nil
	retry, err := service.Purge(context.Background(), purgeRequest(manifest, []string{point.ID}, journal))
	if err != nil || retry.Outcomes[0].Status != OutcomeAlreadyApplied {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
	if store.createCalls != 1 {
		t.Fatalf("retry created post-delete snapshot: %d", store.createCalls)
	}
}

func TestPurgeRefusesAgeBatchFingerprintAndProtected(t *testing.T) {
	now := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		mutate func(*Finding, *qdrant.Point, string)
	}{
		{"too young", func(_ *Finding, p *qdrant.Point, _ string) { p.Payload["quarantined_at"] = "2026-08-13T00:00:00Z" }},
		{"wrong batch", func(_ *Finding, p *qdrant.Point, _ string) { p.Payload["quarantine_batch_id"] = "other" }},
		{"wrong reason", func(_ *Finding, p *qdrant.Point, _ string) { p.Payload["quarantine_reason"] = "superseded_retention" }},
		{"not quarantined", func(_ *Finding, p *qdrant.Point, _ string) {
			for key := range maintenancePayloadKeysForService {
				delete(p.Payload, key)
			}
			p.Payload["maintenance_status"] = "active"
		}},
		{"fingerprint drift", func(_ *Finding, p *qdrant.Point, _ string) { p.Payload["namespace"] = "changed" }},
		{"protected", func(f *Finding, _ *qdrant.Point, _ string) { f.Protected = true; f.EligibleForQuarantine = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := quarantinedPoint("42", "2026-07-01T00:00:00Z")
			f := eligibleExpired(p.ID, qdrant.Point{ID: p.ID, Payload: underlyingPayload(p.Payload)})
			m := manifestFor(f)
			p.Payload["quarantine_batch_id"] = m.BatchID
			tc.mutate(&m.Findings[0], &p, m.BatchID)
			if tc.name == "protected" {
				m.BatchID = batchID(m)
			}
			store := newFakePurgeStore(map[string]qdrant.Point{p.ID: p})
			service, _ := NewService(store, "memory", nil)
			service.now = func() time.Time { return now }
			journal := filepath.Join(t.TempDir(), "purge.json")
			result, err := service.Purge(context.Background(), purgeRequest(m, []string{p.ID}, journal))
			if err != nil {
				t.Fatal(err)
			}
			if len(store.deleted) != 0 || result.Outcomes[0].Status == OutcomeUpdated {
				t.Fatalf("deleted=%v result=%#v", store.deleted, result)
			}
		})
	}
}

func TestPurgeRequiresExplicitIDsAndPositiveBoundedAge(t *testing.T) {
	point := quarantinedPoint("42", "2026-07-01T00:00:00Z")
	finding := eligibleExpired(point.ID, qdrant.Point{ID: point.ID, Payload: underlyingPayload(point.Payload)})
	manifest := manifestFor(finding)
	point.Payload["quarantine_batch_id"] = manifest.BatchID
	service, _ := NewService(newFakePurgeStore(map[string]qdrant.Point{point.ID: point}), "memory", nil)
	for _, request := range []PurgeRequest{
		{Manifest: manifest, Selection: Selection{IncludeEligibleFindings: true}, MinimumQuarantineAge: 30 * 24 * time.Hour},
		{Manifest: manifest, Selection: Selection{PointIDs: []string{point.ID}}, MinimumQuarantineAge: 0},
		{Manifest: manifest, Selection: Selection{PointIDs: []string{point.ID}}, MinimumQuarantineAge: maxPurgeQuarantineAge + time.Hour},
		{Manifest: manifest, Selection: Selection{PointIDs: []string{point.ID}}, JournalPath: "same", SnapshotArchivePath: "same", MinimumQuarantineAge: 30 * 24 * time.Hour},
	} {
		if _, err := service.Purge(context.Background(), request); err == nil {
			t.Fatalf("accepted request=%#v", request)
		}
	}
}

func quarantinedPoint(id, at string) qdrant.Point {
	return qdrant.Point{ID: id, Payload: map[string]interface{}{"text": "fact text", "namespace": "projects", "valid_until": "2026-08-01", "maintenance_status": "quarantined", "quarantined_at": at, "quarantine_reason": "expired", "quarantine_batch_id": "placeholder"}}
}

func purgeRequest(manifest Manifest, ids []string, journal string) PurgeRequest {
	return PurgeRequest{
		Manifest:             manifest,
		Selection:            Selection{PointIDs: ids},
		JournalPath:          journal,
		SnapshotArchivePath:  journal + ".snapshot",
		MinimumQuarantineAge: 30 * 24 * time.Hour,
	}
}

func underlyingPayload(payload map[string]interface{}) map[string]interface{} {
	out := clonePayload(payload)
	for key := range maintenancePayloadKeysForService {
		delete(out, key)
	}
	return out
}

type fakePurgeStore struct {
	*fakePointStore
	created            qdrant.SnapshotIdentity
	snapshots          []qdrant.SnapshotIdentity
	createErr, listErr error
	createCalls        int
	deleteErrors       map[string]error
	deleted            []string
	listCalls          int
	archiveCalls       int
	archiveErr         error
	archiveSHA256      string
	onDelete           func()
	onList             func()
}

func newFakePurgeStore(points map[string]qdrant.Point) *fakePurgeStore {
	identity := qdrant.SnapshotIdentity{Name: "fresh.snapshot"}
	return &fakePurgeStore{fakePointStore: newFakePointStore("memory", points), created: identity, snapshots: []qdrant.SnapshotIdentity{identity}, deleteErrors: map[string]error{}, archiveSHA256: strings.Repeat("a", 64)}
}
func (f *fakePurgeStore) CreateSnapshotIdentity(context.Context) (qdrant.SnapshotIdentity, error) {
	f.createCalls++
	return f.created, f.createErr
}
func (f *fakePurgeStore) ListSnapshotIdentities(context.Context) ([]qdrant.SnapshotIdentity, error) {
	f.listCalls++
	if f.onList != nil {
		f.onList()
	}
	return f.snapshots, f.listErr
}
func (f *fakePurgeStore) EnsureSnapshotArchive(_ context.Context, _ qdrant.SnapshotIdentity, _ string, expectedSHA256 string) (string, error) {
	f.archiveCalls++
	if f.archiveErr != nil {
		return "", f.archiveErr
	}
	if expectedSHA256 != "" && expectedSHA256 != f.archiveSHA256 {
		return "", errors.New("archive checksum mismatch")
	}
	return f.archiveSHA256, nil
}
func (f *fakePurgeStore) DeleteExactStrong(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	if f.onDelete != nil {
		f.onDelete()
	}
	if err := f.deleteErrors[id]; err != nil {
		return err
	}
	delete(f.points, id)
	return nil
}
