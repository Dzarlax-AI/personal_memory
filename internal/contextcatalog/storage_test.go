package contextcatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPublishLoadConflictAndImmutableSnapshot(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "state")
	s := validSnapshot()
	hash, err := Publish(dir, s, "")
	if err != nil {
		t.Fatal(err)
	}
	loaded, got, err := LoadActive(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != hash || loaded.Version != s.Version {
		t.Fatalf("loaded hash/version mismatch: %s, %#v", got, loaded)
	}
	loaded.Entries[0].Aliases[0] = "mutated"
	reloaded, _, err := LoadActive(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Entries[0].Aliases[0] != "memory" {
		t.Fatal("LoadActive did not return an independent copy")
	}
	if _, err := Publish(dir, s, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale publication = %v, want conflict", err)
	}
	next := validSnapshot()
	next.Version = "1.0.1"
	next.ParentHash = hash
	next.CreatedAt = "2026-10-07T11:00:00Z"
	nextHash, err := Publish(dir, next, hash)
	if err != nil {
		t.Fatal(err)
	}
	if nextHash == hash {
		t.Fatal("new snapshot hash should differ")
	}
	files, err := filepath.Glob(filepath.Join(dir, "snapshots", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("immutable snapshots=%d, want 2", len(files))
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("state mode=%o", info.Mode().Perm())
	}
	info, err = os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot mode=%o", info.Mode().Perm())
	}
}

func TestPublishRequiresApprovedEntriesAndCorrectParent(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "state")
	s := validSnapshot()
	s.Entries[0].ReviewStatus = "draft"
	if _, err := Publish(dir, s, ""); err == nil {
		t.Fatal("draft entry published")
	}
	s = validSnapshot()
	s.ParentHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := Publish(dir, s, ""); err == nil {
		t.Fatal("wrong parent accepted")
	}
}

func TestLoadActiveRejectsCorruptSnapshotAndSymlinks(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "state")
	hash, err := Publish(dir, validSnapshot(), "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "snapshots", hash+".json")
	if err := os.WriteFile(path, []byte(`{"broken":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadActive(dir); err == nil {
		t.Fatal("corrupt active snapshot accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "snapshots")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(canonicalTempDir(t), "elsewhere"), filepath.Join(dir, "snapshots")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadActive(dir); err == nil {
		t.Fatal("symlink snapshots directory accepted")
	}
}

func TestLoadActiveRejectsSymlinkRootAndPointer(t *testing.T) {
	parent := canonicalTempDir(t)
	real := filepath.Join(parent, "real")
	if _, err := Publish(real, validSnapshot(), ""); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadActive(link); err == nil {
		t.Fatal("symlink root accepted")
	}
	if err := os.Remove(filepath.Join(real, activeName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(real, activeName)); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(real, validSnapshot(), ""); err == nil {
		t.Fatal("symlink active pointer overwritten")
	}
}

func TestConcurrentPublishersCompareParentUnderLock(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "state")
	first, err := Publish(dir, validSnapshot(), "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			s := validSnapshot()
			s.Version = string(rune('a' + i))
			s.ParentHash = first
			s.CreatedAt = "2026-10-07T12:00:00Z"
			_, e := Publish(dir, s, first)
			results <- e
		}(i)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("publisher error: %v", e)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRejectAncestorSymlinkAndPublicSnapshot(t *testing.T) {
	root := canonicalTempDir(t)
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(filepath.Join(link, "catalog"), validSnapshot(), ""); err == nil {
		t.Fatal("ancestor symlink publication accepted")
	}
	dir := filepath.Join(real, "catalog")
	hash, err := Publish(dir, validSnapshot(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadActive(filepath.Join(link, "catalog")); err == nil {
		t.Fatal("ancestor symlink read accepted")
	}
	if err := os.Chmod(filepath.Join(dir, "snapshots", hash+".json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadActive(dir); err == nil {
		t.Fatal("public snapshot accepted")
	}
}

func TestPublishWritesReconciliableOperationJournal(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "journal")
	hash, err := Publish(dir, validSnapshot(), "")
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, journalDirName, "*.receipt.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("receipts=%d err=%v", len(files), err)
	}
	var rec OperationRecord
	if err := readJSON(files[0], &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Status != "applied" || rec.ResultHash != hash || rec.Metadata.Kind != "manual" {
		t.Fatalf("unexpected receipt: %#v", rec)
	}
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileOperations(dir)
	if err != nil || result.Reconciled != 1 {
		t.Fatalf("reconcile=%#v err=%v", result, err)
	}
	if _, err := LoadOperation(dir, rec.OperationID); err != nil {
		t.Fatal(err)
	}
}

func TestOperationReceiptCorruptionBlocksVersionRead(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "journal")
	if _, err := Publish(dir, validSnapshot(), ""); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, journalDirName, "*.receipt.json"))
	if len(files) != 1 {
		t.Fatalf("receipts=%d", len(files))
	}
	if err := os.WriteFile(files[0], []byte(`{"broken":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileOperations(dir); err == nil {
		t.Fatal("corrupt receipt accepted")
	}
}

func TestRestoreDescriptionPreservesIdentityAndPersistsHold(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "restore")
	in := RegistrationInput{ProjectKey: "project-key-123", Namespace: "projects", Name: "Example project", Tag: "example-project", Summary: "Original client declaration", Evidence: []Evidence{{Kind: "user_declared", Text: "project source"}}}
	registered, err := EnsureProject(dir, "test-owner", in)
	if err != nil {
		t.Fatal(err)
	}
	s, active, err := LoadActive(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := s.Entries[0]
	e.DescriptionSource = "manual"
	e.Summary = "Older description"
	s.Entries[0] = e
	s.ParentHash = active
	s.Version = "manual-1"
	s.CreatedAt = "2026-10-14T10:00:00Z"
	historical, err := Publish(dir, s, active)
	if err != nil {
		t.Fatal(err)
	}
	s, active, err = LoadActive(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Entries[0].Summary = "Current description"
	s.ParentHash = active
	s.Version = "manual-2"
	s.CreatedAt = "2026-10-14T11:00:00Z"
	current, err := Publish(dir, s, active)
	if err != nil {
		t.Fatal(err)
	}
	cur, _, _ := LoadActive(dir)
	hist, _ := LoadSnapshot(dir, historical)
	id := cur.Entries[0].ProjectID
	req := RestoreRequest{OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ProjectID: id, HistoricalHash: historical, HistoricalEntryHash: entryHash(hist.Entries[0]), ExpectedCurrentHash: current, ExpectedCurrentEntryHash: entryHash(cur.Entries[0]), Reason: "Restore reviewed description", ExpectedPolicyRevision: 0}
	result, err := Restore(dir, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "restored" {
		t.Fatalf("restore status=%s", result.Status)
	}
	got, _, err := LoadActive(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entries[0].Summary != "Older description" || got.Entries[0].ProjectID != id || got.Entries[0].ClientEvidence[0].Text != "project source" {
		t.Fatalf("restore lost identity or wrong description: %#v", got.Entries[0])
	}
	policy, err := LoadProjectPolicy(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Hold || policy.Revision != 1 || policy.Reason != "Restore reviewed description" {
		t.Fatalf("hold policy not persisted: %#v", policy)
	}
	loaded, err := LoadOperation(dir, req.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RequestHash != restoreRequestHash(req) || loaded.Reason != req.Reason {
		t.Fatalf("restore operation provenance missing: %#v", loaded)
	}
	versions, err := ListVersions(dir, id, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions.Versions) == 0 || versions.Versions[0].Kind != "restore" || versions.Versions[0].OperationID != req.OperationID {
		t.Fatalf("restore generation provenance=%#v", versions.Versions)
	}
	_ = registered
}

func TestLoadOperationDoesNotFallbackFromCorruptReceipt(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "operation")
	if _, err := Publish(dir, validSnapshot(), ""); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, journalDirName, "*.receipt.json"))
	var record OperationRecord
	if err := readJSON(files[0], &record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files[0], []byte(`{"broken":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOperation(dir, record.OperationID); err == nil {
		t.Fatal("LoadOperation fell back to intent after corrupt receipt")
	}
}

func TestPublicationHoldPreservesHistoricalOnlyAndReconcilesPriorIntent(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "policy-hold")
	if _, err := EnsureProject(dir, "owner", RegistrationInput{ProjectKey: "project-key-456", Namespace: "projects", Name: "Policy project", Tag: "policy-project", Summary: "Initial source", Evidence: []Evidence{{Kind: "user_declared", Text: "evidence"}}}); err != nil {
		t.Fatal(err)
	}
	s, active, err := LoadActive(dir)
	if err != nil {
		t.Fatal(err)
	}
	pid := s.Entries[0].ProjectID
	eh := entryHash(s.Entries[0])
	desired := ProjectPolicy{ProjectID: pid, Revision: 1, Hold: true, HistoricalOnly: false, Reason: "interrupted hold"}
	sum := sha256.Sum256([]byte(pid + "1" + desired.Reason))
	meta := PublishMetadata{Kind: "policy", ProposalChecksum: hex.EncodeToString(sum[:])}
	intent := OperationRecord{OperationID: operationID("policy", active, eh, meta), Kind: "policy", Status: "intent", SourceHash: active, TargetHash: active, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: meta, PolicyRevision: 1, Reason: desired.Reason, Changes: []CardChange{{ProjectID: pid}}}
	if err := writeOperationIntent(dir, intent); err != nil {
		t.Fatal(err)
	}
	policy, err := SetPublicationHold(dir, pid, true, "hold card", eh, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Hold || policy.Revision != 1 {
		t.Fatalf("hold not applied: %#v", policy)
	}
	policy.HistoricalOnly = true
	policy.Reason = "historical restriction"
	policy, err = SetProjectPolicy(dir, policy, eh, policy.Revision)
	if err != nil {
		t.Fatal(err)
	}
	policy, err = SetPublicationHold(dir, pid, false, "resume", eh, policy.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Hold || !policy.HistoricalOnly || policy.Reason != "resume" {
		t.Fatalf("resume lost policy fields: %#v", policy)
	}
	var receipt OperationRecord
	_, receiptPath := opPaths(dir, intent.OperationID)
	if err := readJSON(receiptPath, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "not_applied" {
		t.Fatalf("stale intent reconciliation=%s", receipt.Status)
	}
}

func TestReconcileOperationsAllowsFreshMissingCatalogSafely(t *testing.T) {
	root := canonicalTempDir(t)
	fresh := filepath.Join(root, "fresh", "catalog")
	got, err := ReconcileOperations(fresh)
	if err != nil || got.Reconciled != 0 || len(got.Ambiguous) != 0 {
		t.Fatalf("fresh reconcile=%#v err=%v", got, err)
	}
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileOperations(filepath.Join(link, "catalog")); err == nil {
		t.Fatal("symlink ancestor accepted for missing catalog")
	}
}

func TestPolicyGuardAllowsSequentialDescriptionPublicationsAndRejectsStaleState(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "guard")
	if _, err := EnsureProject(dir, "owner", RegistrationInput{ProjectKey: "project-key-789", Namespace: "projects", Name: "Guarded project", Tag: "guarded-project", Summary: "Caller source", Evidence: []Evidence{{Kind: "user_declared", Text: "source excerpt"}}}); err != nil {
		t.Fatal(err)
	}
	s, active, err := LoadActive(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Entries[0].DescriptionSource = "manual"
	s.ParentHash = active
	s.Version = "manualized"
	s.CreatedAt = "2026-10-14T12:00:00Z"
	active, err = Publish(dir, s, active)
	if err != nil {
		t.Fatal(err)
	}
	s, active, err = LoadActive(dir)
	if err != nil {
		t.Fatal(err)
	}
	pid := s.Entries[0].ProjectID
	entry := entryHash(s.Entries[0])
	policy, err := SetPublicationHold(dir, pid, true, "hold", entry, 0)
	if err != nil {
		t.Fatal(err)
	}
	policy, err = SetPublicationHold(dir, pid, false, "resume", entry, policy.Revision)
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range []string{"first generated description", "second generated description"} {
		current, hash, err := LoadActive(dir)
		if err != nil {
			t.Fatal(err)
		}
		current.Entries[0].Summary = summary
		current.ParentHash = hash
		current.Version = summary
		current.CreatedAt = "2026-10-14T13:00:00Z"
		guard := ProjectPolicyGuard{ProjectID: pid, ExpectedRevision: policy.Revision, ExpectedCurrentEntryHash: entryHash((mustCurrent(t, dir)).Entries[0])}
		newHash, err := PublishWithPolicyGuard(dir, current, hash, PublishMetadata{Kind: "maintenance", RunID: "run", ProposalChecksum: strings.Repeat("a", 64)}, []ProjectPolicyGuard{guard})
		if err != nil {
			t.Fatal(err)
		}
		active = newHash
		_, _, _ = LoadActive(dir)
	}
	cur, hash, err := LoadActive(dir)
	if err != nil {
		t.Fatal(err)
	}
	changed := cloneSnapshot(cur)
	changed.Entries[0].Summary = "stale attempt"
	changed.ParentHash = hash
	changed.CreatedAt = "2026-10-14T14:00:00Z"
	if _, err := PublishWithPolicyGuard(dir, changed, hash, PublishMetadata{RunID: "stale", ProposalChecksum: strings.Repeat("b", 64)}, []ProjectPolicyGuard{{ProjectID: pid, ExpectedRevision: policy.Revision, ExpectedCurrentEntryHash: strings.Repeat("c", 64)}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale active entry accepted: %v", err)
	}
	if _, err := PublishWithPolicyGuard(dir, changed, hash, PublishMetadata{RunID: "stale", ProposalChecksum: strings.Repeat("b", 64)}, []ProjectPolicyGuard{{ProjectID: pid, ExpectedRevision: policy.Revision + 1, ExpectedCurrentEntryHash: entryHash(cur.Entries[0])}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale policy revision accepted: %v", err)
	}
	_ = active
}
func mustCurrent(t *testing.T, dir string) Snapshot {
	t.Helper()
	s, _, err := LoadActive(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRestoreRejectsUnpublishedOrphanSnapshot(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "orphans")
	result, err := EnsureProject(dir, "owner", RegistrationInput{ProjectKey: "project-key-orphan", Namespace: "projects", Name: "Example", Tag: "example", Summary: "Declared source"})
	if err != nil {
		t.Fatal(err)
	}
	current, active, err := LoadActive(dir)
	if err != nil {
		t.Fatal(err)
	}
	orphan := cloneSnapshot(current)
	orphan.ParentHash = active
	orphan.Version = "abandoned"
	orphan.Entries[0].Summary = "Unpublished description"
	orphan.Entries[0].DescriptionSource = "model_derived"
	orphan.Entries[0].DeclaredSummary = current.Entries[0].Summary
	hash, err := Hash(orphan)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := canonicalJSON(orphan)
	if err := writeExclusive(filepath.Join(dir, snapshotDirName, hash+".json"), raw); err != nil {
		t.Fatal(err)
	}
	req := RestoreRequest{Reason: "test", OperationID: strings.Repeat("a", 64), ProjectID: result.ProjectID, HistoricalHash: hash, HistoricalEntryHash: entryHash(orphan.Entries[0]), ExpectedCurrentHash: active, ExpectedCurrentEntryHash: entryHash(current.Entries[0])}
	preview, err := PreviewRestore(dir, req)
	if err != nil || preview.Status != "conflict" {
		t.Fatalf("orphan preview: %#v %v", preview, err)
	}
	restored, err := Restore(dir, req)
	if err != nil || restored.Status != "conflict" {
		t.Fatalf("orphan restore: %#v %v", restored, err)
	}
	_, after, err := LoadActive(dir)
	if err != nil || after != active {
		t.Fatal("orphan changed active catalog")
	}
	policy, err := LoadProjectPolicy(dir, result.ProjectID)
	if err != nil || policy.Hold {
		t.Fatal("rejected orphan created hold")
	}
}
