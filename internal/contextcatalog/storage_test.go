package contextcatalog

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
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
