package aimaintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/Dzarlax-AI/personal-memory/internal/factgrouping"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func declaredTestEntry() contextcatalog.Entry {
	return contextcatalog.Entry{
		Namespace: "projects", Tag: "health", Name: "Health", Summary: "Initial health description.",
		Aliases: []string{}, OwnedComponents: []string{}, Boundaries: []string{}, Uses: []string{}, SharedWith: []string{}, PositiveExamples: []string{}, NegativeExamples: []string{}, EvidenceRefs: []string{}, ReviewStatus: "declared",
		ProjectID: strings.Repeat("a", 64), ProjectKey: "repo:health-project", Owner: "server-owner", DescriptionSource: "client_declared", DeclaredSummary: "Initial health description.",
		ClientEvidence: []contextcatalog.Evidence{{Kind: "client_readme", Text: "Owns the mobile health application."}},
	}
}

func descriptionTestInput() (contextcatalog.Snapshot, Input) {
	s := contextcatalog.Snapshot{SchemaVersion: 1, Version: "registry", CreatedAt: "2026-10-08T10:00:00Z", Entries: []contextcatalog.Entry{declaredTestEntry()}}
	hash, _ := contextcatalog.Hash(s)
	evidence := makeCatalogEvidence(s)
	return s, Input{Catalog: s, CatalogEvidence: evidence, Facts: []Fact{}, baseCatalogHash: hash}
}

func modelDescriptionProposal(base string, entries []contextcatalog.Entry) contextcatalog.Snapshot {
	out := make([]contextcatalog.Entry, len(entries))
	for i, source := range entries {
		out[i] = source
		out[i].ReviewStatus = "draft"
		out[i].ProjectID, out[i].ProjectKey, out[i].Owner = "", "", ""
		out[i].ClientEvidence, out[i].DescriptionSource, out[i].DeclaredSummary = nil, "", ""
	}
	return contextcatalog.Snapshot{SchemaVersion: 1, Version: "model-proposal", ParentHash: base, CreatedAt: "2026-10-08T10:01:00Z", Entries: out}
}

func TestMergeEvidenceBoundedDescriptionPreservesRegistryIdentityAndDeclaredSources(t *testing.T) {
	current, input := descriptionTestInput()
	candidate := modelDescriptionProposal(input.baseCatalogHash, input.Catalog.Entries)
	candidate.Entries[0].Summary = "A mobile health application."
	candidate.Entries[0].OwnedComponents = []string{"Mobile app"}
	candidate.Entries[0].EvidenceRefs = []string{"c000"}
	merged, changed := mergeDescriptiveProposal(current, input.Catalog, candidate, input)
	if !changed {
		t.Fatal("valid evidence-backed description was not merged")
	}
	e := merged.Entries[0]
	if e.Summary != "A mobile health application." || e.DescriptionSource != "model_derived" || e.ReviewStatus != "declared" {
		t.Fatalf("description provenance/status mismatch: %#v", e)
	}
	if e.ProjectID != current.Entries[0].ProjectID || e.ProjectKey != current.Entries[0].ProjectKey || e.Owner != current.Entries[0].Owner || e.DeclaredSummary != current.Entries[0].DeclaredSummary || !clientEvidenceEqual(e.ClientEvidence, current.Entries[0].ClientEvidence) {
		t.Fatal("model update changed registry identity or original client evidence")
	}
}

func TestMergeEvidenceBoundedRejectsUnknownCitationAndIdentityChanges(t *testing.T) {
	current, input := descriptionTestInput()
	base := input.baseCatalogHash
	tests := []struct {
		name   string
		mutate func(*contextcatalog.Snapshot)
	}{
		{"unknown evidence citation", func(p *contextcatalog.Snapshot) {
			p.Entries[0].Summary = "new"
			p.Entries[0].EvidenceRefs = []string{"c099"}
		}},
		{"identity rename", func(p *contextcatalog.Snapshot) { p.Entries[0].Name = "Different" }},
		{"approval escalation", func(p *contextcatalog.Snapshot) { p.Entries[0].ReviewStatus = "approved" }},
		{"new card", func(p *contextcatalog.Snapshot) {
			p.Entries = append(p.Entries, p.Entries[0])
			p.Entries[1].Tag = "new-project"
		}},
		{"deleted card", func(p *contextcatalog.Snapshot) { p.Entries = nil }},
		{"missing evidence", func(p *contextcatalog.Snapshot) { p.Entries[0].Summary = "new" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := modelDescriptionProposal(base, input.Catalog.Entries)
			tc.mutate(&p)
			if _, changed := mergeDescriptiveProposal(current, input.Catalog, p, input); changed {
				t.Fatal("unsafe model catalog was publishable")
			}
		})
	}
}

func TestEvidenceOnlyInferenceKeyUsesOnlyOriginalClientSources(t *testing.T) {
	entry := declaredTestEntry()
	s := contextcatalog.Snapshot{SchemaVersion: 1, Version: "a", CreatedAt: "2026-10-08T10:00:00Z", Entries: []contextcatalog.Entry{entry}}
	a := makeCatalogEvidence(s)
	entry.Summary = "Model derived summary that must not become evidence."
	entry.DescriptionSource = "model_derived"
	s.Entries[0] = entry
	b := makeCatalogEvidence(s)
	if len(a) != 2 || len(b) != 2 || !sameCatalogEvidence(a, b) {
		t.Fatalf("model-derived card fields changed source evidence: %#v %#v", a, b)
	}
	if a[0].Kind != "user_declared" || a[1].Kind != "client_readme" {
		t.Fatalf("unexpected source kinds: %#v", a)
	}
}

func TestEvidenceInferenceProposalArtifactPreventsRepeatAfterStateWriteFailure(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "maintenance")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	_, input := descriptionTestInput()
	cfg := workerConfig(t, filepath.Join(t.TempDir(), "catalog"), filepath.Join(t.TempDir(), "state"))
	w := &Worker{cfg: cfg, dir: dir}
	key := w.evidenceInferenceKey(input.CatalogEvidence)
	if key == "" || key == w.evidenceInferenceKey(nil) {
		t.Fatal("evidence-only key missing")
	}
	if err := os.MkdirAll(filepath.Join(dir, "proposals"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := w.saveProposal(privateProposal{SchemaVersion: 1, EvidenceInferenceKey: key}); err != nil {
		t.Fatal(err)
	}
	if !w.hasEvidenceProposal(key) {
		t.Fatal("saved proposal was not discovered for retry suppression")
	}
}

func TestEvidenceBoundedCatalogConflictAndCancellationFailClosed(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalogDir := filepath.Join(root, "catalog")
	current, input := descriptionTestInput()
	base, err := contextcatalog.Publish(catalogDir, current, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate := modelDescriptionProposal(base, input.Catalog.Entries)
	candidate.Entries[0].Summary = "A mobile health application."
	candidate.Entries[0].EvidenceRefs = []string{"c000"}
	cfg := workerConfig(t, catalogDir, filepath.Join(root, "state"))
	cfg.Maintenance.CatalogPublish = "evidence_bounded"
	w := &Worker{cfg: cfg}
	private := privateProposal{BaseCatalogHash: base}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if published, err := w.tryPublishDescription(ctx, current, base, input, Proposal{Catalog: &candidate}, private); err != nil || published {
		t.Fatalf("canceled publication=%v err=%v", published, err)
	}
	changed := cloneSnapshot(current)
	changed.Entries[0].Summary = "Manual change."
	changed.ParentHash = base
	changed.CreatedAt = "2026-10-08T10:02:00Z"
	if _, err := contextcatalog.Publish(catalogDir, changed, base); err != nil {
		t.Fatal(err)
	}
	if published, err := w.tryPublishDescription(context.Background(), current, base, input, Proposal{Catalog: &candidate}, private); err != nil || published {
		t.Fatalf("stale catalog publication=%v err=%v", published, err)
	}
}

func TestStaleFactFingerprintBlocksEvidenceBoundedPublication(t *testing.T) {
	originalPayload := map[string]interface{}{"text": "original", "namespace": "projects", "tags": []interface{}{}}
	changedPayload := map[string]interface{}{"text": "changed", "namespace": "projects", "tags": []interface{}{}}
	fingerprint, err := factgrouping.Fingerprint(originalPayload)
	if err != nil {
		t.Fatal(err)
	}
	facts := []Fact{{Alias: "f000", Text: "original", Namespace: "projects", Fingerprint: fingerprint}}
	dependencies := []privateDependency{{Alias: "f000", PointID: "point-1", Fingerprint: fingerprint, Namespace: "projects", Text: "original"}}
	get := func(context.Context, string) (qdrant.Point, bool, error) {
		return qdrant.Point{ID: "point-1", Payload: changedPayload}, true, nil
	}
	if verifyFactDependenciesWithGetter(context.Background(), facts, dependencies, get) {
		t.Fatal("changed fact fingerprint remained eligible for publication")
	}
}

func TestConcurrentGroupingChangeBlocksEvidenceBoundedPublication(t *testing.T) {
	payload := map[string]interface{}{"text": "same fact", "namespace": "projects", "tags": []interface{}{"health"}, "primary_tag": "health"}
	fingerprint, err := factgrouping.Fingerprint(payload)
	if err != nil {
		t.Fatal(err)
	}
	facts := []Fact{{Alias: "f000", Text: "same fact", Namespace: "projects", Tags: []string{"health"}, PrimaryTag: "health", Fingerprint: fingerprint}}
	dep := []privateDependency{{Alias: "f000", PointID: "point-1", Fingerprint: fingerprint, Namespace: "projects", Text: "same fact", Before: factgrouping.GroupFrom(payload)}}
	mutated := map[string]interface{}{"text": "same fact", "namespace": "projects", "tags": []interface{}{"personal"}, "primary_tag": "personal"}
	get := func(context.Context, string) (qdrant.Point, bool, error) {
		return qdrant.Point{ID: "point-1", Payload: mutated}, true, nil
	}
	if verifyFactDependenciesWithGetter(context.Background(), facts, dep, get) {
		t.Fatal("concurrent grouping change remained eligible for publication")
	}
}

func TestCatalogEvidenceDigestIsBoundToExactText(t *testing.T) {
	text := "client excerpt"
	sum := sha256.Sum256([]byte(text))
	in := Input{Catalog: contextcatalog.Snapshot{SchemaVersion: 1, Version: "t", CreatedAt: "2026-10-08T00:00:00Z", Entries: []contextcatalog.Entry{{Namespace: "projects", Tag: "health", Name: "Health", Summary: "Health", ReviewStatus: "approved"}}}, CatalogEvidence: []CatalogEvidence{{Alias: "c000", Namespace: "projects", Tag: "health", Kind: "user_declared", Digest: hex.EncodeToString(sum[:]), Text: text}}}
	if err := validateInput(in); err != nil {
		t.Fatal(err)
	}
	in.CatalogEvidence[0].Text = "different excerpt"
	if err := validateInput(in); err == nil {
		t.Fatal("evidence text change was not detected")
	}
}
