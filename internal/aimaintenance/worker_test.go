package aimaintenance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/Dzarlax-AI/personal-memory/internal/factgrouping"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

type fakeProvider struct {
	calls  atomic.Int32
	result func(Input) Proposal
	seen   chan Input
}

func (p *fakeProvider) Propose(_ context.Context, in Input) (Proposal, aijudgment.Usage, error) {
	p.calls.Add(1)
	if p.seen != nil {
		p.seen <- in
	}
	return p.result(in), aijudgment.Usage{InputTokens: 10, OutputTokens: 3, Known: true}, nil
}

func workerConfig(t *testing.T, catalogDir, stateDir string) aipolicy.Config {
	t.Helper()
	c := aipolicy.Off()
	c.CatalogDir = catalogDir
	c.StateDir = stateDir
	c.Maintenance = aipolicy.Maintenance{Enabled: true, Profile: "local", IntervalSeconds: 3600, MaxFactsPerRun: 1, CatalogPublish: "review", GroupingApply: "manual"}
	c.Profiles = map[string]aipolicy.Profile{"local": {Protocol: "openai-compatible-chat", Model: "local-model", Endpoint: "http://localhost:11434/v1/chat/completions", Local: true, TimeoutMS: 1000}}
	c.Egress = aipolicy.Egress{AllowedNamespaces: []string{"projects"}, AllowedProjectTags: []string{"health"}, UnassignedNamespaces: []string{"projects"}}
	c.Limits = aipolicy.Limits{DailyCalls: 20, DailyInputTokens: 2_000_000, ReservationTokens: 131_072}
	return c
}

func setupWorker(t *testing.T, provider Provider) (*Worker, *httptest.Server, string, string, *atomic.Int32, func()) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalogDir := filepath.Join(root, "catalog")
	snap := testCatalog()
	if _, err := contextcatalog.Publish(catalogDir, snap, ""); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	cfg := workerConfig(t, catalogDir, stateDir)
	budget, err := aipolicy.NewBudget(filepath.Join(stateDir, "budget"), cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/collections/memory/points/scroll" {
			http.NotFound(w, r)
			return
		}
		var input struct {
			Offset     any  `json:"offset"`
			WithVector bool `json:"with_vector"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input.WithVector {
			t.Error("worker requested vectors")
		}
		id := 1
		var next any = "1"
		text := "first current fact"
		if input.Offset != nil {
			id = 2
			next = nil
			text = "second current fact"
		}
		payload := map[string]any{"text": text, "namespace": "projects", "tags": []string{}}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "result": map[string]any{"points": []any{map[string]any{"id": id, "payload": payload}}, "next_page_offset": next}})
	}))
	store := qdrant.NewClient(server.URL, "memory")
	worker, err := NewWorker(cfg, store, provider, budget)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return worker, server, catalogDir, stateDir, &requests, func() { server.Close() }
}

func TestWorkerPaginatesAcrossRestartAndWritesUnapprovedManifest(t *testing.T) {
	provider := &fakeProvider{seen: make(chan Input, 3)}
	provider.result = func(in Input) Proposal {
		return Proposal{SchemaVersion: 1, BaseCatalogHash: in.baseCatalogHash, Grouping: []GroupingProposal{{Alias: in.Facts[0].Alias, PrimaryTag: "health", RelatedTags: []string{}, EvidenceAliases: []string{in.Facts[0].Alias}}}, MissingEvidence: []string{}}
	}
	w, _, catalogDir, stateDir, _, closeServer := setupWorker(t, provider)
	defer closeServer()
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := w.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.InProgress || state.Offset != "1" {
		t.Fatalf("cursor not retained after first page: %#v", state)
	}
	input := <-provider.seen
	if len(input.Facts) != 1 || input.Facts[0].Fingerprint == "" {
		t.Fatal("provider did not receive one local fingerprinted fact")
	}
	// Restart by constructing a fresh worker over the same private state and catalog.
	cfg := workerConfig(t, catalogDir, stateDir)
	budget, err := aipolicy.NewBudget(filepath.Join(stateDir, "budget2"), cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWorker(cfg, w.store, provider, budget)
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
	if state.InProgress || len(state.Previous) != 2 {
		t.Fatalf("completed sweep state = %#v", state)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls=%d, want 2", provider.calls.Load())
	}
	entries, err := filepath.Glob(filepath.Join(stateDir, "maintenance", "proposals", "proposal-*.json"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("proposal files=%d err=%v", len(entries), err)
	}
	for _, name := range entries {
		raw, err := aipolicy.ReadPrivate(name, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		var saved privateProposal
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Manifest.Approved || len(saved.Manifest.Changes) != 1 {
			t.Fatal("worker did not emit an unapproved one-change manifest")
		}
		if saved.Manifest.Changes[0].PointID == "" {
			t.Fatal("private proposal omitted local exact ID")
		}
		if len(saved.Dependencies) != 1 || saved.Dependencies[0].PointID == "" || saved.Dependencies[0].Fingerprint == "" || saved.Dependencies[0].Text == "" {
			t.Fatal("private proposal omitted its alias dependency")
		}
	}
}

func TestPrivateProposalKeepsUnmodifiedEvidenceAndCatalogOnlyDependencies(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalogDir := filepath.Join(root, "catalog")
	cat := testCatalog()
	cat.Entries = append(cat.Entries,
		contextcatalog.Entry{Namespace: "projects", Tag: "alpha", Name: "Alpha", Summary: "Alpha", Aliases: []string{}, OwnedComponents: []string{}, Boundaries: []string{}, Uses: []string{}, SharedWith: []string{}, PositiveExamples: []string{}, NegativeExamples: []string{}, EvidenceRefs: []string{}, ReviewStatus: "approved"},
		contextcatalog.Entry{Namespace: "projects", Tag: "beta", Name: "Beta", Summary: "Beta", Aliases: []string{}, OwnedComponents: []string{}, Boundaries: []string{}, Uses: []string{}, SharedWith: []string{}, PositiveExamples: []string{}, NegativeExamples: []string{}, EvidenceRefs: []string{}, ReviewStatus: "approved"},
	)
	if _, err := contextcatalog.Publish(catalogDir, cat, ""); err != nil {
		t.Fatal(err)
	}
	cfg := workerConfig(t, catalogDir, filepath.Join(root, "state"))
	w := &Worker{cfg: cfg}
	base, err := contextcatalog.Hash(cat)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("a", 64)
	input := Input{Catalog: cat, baseCatalogHash: base, Facts: []Fact{
		{Alias: "f000", Text: "proposed grouping fact", Namespace: "projects", Tags: []string{}, Fingerprint: fingerprint},
		{Alias: "f001", Text: "unchanged supporting evidence", Namespace: "projects", Tags: []string{}, Fingerprint: strings.Repeat("b", 64)},
	}}
	points := map[string]qdrant.ScrollPoint{
		"f000": {ID: "point-a"},
		"f001": {ID: "point-b"},
	}
	group := factgrouping.Group{}
	records := map[string]factRecord{"point-a": {Fingerprint: fingerprint, Group: group}, "point-b": {Fingerprint: strings.Repeat("b", 64), Group: group}}
	draft := Proposal{SchemaVersion: 1, BaseCatalogHash: base, Catalog: &contextcatalog.Snapshot{SchemaVersion: 1, Version: "1.1", ParentHash: base, CreatedAt: "2026-10-07T10:00:00Z", Entries: []contextcatalog.Entry{{Namespace: "projects", Tag: "new-area", Name: "New area", Summary: "Draft area", Aliases: []string{}, OwnedComponents: []string{}, Boundaries: []string{}, Uses: []string{}, SharedWith: []string{}, PositiveExamples: []string{}, NegativeExamples: []string{}, EvidenceRefs: []string{"f001"}, ReviewStatus: "draft"}}}, Grouping: []GroupingProposal{}, MissingEvidence: []string{}}
	if err := validateProposal(input, draft); err != nil {
		t.Fatalf("catalog evidence alias was rejected: %v", err)
	}
	invalidEvidence := draft
	invalidEvidence.Catalog = draft.Catalog
	invalidSnapshot := *draft.Catalog
	invalidSnapshot.Entries = append([]contextcatalog.Entry(nil), draft.Catalog.Entries...)
	invalidSnapshot.Entries[0].EvidenceRefs = []string{"private/path.md"}
	invalidEvidence.Catalog = &invalidSnapshot
	if err := validateProposal(input, invalidEvidence); err == nil {
		t.Fatal("catalog evidence reference without a supplied alias was accepted")
	}
	private, err := w.makePrivateProposal(base, draft, input, points, records)
	if err != nil {
		t.Fatal(err)
	}
	if len(private.Dependencies) != 2 || private.Dependencies[1].Alias != "f001" || private.Dependencies[1].PointID != "point-b" || private.Dependencies[1].Text != "unchanged supporting evidence" {
		t.Fatalf("unmodified evidence dependency was not preserved: %#v", private.Dependencies)
	}
	if len(private.Manifest.Changes) != 0 {
		t.Fatalf("catalog-only proposal unexpectedly has grouping changes: %#v", private.Manifest.Changes)
	}
	input.Facts[0].PrimaryTag = "alpha"
	input.Facts[0].Tags = []string{"alpha", "custom-label"}
	private, err = w.makePrivateProposal(base, Proposal{SchemaVersion: 1, BaseCatalogHash: base, Grouping: []GroupingProposal{{Alias: "f000", PrimaryTag: "beta", RelatedTags: []string{}, EvidenceAliases: []string{"f000", "f001"}}}, MissingEvidence: []string{}}, input, points, records)
	if err != nil {
		t.Fatal(err)
	}
	if len(private.Manifest.Changes) != 1 {
		t.Fatalf("existing primary-tag change was not proposed: %#v", private.Manifest.Changes)
	}
	after := private.Manifest.Changes[0].After
	if after.PrimaryTag != "beta" || contains(after.Tags, "alpha") || !contains(after.Tags, "custom-label") || !contains(after.Tags, "beta") {
		t.Fatalf("primary-tag proposal lost unrelated tags or retained replaced primary: %#v", after)
	}
}

func TestWorkerStartDoesNotRunImmediatelyAndStopWaits(t *testing.T) {
	provider := &fakeProvider{result: func(in Input) Proposal {
		return Proposal{SchemaVersion: 1, BaseCatalogHash: in.baseCatalogHash, Grouping: []GroupingProposal{}, MissingEvidence: []string{}}
	}}
	w, _, _, _, requests, closeServer := setupWorker(t, provider)
	defer closeServer()
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if provider.calls.Load() != 0 || requests.Load() != 0 {
		t.Fatal("worker called provider at startup")
	}
	cancel()
	if err := w.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.Stop()
}

func TestWorkerProviderBindingChangeRescansUnchangedFacts(t *testing.T) {
	provider := &fakeProvider{result: func(in Input) Proposal {
		return Proposal{SchemaVersion: 1, BaseCatalogHash: in.baseCatalogHash, Grouping: []GroupingProposal{}, MissingEvidence: []string{}}
	}}
	w, _, _, _, _, closeServer := setupWorker(t, provider)
	defer closeServer()
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("initial full sweep inference calls=%d", provider.calls.Load())
	}
	w.cfg.Profiles["local"] = func() aipolicy.Profile {
		p := w.cfg.Profiles["local"]
		p.Model = "changed-model"
		return p
	}()
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 3 {
		t.Fatalf("provider/model binding change did not rescan facts: calls=%d", provider.calls.Load())
	}
	state, err := w.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.BindingHash == "" || state.InProgress == false {
		t.Fatalf("expected new binding and in-progress rescan state, got %#v", state)
	}
}

func TestWorkerSkipsFactsOutsideEgressAndNonCurrent(t *testing.T) {
	// The normal test Qdrant response is unassigned in projects; this test directly
	// exercises filtering without any provider or storage mutation.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := workerConfig(t, filepath.Join(root, "catalog"), filepath.Join(root, "state"))
	w := &Worker{cfg: cfg}
	page := []qdrant.ScrollPoint{{ID: "1", Payload: map[string]interface{}{"text": "fact", "namespace": "projects", "tags": []interface{}{"unknown"}}}, {ID: "2", Payload: map[string]interface{}{"text": "old", "namespace": "projects", "tags": []interface{}{}, "lifecycle_state": "historical"}}}
	input, _, _, _ := w.makeInput(page, testCatalog(), "hash", workerState{Previous: map[string]factRecord{}, Seen: map[string]factRecord{}}, map[string]bool{})
	if len(input.Facts) != 0 {
		t.Fatal("out-of-scope or historical facts reached provider")
	}
}
