package aimaintenance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/Dzarlax-AI/personal-memory/internal/factgrouping"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func setupEvidenceWorker(t *testing.T, policy string, result func(Input) Proposal) (*Worker, *fakeProvider, string, func()) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalogDir, stateDir := filepath.Join(root, "catalog"), filepath.Join(root, "state")
	cat := contextcatalog.Snapshot{SchemaVersion: 1, Version: "declared", CreatedAt: "2026-10-08T10:00:00Z", Entries: []contextcatalog.Entry{declaredTestEntry()}}
	if _, err := contextcatalog.Publish(catalogDir, cat, ""); err != nil {
		t.Fatal(err)
	}
	cfg := workerConfig(t, catalogDir, stateDir)
	cfg.Maintenance.CatalogPublish = policy
	budget, err := aipolicy.NewBudget(filepath.Join(stateDir, "budget"), cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/collections/memory/points/scroll" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "result": map[string]any{"points": []any{}, "next_page_offset": nil}})
	}))
	store := qdrant.NewClient(server.URL, "memory")
	provider := &fakeProvider{result: result}
	worker, err := NewWorker(cfg, store, provider, budget)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return worker, provider, catalogDir, server.Close
}

func TestEvidenceOnlyBootstrapRunsOnceAcrossRestart(t *testing.T) {
	result := func(in Input) Proposal {
		return Proposal{SchemaVersion: 1, BaseCatalogHash: in.baseCatalogHash, Grouping: []GroupingProposal{}, MissingEvidence: []string{}}
	}
	w, provider, catalogDir, closeServer := setupEvidenceWorker(t, "review", result)
	defer closeServer()
	for i := 0; i < 2; i++ {
		if err := w.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	state, err := w.loadState()
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWorker(w.cfg, w.store, provider, w.budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = restarted.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 1 || state.EvidenceInferenceKey == "" || len(provider.seen) > 0 {
		t.Fatalf("evidence-only bootstrap repeated or missing: calls=%d state=%#v", provider.calls.Load(), state)
	}
	cat, _, err := contextcatalog.LoadActive(catalogDir)
	if err != nil || cat.Entries[0].Summary != "Initial health description." {
		t.Fatalf("review policy changed active card: %#v err=%v", cat, err)
	}
}

func TestEvidenceBoundedPublicationPreservesDeclaredIdentityAndPersistsProposalFirst(t *testing.T) {
	result := func(in Input) Proposal {
		candidate := modelDescriptionProposal(in.baseCatalogHash, in.Catalog.Entries)
		candidate.Entries[0].Summary = "A mobile health application."
		candidate.Entries[0].OwnedComponents = []string{"Mobile app"}
		candidate.Entries[0].EvidenceRefs = []string{"c000"}
		return Proposal{SchemaVersion: 1, BaseCatalogHash: in.baseCatalogHash, Catalog: &candidate, Grouping: []GroupingProposal{}, MissingEvidence: []string{}}
	}
	w, provider, catalogDir, closeServer := setupEvidenceWorker(t, "evidence_bounded", result)
	defer closeServer()
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("evidence bootstrap calls=%d", provider.calls.Load())
	}
	entries, err := filepath.Glob(filepath.Join(w.dir, "proposals", "proposal-*.json"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("proposal artifact missing before/after publish: %v err=%v", entries, err)
	}
	active, _, err := contextcatalog.LoadActive(catalogDir)
	if err != nil {
		t.Fatal(err)
	}
	e := active.Entries[0]
	if e.Summary != "A mobile health application." || e.DescriptionSource != "model_derived" || e.ReviewStatus != "declared" || e.ProjectID != strings.Repeat("a", 64) || e.ProjectKey != "repo:health-project" || e.Owner != "server-owner" || e.DeclaredSummary != "Initial health description." || !clientEvidenceEqual(e.ClientEvidence, declaredTestEntry().ClientEvidence) {
		t.Fatalf("published card lost source identity/provenance: %#v", e)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 1 {
		t.Fatal("unchanged client evidence triggered a repeat inference after publication")
	}
}

func TestEvidenceBoundedPublicationLeavesStaleFactAsProposal(t *testing.T) {
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
	original := map[string]any{"text": "original", "namespace": "projects", "tags": []any{}}
	changed := map[string]any{"text": "changed", "namespace": "projects", "tags": []any{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/points/point-1") {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "result": map[string]any{"id": "point-1", "payload": changed}})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	input.baseCatalogHash = base
	fingerprint, err := factFingerprint(original)
	if err != nil {
		t.Fatal(err)
	}
	input.Facts = []Fact{{Alias: "f000", Text: "original", Namespace: "projects", Fingerprint: fingerprint}}
	p := Proposal{SchemaVersion: 1, BaseCatalogHash: base, Catalog: func() *contextcatalog.Snapshot {
		c := modelDescriptionProposal(base, input.Catalog.Entries)
		c.Entries[0].Summary = "updated"
		c.Entries[0].EvidenceRefs = []string{"c000", "f000"}
		return &c
	}(), Grouping: []GroupingProposal{}, MissingEvidence: []string{}}
	cfg := workerConfig(t, catalogDir, filepath.Join(root, "state"))
	cfg.Maintenance.CatalogPublish = "evidence_bounded"
	w := &Worker{cfg: cfg, store: qdrant.NewClient(server.URL, "memory")}
	private := privateProposal{BaseCatalogHash: base, Dependencies: []privateDependency{{Alias: "f000", PointID: "point-1", Fingerprint: fingerprint, Namespace: "projects", Text: "original"}}, CatalogDependencies: []privateCatalogDependency{{Alias: "c000", ProjectID: input.Catalog.Entries[0].ProjectID, Owner: input.Catalog.Entries[0].Owner, ProjectKey: input.Catalog.Entries[0].ProjectKey, Namespace: "projects", Tag: "health", Kind: "user_declared", Digest: input.CatalogEvidence[0].Digest}, {Alias: "c001", ProjectID: input.Catalog.Entries[0].ProjectID, Owner: input.Catalog.Entries[0].Owner, ProjectKey: input.Catalog.Entries[0].ProjectKey, Namespace: "projects", Tag: "health", Kind: "client_readme", Digest: input.CatalogEvidence[1].Digest}}}
	published, err := w.tryPublishDescription(context.Background(), current, base, input, p, private)
	if err != nil || published {
		t.Fatalf("stale fact was published: published=%v err=%v", published, err)
	}
	active, hash, err := contextcatalog.LoadActive(catalogDir)
	if err != nil {
		t.Fatal(err)
	}
	if hash != base || active.Entries[0].Summary != current.Entries[0].Summary {
		t.Fatal("stale fact changed active catalog")
	}
}

func factFingerprint(payload map[string]any) (string, error) {
	return factgrouping.Fingerprint(payload)
}
