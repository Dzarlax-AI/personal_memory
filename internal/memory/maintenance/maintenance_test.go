package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func TestWriteManifestIsPrivateAndExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "analysis.json")
	manifest := Manifest{SchemaVersion: ManifestSchemaVersion, PolicyVersion: PolicyVersion, BatchID: "batch", Collection: "memory", ReferenceTime: "2026-08-14T00:00:00Z", Complete: true, Findings: []Finding{}}
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if err := WriteManifest(path, manifest); err == nil {
		t.Fatal("expected exclusive-create refusal")
	}
}

func TestParseStatus(t *testing.T) {
	now := "2026-08-14T10:00:00Z"
	tests := []struct {
		name    string
		payload map[string]interface{}
		status  Status
		legacy  bool
		valid   bool
	}{
		{name: "legacy active", payload: map[string]interface{}{}, status: Active, legacy: true, valid: true},
		{name: "explicit active", payload: map[string]interface{}{"maintenance_status": "active"}, status: Active, valid: true},
		{name: "quarantined", payload: map[string]interface{}{"maintenance_status": "quarantined", "quarantined_at": now, "quarantine_reason": "expired", "quarantine_batch_id": "batch-1"}, status: Quarantined, valid: true},
		{name: "unknown quarantine reason", payload: map[string]interface{}{"maintenance_status": "quarantined", "quarantined_at": now, "quarantine_reason": "old", "quarantine_batch_id": "batch-1"}},
		{name: "unknown", payload: map[string]interface{}{"maintenance_status": "deleted"}},
		{name: "incomplete quarantine", payload: map[string]interface{}{"maintenance_status": "quarantined", "quarantined_at": now}},
		{name: "active with quarantine fields", payload: map[string]interface{}{"maintenance_status": "active", "quarantined_at": now}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.payload)
			if got.Status != tt.status || got.Legacy != tt.legacy || got.Valid != tt.valid {
				t.Fatalf("Parse() = %#v", got)
			}
		})
	}
}

func TestNamespaceScopedAnalyzeDoesNotInventCrossNamespaceOrphans(t *testing.T) {
	points := []qdrant.ScrollPoint{{ID: "1", Payload: map[string]interface{}{
		"namespace": "projects", "lifecycle_state": "superseded", "canonical": false,
		"supersedes": []interface{}{}, "superseded_by": []interface{}{"2"}, "updated_at": "2026-08-13T00:00:00Z", "lifecycle_transitioned_at": "2026-08-13T00:00:00Z",
	}}}
	manifest, err := Analyze(context.Background(), fakeScanner{points: points}, Options{
		Collection: "memory", Namespace: "projects", ReferenceTime: time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
		SupersededRetention: 30 * 24 * time.Hour, StaleAfter: 90 * 24 * time.Hour, LowRecallThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Findings) != 0 {
		t.Fatalf("findings = %#v", manifest.Findings)
	}
}

func TestActiveFilterComposesWithoutMutatingBase(t *testing.T) {
	base := map[string]interface{}{"should": []map[string]interface{}{{"key": "lifecycle_state"}}}
	got := ActiveFilter(base)
	encoded, _ := json.Marshal(got)
	for _, want := range []string{"maintenance_status", "active", "is_empty", "lifecycle_state"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("filter %s missing %q", encoded, want)
		}
	}
	if len(base) != 1 {
		t.Fatalf("base mutated: %#v", base)
	}
}

func TestAnalyzeClassifiesConservativelyAndNeverEmitsText(t *testing.T) {
	reference := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	points := []qdrant.ScrollPoint{
		{ID: "1", Payload: map[string]interface{}{"text": "private expired", "namespace": "projects", "created_at": "2026-01-01T00:00:00Z", "valid_until": "2026-08-01", "recall_count": float64(0)}},
		{ID: "2", Payload: map[string]interface{}{"text": "protected fact body", "created_at": "2025-01-01T00:00:00Z", "lifecycle_state": "current", "canonical": true, "supersedes": []interface{}{}, "superseded_by": []interface{}{}, "recall_count": float64(0)}},
		{ID: "3", Payload: map[string]interface{}{"text": "superseded fact body", "created_at": "2025-01-01T00:00:00Z", "updated_at": "2026-08-13T00:00:00Z", "lifecycle_transitioned_at": "2026-06-01T00:00:00Z", "lifecycle_state": "superseded", "canonical": false, "supersedes": []interface{}{}, "superseded_by": []interface{}{"4"}, "recall_count": float64(0)}},
		{ID: "4", Payload: map[string]interface{}{"text": "current", "created_at": "2026-06-01T00:00:00Z", "recall_count": float64(4)}},
		{ID: "5", Payload: map[string]interface{}{"text": "bad metadata", "valid_until": "tomorrow"}},
	}
	manifest, err := Analyze(context.Background(), fakeScanner{points: points}, Options{
		Collection:          "memory",
		ReferenceTime:       reference,
		SupersededRetention: 30 * 24 * time.Hour,
		StaleAfter:          90 * 24 * time.Hour,
		LowRecallThreshold:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Complete || manifest.Scanned != len(points) || manifest.BatchID == "" {
		t.Fatalf("manifest = %#v", manifest)
	}
	encoded, _ := json.Marshal(manifest)
	for _, secret := range []string{"private expired", "protected fact body", "superseded fact body", "bad metadata", `"text"`} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("manifest leaked %q: %s", secret, encoded)
		}
	}
	byID := map[string]Finding{}
	for _, finding := range manifest.Findings {
		byID[finding.PointID] = finding
	}
	if !byID["1"].EligibleForQuarantine || !containsClass(byID["1"].Classes, ClassExpired) {
		t.Fatalf("expired finding = %#v", byID["1"])
	}
	if !byID["2"].Protected || byID["2"].EligibleForQuarantine {
		t.Fatalf("protected finding = %#v", byID["2"])
	}
	if !byID["3"].EligibleForQuarantine || !containsClass(byID["3"].Classes, ClassSupersededRetention) {
		t.Fatalf("superseded finding = %#v", byID["3"])
	}
	if !containsClass(byID["5"].Classes, ClassMalformedMetadata) {
		t.Fatalf("malformed finding = %#v", byID["5"])
	}
}

func TestAnalyzeOmitsAllMetadataWhenAnyManifestFieldIsMalformed(t *testing.T) {
	const secret = "PRIVATE_METADATA_MARKER"
	manifest, err := Analyze(context.Background(), fakeScanner{points: []qdrant.ScrollPoint{{
		ID: "1", Payload: map[string]interface{}{
			"text":               secret,
			"lifecycle_state":    secret,
			"created_at":         secret,
			"updated_at":         secret,
			"valid_until":        secret,
			"maintenance_status": secret,
		},
	}}}, Options{
		Collection: "memory", ReferenceTime: time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
		SupersededRetention: 30 * 24 * time.Hour, StaleAfter: 90 * 24 * time.Hour, LowRecallThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("manifest leaked malformed metadata: %s", encoded)
	}
	if len(manifest.Findings) != 1 || !containsClass(manifest.Findings[0].Classes, ClassMalformedMetadata) {
		t.Fatalf("findings = %#v", manifest.Findings)
	}
}

func TestSupersededRetentionUsesLifecycleTransitionTime(t *testing.T) {
	reference := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	points := []qdrant.ScrollPoint{
		{ID: "old-transition", Payload: map[string]interface{}{"updated_at": "2026-08-13T00:00:00Z", "lifecycle_transitioned_at": "2026-06-01T00:00:00Z", "lifecycle_state": "superseded", "superseded_by": []interface{}{"current"}}},
		{ID: "new-transition", Payload: map[string]interface{}{"updated_at": "2020-01-01T00:00:00Z", "lifecycle_transitioned_at": "2026-08-13T00:00:00Z", "lifecycle_state": "superseded", "superseded_by": []interface{}{"current"}}},
		{ID: "current", Payload: map[string]interface{}{}},
	}
	manifest, err := Analyze(context.Background(), fakeScanner{points: points}, Options{
		Collection: "memory", ReferenceTime: reference, SupersededRetention: 30 * 24 * time.Hour,
		StaleAfter: 90 * 24 * time.Hour, LowRecallThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Finding{}
	for _, finding := range manifest.Findings {
		byID[finding.PointID] = finding
	}
	if !byID["old-transition"].EligibleForQuarantine || !containsClass(byID["old-transition"].Classes, ClassSupersededRetention) {
		t.Fatalf("old transition = %#v", byID["old-transition"])
	}
	if containsClass(byID["new-transition"].Classes, ClassSupersededRetention) || byID["new-transition"].EligibleForQuarantine {
		t.Fatalf("new transition = %#v", byID["new-transition"])
	}
}

func TestAgeAndLowRecallAloneAreReviewOnly(t *testing.T) {
	manifest, err := Analyze(context.Background(), fakeScanner{points: []qdrant.ScrollPoint{{
		ID: "1", Payload: map[string]interface{}{"text": "old", "created_at": "2020-01-01T00:00:00Z", "recall_count": float64(0)},
	}}}, Options{Collection: "memory", ReferenceTime: time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC), StaleAfter: 90 * 24 * time.Hour, LowRecallThreshold: 1, SupersededRetention: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Findings) != 1 || manifest.Findings[0].EligibleForQuarantine || !containsClass(manifest.Findings[0].Classes, ClassStaleUnused) {
		t.Fatalf("findings = %#v", manifest.Findings)
	}
}

func TestQuarantineRestoreServiceIsManifestBoundIdempotentAndContentFree(t *testing.T) {
	const private = "PRIVATE_FACT_TEXT_MUST_NOT_LEAK"
	store := newFakePointStore("memory", map[string]qdrant.Point{
		"42": {ID: "42", Payload: map[string]interface{}{"text": private, "valid_until": "2026-08-01", "created_at": "2026-01-01T00:00:00Z"}},
	})
	manifest := manifestFor(eligibleExpired("42", store.points["42"]))
	invalidations := 0
	service, err := NewService(store, "memory", func() { invalidations++ })
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) }
	journal := filepath.Join(t.TempDir(), "result.json")
	request := Request{Manifest: manifest, Selection: Selection{PointIDs: []string{"42"}}, JournalPath: journal}

	result, err := service.Quarantine(context.Background(), request)
	if err != nil || result.Outcomes[0].Status != OutcomeUpdated {
		t.Fatalf("quarantine result=%#v err=%v", result, err)
	}
	if invalidations != 1 || len(store.writes) != 1 {
		t.Fatalf("invalidations=%d writes=%#v", invalidations, store.writes)
	}
	write := store.writes[0]
	if write.id != "42" || len(write.set) != 4 || len(write.deleteKeys) != 0 || write.set["quarantine_reason"] != "expired" {
		t.Fatalf("write = %#v", write)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), private) {
		t.Fatalf("result leaked private content: %s", encoded)
	}
	journalContents, err := os.ReadFile(journal)
	if err != nil || strings.Contains(string(journalContents), private) || strings.Contains(string(journalContents), "text") {
		t.Fatalf("journal leaked private content: %s (%v)", journalContents, err)
	}
	info, err := os.Stat(journal)
	if err != nil {
		t.Fatalf("stat journal: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode=%v", info.Mode())
	}

	result, err = service.Quarantine(context.Background(), request)
	if err != nil || result.Outcomes[0].Status != OutcomeAlreadyApplied || len(store.writes) != 1 || invalidations != 1 {
		t.Fatalf("idempotent quarantine=%#v err=%v writes=%d invalidations=%d", result, err, len(store.writes), invalidations)
	}
	result, err = service.Restore(context.Background(), request)
	if err != nil || result.Outcomes[0].Status != OutcomeUpdated || len(store.writes) != 2 || invalidations != 2 {
		t.Fatalf("restore=%#v err=%v writes=%d invalidations=%d", result, err, len(store.writes), invalidations)
	}
	if got := store.writes[1]; got.set["maintenance_status"] != "active" || !sameStrings(got.deleteKeys, []string{"quarantined_at", "quarantine_reason", "quarantine_batch_id"}) {
		t.Fatalf("restore write=%#v", got)
	}
	result, err = service.Restore(context.Background(), request)
	if err != nil || result.Outcomes[0].Status != OutcomeAlreadyApplied || len(store.writes) != 2 || invalidations != 2 {
		t.Fatalf("idempotent restore=%#v err=%v writes=%d invalidations=%d", result, err, len(store.writes), invalidations)
	}
}

func TestServiceRejectsManifestAndSelectionBoundaries(t *testing.T) {
	point := qdrant.Point{ID: "1", Payload: map[string]interface{}{"valid_until": "2026-08-01"}}
	store := newFakePointStore("memory", map[string]qdrant.Point{"1": point})
	service, err := NewService(store, "memory", nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest := manifestFor(eligibleExpired("1", point))
	for name, request := range map[string]Request{
		"incomplete":          {Manifest: func() Manifest { m := manifest; m.Complete = false; return m }(), Selection: Selection{PointIDs: []string{"1"}}},
		"wrong collection":    {Manifest: func() Manifest { m := manifest; m.Collection = "other"; return m }(), Selection: Selection{PointIDs: []string{"1"}}},
		"wrong policy":        {Manifest: func() Manifest { m := manifest; m.PolicyVersion = "0"; return m }(), Selection: Selection{PointIDs: []string{"1"}}},
		"tampered batch":      {Manifest: func() Manifest { m := manifest; m.BatchID = "maintenance-tampered"; return m }(), Selection: Selection{PointIDs: []string{"1"}}},
		"unknown selection":   {Manifest: manifest, Selection: Selection{PointIDs: []string{"not-in-manifest"}}},
		"duplicate selection": {Manifest: manifest, Selection: Selection{PointIDs: []string{"1", "1"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Quarantine(context.Background(), request); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestServiceDistinguishesNotFoundIneligibleConflictAndAmbiguous(t *testing.T) {
	base := qdrant.Point{ID: "missing", Payload: map[string]interface{}{"valid_until": "2026-08-01"}}
	protected := qdrant.Point{ID: "protected", Payload: map[string]interface{}{"valid_until": "2026-08-01", "permanent": true}}
	drift := qdrant.Point{ID: "drift", Payload: map[string]interface{}{"valid_until": "2026-08-01", "other": "current"}}
	conflict := qdrant.Point{ID: "conflict", Payload: map[string]interface{}{"valid_until": "2026-08-01", "maintenance_status": "quarantined", "quarantined_at": "2026-08-14T00:00:00Z", "quarantine_reason": "expired", "quarantine_batch_id": "other-batch"}}
	ambiguous := qdrant.Point{ID: "ambiguous", Payload: map[string]interface{}{"valid_until": "2026-08-01"}}
	store := newFakePointStore("memory", map[string]qdrant.Point{"protected": protected, "drift": drift, "conflict": conflict, "ambiguous": ambiguous})
	findings := []Finding{
		eligibleExpired("missing", base),
		{PointID: "protected", Classes: []CandidateClass{ClassExpired, ClassProtected}, Protected: true, Fingerprint: fp(protected)},
		func() Finding {
			before := drift
			before.Payload = map[string]interface{}{"valid_until": "2026-08-01", "other": "before"}
			return eligibleExpired("drift", before)
		}(),
		func() Finding {
			before := conflict
			before.Payload = map[string]interface{}{"valid_until": "2026-08-01"}
			return eligibleExpired("conflict", before)
		}(),
		eligibleExpired("ambiguous", ambiguous),
	}
	manifest := manifestWithFindings(findings)
	store.mutationErrors["ambiguous"] = fmt.Errorf("connection closed after dispatch")
	invalidations := 0
	service, err := NewService(store, "memory", func() { invalidations++ })
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Quarantine(context.Background(), Request{Manifest: manifest, Selection: Selection{IncludeEligibleFindings: true}})
	if err != nil {
		t.Fatal(err)
	}
	got := outcomesByID(result)
	for id, want := range map[string]OutcomeStatus{"missing": OutcomeNotFound, "drift": OutcomeConflict, "conflict": OutcomeConflict, "ambiguous": OutcomeAmbiguous} {
		if got[id] != want {
			t.Errorf("%s=%q want %q", id, got[id], want)
		}
	}
	if invalidations != 1 {
		t.Fatalf("invalidations=%d want 1 after ambiguous dispatch", invalidations)
	}
	result, err = service.Quarantine(context.Background(), Request{Manifest: manifest, Selection: Selection{PointIDs: []string{"protected"}}})
	if err != nil || result.Outcomes[0].Status != OutcomeProtectedOrIneligible {
		t.Fatalf("protected result=%#v err=%v", result, err)
	}
}

func TestServicePreservesNumericAndStringIDsAndRequiresCompatibleBatchForRestore(t *testing.T) {
	numeric := qdrant.Point{ID: "42", Payload: map[string]interface{}{"valid_until": "2026-08-01"}}
	uuid := qdrant.Point{ID: "4f08ef2a-42c0-45df-a6c3-5ca86db4ddf8", Payload: map[string]interface{}{"valid_until": "2026-08-01"}}
	store := newFakePointStore("memory", map[string]qdrant.Point{"42": numeric, uuid.ID: uuid})
	manifest := manifestWithFindings([]Finding{eligibleExpired(numeric.ID, numeric), eligibleExpired(uuid.ID, uuid)})
	service, err := NewService(store, "memory", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Quarantine(context.Background(), Request{Manifest: manifest, Selection: Selection{PointIDs: []string{uuid.ID, numeric.ID}}})
	if err != nil || len(result.Outcomes) != 2 || result.Outcomes[0].PointID != numeric.ID || result.Outcomes[1].PointID != uuid.ID {
		t.Fatalf("mixed IDs result=%#v err=%v", result, err)
	}
	store.points[numeric.ID].Payload["quarantine_batch_id"] = "wrong"
	result, err = service.Restore(context.Background(), Request{Manifest: manifest, Selection: Selection{PointIDs: []string{numeric.ID}}})
	if err != nil || result.Outcomes[0].Status != OutcomeConflict {
		t.Fatalf("conflicting batch restore=%#v err=%v", result, err)
	}
}

func TestServiceReportsAmbiguousWhenPostWriteVerificationSeesConcurrentDrift(t *testing.T) {
	point := qdrant.Point{ID: "quarantine", Payload: map[string]interface{}{"valid_until": "2026-08-01"}}
	store := newFakePointStore("memory", map[string]qdrant.Point{point.ID: point})
	manifest := manifestFor(eligibleExpired(point.ID, point))
	invalidations := 0
	service, err := NewService(store, "memory", func() { invalidations++ })
	if err != nil {
		t.Fatal(err)
	}
	store.afterWrite = func(store *fakePointStore, id string) { store.points[id].Payload["concurrent"] = "changed" }
	result, err := service.Quarantine(context.Background(), Request{Manifest: manifest, Selection: Selection{PointIDs: []string{point.ID}}})
	if err != nil || result.Outcomes[0].Status != OutcomeAmbiguous || invalidations != 1 {
		t.Fatalf("quarantine post-write drift result=%#v err=%v invalidations=%d", result, err, invalidations)
	}

	restorePoint := qdrant.Point{ID: "restore", Payload: map[string]interface{}{"valid_until": "2026-08-01"}}
	restoreManifest := manifestFor(eligibleExpired(restorePoint.ID, restorePoint))
	restorePoint.Payload = map[string]interface{}{
		"valid_until": "2026-08-01", "maintenance_status": "quarantined", "quarantined_at": "2026-08-14T00:00:00Z",
		"quarantine_reason": "expired", "quarantine_batch_id": restoreManifest.BatchID,
	}
	store = newFakePointStore("memory", map[string]qdrant.Point{restorePoint.ID: restorePoint})
	service, err = NewService(store, "memory", func() { invalidations++ })
	if err != nil {
		t.Fatal(err)
	}
	store.afterWrite = func(store *fakePointStore, id string) { store.points[id].Payload["concurrent"] = "changed" }
	result, err = service.Restore(context.Background(), Request{Manifest: restoreManifest, Selection: Selection{PointIDs: []string{restorePoint.ID}}})
	if err != nil || result.Outcomes[0].Status != OutcomeAmbiguous || invalidations != 2 {
		t.Fatalf("restore post-write drift result=%#v err=%v invalidations=%d", result, err, invalidations)
	}
}

func TestWriteResultJournalIsAtomicPrivateAndCancellationSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	first := Result{SchemaVersion: 1, PolicyVersion: PolicyVersion, BatchID: "batch", Operation: OperationQuarantine, Outcomes: []PointOutcome{{PointID: "1", Status: OutcomeUpdated}}, Timestamp: "2026-08-14T00:00:00Z"}
	if err := WriteResultJournal(context.Background(), path, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Operation = OperationRestore
	if err := WriteResultJournal(context.Background(), path, second); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"operation": "restore"`) {
		t.Fatalf("atomic replacement data=%s err=%v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat journal: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", info.Mode())
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WriteResultJournal(canceled, path, first); err == nil {
		t.Fatal("expected canceled journal write")
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), `"operation": "restore"`) {
		t.Fatalf("cancellation changed journal: %s", data)
	}
}

func TestServicePersistsAmbiguousJournalAfterMutationCancelsRequest(t *testing.T) {
	point := qdrant.Point{ID: "cancelled", Payload: map[string]interface{}{"valid_until": "2026-08-01"}}
	store := newFakePointStore("memory", map[string]qdrant.Point{point.ID: point})
	manifest := manifestFor(eligibleExpired(point.ID, point))
	ctx, cancel := context.WithCancel(context.Background())
	store.afterWrite = func(_ *fakePointStore, _ string) { cancel() }
	store.afterWriteError = context.Canceled
	journal := filepath.Join(t.TempDir(), "result.json")
	service, err := NewService(store, "memory", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Quarantine(ctx, Request{Manifest: manifest, JournalPath: journal, Selection: Selection{PointIDs: []string{point.ID}}})
	if err != nil {
		t.Fatalf("journal was not persisted after dispatch cancellation: %v", err)
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Status != OutcomeAmbiguous {
		t.Fatalf("result=%#v", result)
	}
	data, err := os.ReadFile(journal)
	if err != nil || !strings.Contains(string(data), `"status": "ambiguous"`) {
		t.Fatalf("journal=%s err=%v", data, err)
	}
}

type maintenanceWrite struct {
	id         string
	set        map[string]interface{}
	deleteKeys []string
}

type fakePointStore struct {
	collection      string
	points          map[string]qdrant.Point
	mutationErrors  map[string]error
	writes          []maintenanceWrite
	afterWrite      func(*fakePointStore, string)
	afterWriteError error
}

func newFakePointStore(collection string, points map[string]qdrant.Point) *fakePointStore {
	return &fakePointStore{collection: collection, points: points, mutationErrors: map[string]error{}}
}

func (f *fakePointStore) CollectionName() string { return f.collection }
func (f *fakePointStore) Get(_ context.Context, id string) (qdrant.Point, bool, error) {
	point, ok := f.points[id]
	return point, ok, nil
}
func (f *fakePointStore) QuarantineMaintenance(_ context.Context, id string, at time.Time, reason qdrant.MaintenanceReason, batchID string) error {
	return f.applyMaintenance(id, map[string]interface{}{
		"maintenance_status":  "quarantined",
		"quarantined_at":      at.UTC().Format(time.RFC3339),
		"quarantine_reason":   string(reason),
		"quarantine_batch_id": batchID,
	}, nil)
}

func (f *fakePointStore) RestoreMaintenance(_ context.Context, id string) error {
	return f.applyMaintenance(id, map[string]interface{}{"maintenance_status": "active"}, []string{"quarantined_at", "quarantine_reason", "quarantine_batch_id"})
}

func (f *fakePointStore) applyMaintenance(id string, set map[string]interface{}, deleteKeys []string) error {
	f.writes = append(f.writes, maintenanceWrite{id: id, set: clonePayload(set), deleteKeys: append([]string(nil), deleteKeys...)})
	if err := f.mutationErrors[id]; err != nil {
		return err
	}
	point, ok := f.points[id]
	if !ok {
		return fmt.Errorf("missing point")
	}
	for key, value := range set {
		point.Payload[key] = value
	}
	for _, key := range deleteKeys {
		delete(point.Payload, key)
	}
	f.points[id] = point
	if f.afterWrite != nil {
		f.afterWrite(f, id)
	}
	return f.afterWriteError
}

func eligibleExpired(id string, point qdrant.Point) Finding {
	return Finding{PointID: id, Classes: []CandidateClass{ClassExpired}, MaintenanceStatus: Active, EligibleForQuarantine: true, Fingerprint: fp(point)}
}

func fp(point qdrant.Point) string {
	return fingerprint(qdrant.ScrollPoint{ID: point.ID, Payload: point.Payload})
}

func manifestFor(finding Finding) Manifest {
	return manifestWithFindings([]Finding{finding})
}

func manifestWithFindings(findings []Finding) Manifest {
	manifest := Manifest{SchemaVersion: ManifestSchemaVersion, PolicyVersion: PolicyVersion, Collection: "memory", ReferenceTime: "2026-08-14T00:00:00Z", Complete: true, Findings: findings}
	manifest.BatchID = batchID(manifest)
	return manifest
}

func outcomesByID(result Result) map[string]OutcomeStatus {
	out := make(map[string]OutcomeStatus, len(result.Outcomes))
	for _, finding := range result.Outcomes {
		out[finding.PointID] = finding.Status
	}
	return out
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

type fakeScanner struct {
	points []qdrant.ScrollPoint
	err    error
}

func (f fakeScanner) ScrollAll(context.Context, map[string]interface{}, bool) ([]qdrant.ScrollPoint, error) {
	return f.points, f.err
}

func containsClass(classes []CandidateClass, want CandidateClass) bool {
	for _, class := range classes {
		if class == want {
			return true
		}
	}
	return false
}
