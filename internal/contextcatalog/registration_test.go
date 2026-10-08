package contextcatalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func registrationInput() RegistrationInput {
	return RegistrationInput{ProjectKey: "repo:personal-memory", Namespace: "projects", Name: "Personal Memory", Tag: "personal-memory", Summary: "Semantic memory service.", Evidence: []Evidence{{Kind: "client_readme", Text: "Self-hosted semantic memory and Todoist integration."}}}
}

func TestEnsureProjectCreatesDeclaredAndResolvesOwnerScoped(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "registry")
	in := registrationInput()
	created, err := EnsureProject(dir, "owner-a", in)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != "created" || created.ProjectID != scopedProjectID("owner-a", "projects", in.ProjectKey) || created.CatalogHash == "" {
		t.Fatalf("unexpected create result: %#v", created)
	}
	e, hash, err := ResolveProject(dir, "owner-a", created.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if hash != created.CatalogHash || e.ReviewStatus != "declared" || !Eligible(e) || e.Owner != "owner-a" {
		t.Fatalf("unexpected resolved entry/hash: %#v %s", e, hash)
	}
	if _, _, err := ResolveProject(dir, "owner-b", created.ProjectID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner resolve = %v", err)
	}
	existing, err := EnsureProject(dir, "owner-a", in)
	if err != nil || existing.Status != "existing" || existing.CatalogHash != created.CatalogHash {
		t.Fatalf("idempotent ensure = %#v, %v", existing, err)
	}
}

func TestEnsureProjectChangedDescriptionProposesWithoutActiveOverwrite(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "registry")
	in := registrationInput()
	created, err := EnsureProject(dir, "owner-a", in)
	if err != nil {
		t.Fatal(err)
	}
	in.Summary = "Updated summary."
	proposed, err := EnsureProject(dir, "owner-a", in)
	if err != nil || proposed.Status != "proposed_update" || proposed.CatalogHash != created.CatalogHash {
		t.Fatalf("proposal = %#v, %v", proposed, err)
	}
	if _, err := EnsureProject(dir, "owner-a", in); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, proposalDirName, "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("proposal files=%v err=%v", files, err)
	}
	active, hash, err := LoadActive(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hash != created.CatalogHash || active.Entries[0].Summary != registrationInput().Summary {
		t.Fatal("proposed update changed active card")
	}
	info, err := os.Stat(files[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("proposal permissions=%v err=%v", info, err)
	}
}

func TestEnsureProjectTagCollisionIsAmbiguousAndDoesNotMerge(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "registry")
	in := registrationInput()
	if _, err := EnsureProject(dir, "owner-a", in); err != nil {
		t.Fatal(err)
	}
	other := in
	other.ProjectKey = "repo:another-project"
	other.Name = "Another Project"
	result, err := EnsureProject(dir, "owner-a", other)
	if err != nil || result.Status != "ambiguous" {
		t.Fatalf("collision = %#v, %v", result, err)
	}
	s, _, err := LoadActive(dir)
	if err != nil || len(s.Entries) != 1 {
		t.Fatalf("collision changed catalog: entries=%d err=%v", len(s.Entries), err)
	}
}

func TestConcurrentEnsureProjectDoesNotLoseEntries(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "registry")
	inputs := []RegistrationInput{registrationInput(), registrationInput()}
	inputs[1].ProjectKey = "repo:another-project"
	inputs[1].Tag = "another-project"
	inputs[1].Name = "Another Project"
	var wg sync.WaitGroup
	results := make([]RegistrationResult, len(inputs))
	errCh := make(chan error, len(inputs))
	for i := range inputs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			results[i], err = EnsureProject(dir, "owner-a", inputs[i])
			if err != nil {
				errCh <- err
			}
			if results[i].Status == "" {
				errCh <- errors.New("empty registration status")
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	s, _, err := LoadActive(dir)
	if err != nil || len(s.Entries) != 2 {
		t.Fatalf("concurrent registrations lost entry: results=%#v count=%d err=%v", results, len(s.Entries), err)
	}
}

func TestEnsureProjectBindsExactConsistentLegacyTag(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "registry")
	in := registrationInput()
	legacy := validSnapshot()
	legacy.Entries[0].Name = in.Name
	legacy.Entries[0].Summary = in.Summary
	hash, err := Publish(dir, legacy, "")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := EnsureProject(dir, "owner-a", in)
	if err != nil || bound.Status != "existing" || bound.CatalogHash == hash {
		t.Fatalf("legacy bind=%#v err=%v", bound, err)
	}
	e, _, err := ResolveProject(dir, "owner-a", bound.ProjectID)
	if err != nil || e.ReviewStatus != "approved" || e.DescriptionSource != "client_declared" {
		t.Fatalf("bound entry=%#v err=%v", e, err)
	}
}

func TestLegacyTagWithChangedSummaryIsNotBound(t *testing.T) {
	dir := filepath.Join(canonicalTempDir(t), "registry")
	in := registrationInput()
	legacy := validSnapshot()
	legacy.Entries[0].Name = in.Name
	legacy.Entries[0].Summary = "Operator-authored, different summary."
	if _, err := Publish(dir, legacy, ""); err != nil {
		t.Fatal(err)
	}
	result, err := EnsureProject(dir, "owner-a", in)
	if err != nil || result.Status != "proposed_update" {
		t.Fatalf("legacy summary mismatch = %#v, %v", result, err)
	}
	snapshot, hash, err := LoadActive(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hash != result.CatalogHash || snapshot.Entries[0].ProjectID != "" || snapshot.Entries[0].Summary != "Operator-authored, different summary." {
		t.Fatal("legacy mismatch changed or bound active entry")
	}
}

func TestEnsureProjectRejectsBoundsAndSensitiveMaterial(t *testing.T) {
	base := registrationInput()
	tests := []struct {
		name   string
		mutate func(*RegistrationInput)
	}{
		{"wrong namespace", func(in *RegistrationInput) { in.Namespace = "work" }},
		{"short key", func(in *RegistrationInput) { in.ProjectKey = "repo" }},
		{"credential-like key", func(in *RegistrationInput) { in.ProjectKey = "sk-abcdefghijklmnop" }},
		{"credential-like tag", func(in *RegistrationInput) { in.Tag = "sk-abcdefghijklmnop" }},
		{"oversized summary", func(in *RegistrationInput) { in.Summary = strings.Repeat("x", MaxSummaryBytes+1) }},
		{"absolute path", func(in *RegistrationInput) { in.Evidence[0].Text = "Read /Users/alex/project/README.md" }},
		{"credential", func(in *RegistrationInput) { in.Summary = "token=super-secret-value" }},
		{"unsupported evidence", func(in *RegistrationInput) { in.Evidence[0].Kind = "http" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.Evidence = cloneEvidence(base.Evidence)
			tc.mutate(&in)
			if _, err := EnsureProject(filepath.Join(canonicalTempDir(t), "registry"), "owner-a", in); err == nil {
				t.Fatal("invalid registration accepted")
			}
		})
	}
}

func TestDeclaredStatusNeedsNoIdentityWhenSanitized(t *testing.T) {
	e := Entry{Namespace: "projects", Tag: "personal-memory", Name: "Personal Memory", Summary: "Semantic memory service.", ReviewStatus: "declared"}
	s := Snapshot{SchemaVersion: SchemaVersion, Version: "sanitized", CreatedAt: "2026-10-07T10:00:00Z", Entries: []Entry{e}}
	if err := Validate(s); err != nil {
		t.Fatal(err)
	}
	if !Eligible(e) {
		t.Fatal("declared entry should be eligible")
	}
}

func TestEndpointPathsRemainValidRegistrationEvidence(t *testing.T) {
	for _, text := range []string{"Exposes GET /health", "Served at /mcp"} {
		if absolutePathPattern.MatchString(text) {
			t.Fatal("endpoint rejected", text)
		}
	}
	for _, text := range []string{"file /Users/test/source", "file /home", "file /etc/secrets", "file C:\\private"} {
		if !absolutePathPattern.MatchString(text) {
			t.Fatal("private path accepted", text)
		}
	}
}
