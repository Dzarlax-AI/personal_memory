package contextcatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSameReviewSourceIdentityBindsDeclaredEvidence(t *testing.T) {
	original := Entry{Namespace: "projects", Tag: "sample", Name: "Sample", ReviewStatus: "declared", ProjectID: "id", ProjectKey: "sample", Owner: "client", DescriptionSource: "client_declared", Summary: "Source summary", ClientEvidence: []Evidence{{Kind: "client_readme", Text: "Source excerpt"}}}
	modelDerived := original
	modelDerived.Summary = "Generated summary"
	modelDerived.DescriptionSource = "model_derived"
	modelDerived.DeclaredSummary = original.Summary
	if !SameReviewSourceIdentity(modelDerived, original) {
		t.Fatal("compatible model-derived summary lost source identity")
	}
	fallbackEqual := original
	if !SameReviewSourceIdentity(fallbackEqual, original) {
		t.Fatal("equal client-declared summary fallback lost source identity")
	}
	fallbackChanged := original
	fallbackChanged.Summary = "Different client summary"
	if SameReviewSourceIdentity(fallbackChanged, original) {
		t.Fatal("different client-declared summary fallback retained source identity")
	}
	changedEvidence := modelDerived
	changedEvidence.ClientEvidence = []Evidence{{Kind: "client_readme", Text: "Changed excerpt"}}
	if SameReviewSourceIdentity(changedEvidence, original) {
		t.Fatal("changed client evidence retained source identity")
	}
	changedDeclaration := modelDerived
	changedDeclaration.DeclaredSummary = "Changed declaration"
	if SameReviewSourceIdentity(changedDeclaration, original) {
		t.Fatal("changed declared summary retained source identity")
	}
}

func validSnapshot() Snapshot {
	return Snapshot{SchemaVersion: 1, Version: "1.0.0", CreatedAt: "2026-10-07T10:00:00Z", Entries: []Entry{{
		Namespace: "projects", Tag: "personal-memory", Name: "Personal Memory", Summary: "Semantic memory service.",
		Aliases: []string{"memory"}, OwnedComponents: []string{"memory API"}, Boundaries: []string{"No task storage."},
		Uses: []string{"Fact recall"}, SharedWith: []string{}, PositiveExamples: []string{"Memory service architecture"},
		NegativeExamples: []string{"A reminder"}, EvidenceRefs: []string{"docs/overview.md"}, ReviewStatus: "approved",
	}}}
}

func TestLoadSnapshotRequiresExpectedDigestAndReadsImmutableOldGeneration(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := validSnapshot()
	hash1, err := Publish(dir, first, "")
	if err != nil {
		t.Fatal(err)
	}
	second := validSnapshot()
	second.ParentHash = hash1
	second.Version = "2.0.0"
	second.Entries[0].Summary = "Updated reviewed summary."
	hash2, err := Publish(dir, second, hash1)
	if err != nil {
		t.Fatal(err)
	}
	old, err := LoadSnapshot(dir, hash1)
	if err != nil {
		t.Fatal(err)
	}
	if old.Entries[0].Summary != first.Entries[0].Summary {
		t.Fatalf("old snapshot changed: %+v", old)
	}
	if _, err := LoadSnapshot(dir, hash2); err != nil {
		t.Fatalf("new snapshot: %v", err)
	}
	if _, err := LoadSnapshot(dir, "../active"); err == nil {
		t.Fatal("accepted invalid expected digest")
	}
	if err := os.Remove(filepath.Join(dir, "snapshots", hash1+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(dir, hash1); err == nil {
		t.Fatal("accepted missing immutable snapshot")
	}
}

func TestLoadSnapshotRejectsSymlinkSnapshotDirectory(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := validSnapshot()
	hash, err := Publish(dir, s, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "snapshots"), filepath.Join(dir, "snapshots-old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "snapshots-old"), filepath.Join(dir, "snapshots")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(dir, hash); err == nil {
		t.Fatal("followed symlinked snapshots directory")
	}
}

func TestHashDeterministicAndPreservesMeaningfulArrayOrder(t *testing.T) {
	s := validSnapshot()
	first, err := Hash(s)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Hash(s)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("hash changed: %s != %s", first, second)
	}
	s.Entries[0].Aliases = []string{"second", "first"}
	reordered, err := Hash(s)
	if err != nil {
		t.Fatal(err)
	}
	if first == reordered {
		t.Fatal("array reordering should change the reviewed snapshot identity")
	}
}

func TestValidateLimitsAndNamespace(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"unknown namespace", func(s *Snapshot) { s.Entries[0].Namespace = "default" }},
		{"bad tag", func(s *Snapshot) { s.Entries[0].Tag = "Personal_Memory" }},
		{"unknown status", func(s *Snapshot) { s.Entries[0].ReviewStatus = "published" }},
		{"oversized description", func(s *Snapshot) { s.Entries[0].Summary = strings.Repeat("x", MaxDescriptionLen+1) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := validSnapshot()
			tc.mutate(&s)
			if err := Validate(s); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	s := validSnapshot()
	s.Entries = make([]Entry, MaxEntries+1)
	if err := Validate(s); err == nil {
		t.Fatal("expected entry count error")
	}
}

func TestCanonicalHashIgnoresJSONObjectKeyOrder(t *testing.T) {
	a := `{"schema_version":1,"catalog_version":"1.0.0","parent_hash":"","created_at":"2026-10-07T10:00:00Z","entries":[]}`
	b := `{"entries":[],"created_at":"2026-10-07T10:00:00Z","parent_hash":"","catalog_version":"1.0.0","schema_version":1}`
	decode := func(raw string) Snapshot {
		var s Snapshot
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	ha, err := Hash(decode(a))
	if err != nil {
		t.Fatal(err)
	}
	hb, err := Hash(decode(b))
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("object key order changed hash: %s != %s", ha, hb)
	}
}

func TestValidateAllowsSanitizedDescriptionSourceOnly(t *testing.T) {
	s := validSnapshot()
	s.Entries[0].DescriptionSource = "model_derived"
	if err := Validate(s); err != nil {
		t.Fatalf("sanitized provenance-only entry rejected: %v", err)
	}
	s.Entries[0].DescriptionSource = "invented"
	if err := Validate(s); err == nil {
		t.Fatal("unknown description source accepted")
	}
}
