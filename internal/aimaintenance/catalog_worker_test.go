package aimaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func catalogFixture(n int) contextcatalog.Snapshot {
	entries := make([]contextcatalog.Entry, 0, n)
	for i := 0; i < n; i++ {
		tag := fmt.Sprintf("project-%02d", i)
		entries = append(entries, contextcatalog.Entry{Namespace: "projects", Tag: tag, Name: "Project " + tag, Summary: "Declared " + tag, Aliases: []string{}, OwnedComponents: []string{}, Boundaries: []string{}, Uses: []string{}, SharedWith: []string{}, PositiveExamples: []string{}, NegativeExamples: []string{}, EvidenceRefs: []string{}, ReviewStatus: "declared", ProjectID: strings.Repeat(fmt.Sprintf("%x", i%16), 64), ProjectKey: "repo:" + tag, Owner: "fixture-owner", DescriptionSource: "client_declared", DeclaredSummary: "Declared " + tag, ClientEvidence: []contextcatalog.Evidence{{Kind: "client_readme", Text: "Original client evidence for " + tag}}})
	}
	return contextcatalog.Snapshot{SchemaVersion: 1, Version: "registry", CreatedAt: "2026-10-08T10:00:00Z", Entries: entries}
}

func cloneTestProfiles(in map[string]aipolicy.Profile) map[string]aipolicy.Profile {
	out := make(map[string]aipolicy.Profile, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func TestCatalogOnlyThirtyTwoCardsWithMaximumSourceCountNeverTruncateOrStarve(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 32, 32, "local")
	cat, base, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	cat.ParentHash = base
	for i := range cat.Entries {
		entry := &cat.Entries[i]
		entry.DeclaredSummary = strings.Repeat("s", 8)
		entry.ClientEvidence = make([]contextcatalog.Evidence, 4)
		for j := range entry.ClientEvidence {
			entry.ClientEvidence[j] = contextcatalog.Evidence{Kind: "client_readme", Text: fmt.Sprintf("%s-%d", entry.Tag, j)}
		}
	}
	if _, err := contextcatalog.Publish(cfg.CatalogDir, cat, base); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for runs := 0; runs < 40 && len(seen) < 32; runs++ {
		if err := w.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		select {
		case in := <-p.seen:
			if len(in.CatalogEvidence) > maxCatalogEvidenceItems || !batchEvidenceFits(in.CatalogEvidence) {
				t.Fatalf("batch exceeded evidence bounds: %d", len(in.CatalogEvidence))
			}
			body, err := BuildRequest(cfg.Profiles["local"], in)
			if err != nil || len(body) > MaxRequestBytes {
				t.Fatalf("request bound: bytes=%d err=%v", len(body), err)
			}
			perCard := map[string]int{}
			for _, ev := range in.CatalogEvidence {
				perCard[ev.Tag]++
			}
			for _, card := range in.Catalog.Entries {
				if perCard[card.Tag] != 5 {
					t.Fatalf("card %s was partially represented: %d sources", card.Tag, perCard[card.Tag])
				}
				if seen[card.Tag] {
					t.Fatalf("card %s starved or repeated before coverage", card.Tag)
				}
				seen[card.Tag] = true
			}
		default:
		}
	}
	if len(seen) != 32 {
		t.Fatalf("expected all 32 source-complete cards, got %d", len(seen))
	}
}

func TestCatalogOnlyByteLimitDefersWholeCard(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 32, 32, "local")
	cat, base, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	cat.ParentHash = base
	for i := range cat.Entries {
		entry := &cat.Entries[i]
		if i < 4 {
			entry.DeclaredSummary = strings.Repeat("s", 2000)
			entry.ClientEvidence = []contextcatalog.Evidence{{Kind: "client_readme", Text: strings.Repeat("a", 1000)}, {Kind: "client_agents", Text: strings.Repeat("b", 1000)}, {Kind: "client_readme", Text: strings.Repeat("c", 1000)}, {Kind: "client_agents", Text: strings.Repeat("d", 1000)}}
		} else {
			entry.DeclaredSummary = "s"
			entry.ClientEvidence = []contextcatalog.Evidence{{Kind: "client_readme", Text: "e"}, {Kind: "client_readme", Text: "f"}, {Kind: "client_readme", Text: "g"}, {Kind: "client_readme", Text: "h"}}
		}
	}
	if _, err := contextcatalog.Publish(cfg.CatalogDir, cat, base); err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := <-p.seen
	if len(first.Catalog.Entries) != 2 || !batchEvidenceFits(first.CatalogEvidence) {
		t.Fatalf("expected two full cards under 16 KiB, got cards=%d bytes-fit=%v", len(first.Catalog.Entries), batchEvidenceFits(first.CatalogEvidence))
	}
	for _, ev := range first.CatalogEvidence {
		if len(ev.Text) == 0 {
			t.Fatal("empty evidence")
		}
	}
	seen := map[string]bool{}
	for _, card := range first.Catalog.Entries {
		seen[card.Tag] = true
	}
	for runs := 0; runs < 40 && len(seen) < 32; runs++ {
		if err := w.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		select {
		case in := <-p.seen:
			for _, card := range in.Catalog.Entries {
				if seen[card.Tag] {
					t.Fatalf("duplicate card %s", card.Tag)
				}
				seen[card.Tag] = true
			}
		default:
		}
	}
	if len(seen) != 32 {
		t.Fatalf("byte-bound deferral starved later cards: %d/32", len(seen))
	}
}

func TestCatalogOnlyRecoversReceiptAfterStateWriteFailure(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 1, 1, "local")
	statePath := filepath.Join(cfg.StateDir, "maintenance-catalog", "state.json")
	p.after = func(Input) {
		p.after = nil
		_ = os.Remove(statePath)
		_ = os.Mkdir(statePath, 0700) // Force the post-dispatch and final state writes to fail.
	}
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("expected state write failure")
	}
	if p.calls.Load() != 1 {
		t.Fatalf("expected one completed provider call, got %d", p.calls.Load())
	}
	if err := os.RemoveAll(statePath); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWorker(cfg, nil, p, w.budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("saved receipt was not reused after state failure; calls=%d", p.calls.Load())
	}
}

func TestCatalogOnlyStaleReceiptIsNotRebasedAcrossCatalogBase(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 1, 1, "local")
	statePath := filepath.Join(cfg.StateDir, "maintenance-catalog", "state.json")
	p.after = func(Input) { p.after = nil; _ = os.Remove(statePath); _ = os.Mkdir(statePath, 0700) }
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("expected state write failure")
	}
	if err := os.RemoveAll(statePath); err != nil {
		t.Fatal(err)
	}
	cat, base, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	cat.CreatedAt = "2026-10-09T10:00:00Z"
	cat.ParentHash = base
	if _, err := contextcatalog.Publish(cfg.CatalogDir, cat, base); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWorker(cfg, nil, p, w.budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 2 {
		t.Fatalf("stale-base receipt was reused or rebased: calls=%d", p.calls.Load())
	}
}

func TestCatalogOnlyDeniedAndEvidenceFreeCardsDoNotDispatch(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 1, 1, "local")
	w.cfg.Egress.AllowedProjectTags = nil
	w.catalog.cfg.Egress.AllowedProjectTags = nil
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 0 {
		t.Fatal("denied project card reached the provider")
	}
	w.cfg.Egress.AllowedProjectTags = []string{"project-00"}
	w.catalog.cfg.Egress.AllowedProjectTags = []string{"project-00"}
	cat, base, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	cat.ParentHash = base
	entry := &cat.Entries[0]
	entry.DescriptionSource = "model_derived"
	entry.DeclaredSummary = ""
	entry.ClientEvidence = nil
	if _, err := contextcatalog.Publish(cfg.CatalogDir, cat, base); err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 0 {
		t.Fatal("evidence-free project card reached the provider")
	}
	status, err := ReadCatalogStatus(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastOutcome != "skipped" || status.Skipped != 1 {
		t.Fatalf("missing-source outcome not visible: %#v", status)
	}
}

func TestCatalogOnlyMakesZeroQdrantRequests(t *testing.T) {
	baseWorker, p, cfg, _ := setupCatalogOnly(t, 1, 1, "local")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	w, err := NewWorker(cfg, qdrant.NewClient(server.URL, "memory"), p, baseWorker.budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("catalog-only run made %d Qdrant requests", requests.Load())
	}
}

func catalogOnlyDescriptionProposal(in Input) Proposal {
	entries := make([]contextcatalog.Entry, len(in.Catalog.Entries))
	for i, source := range in.Catalog.Entries {
		entry := source
		entry.ReviewStatus = "draft"
		entry.ProjectID, entry.ProjectKey, entry.Owner = "", "", ""
		entry.ClientEvidence, entry.DescriptionSource, entry.DeclaredSummary = nil, "", ""
		entry.Summary = "Proposed description for " + source.Tag
		entry.Aliases = []string{}
		entry.OwnedComponents = []string{}
		entry.Boundaries = []string{}
		entry.Uses = []string{}
		entry.SharedWith = []string{}
		entry.PositiveExamples = []string{}
		entry.NegativeExamples = []string{}
		entry.EvidenceRefs = []string{fmt.Sprintf("c%03d", i*2)}
		entries[i] = entry
	}
	candidate := contextcatalog.Snapshot{SchemaVersion: in.Catalog.SchemaVersion, Version: "catalog-model-proposal", ParentHash: in.baseCatalogHash, CreatedAt: in.Catalog.CreatedAt, Entries: entries}
	return Proposal{SchemaVersion: 1, BaseCatalogHash: in.baseCatalogHash, Catalog: &candidate, Grouping: []GroupingProposal{}, MissingEvidence: []string{}}
}

func TestCatalogOnlyPublicationPreservesUnselectedAndDoesNotLoopOnOwnDescription(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 2, 1, "local")
	w.cfg.Maintenance.CatalogPublish, w.catalog.cfg.Maintenance.CatalogPublish = "evidence_bounded", "evidence_bounded"
	p.result = catalogOnlyDescriptionProposal
	original, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	active, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	if active.Entries[0].Summary != "Proposed description for project-00" {
		t.Fatalf("selected card was not published: %q", active.Entries[0].Summary)
	}
	if active.Entries[1].Summary != original.Entries[1].Summary || active.Entries[1].DescriptionSource != original.Entries[1].DescriptionSource {
		t.Fatal("unselected card changed during selected publication")
	}
	w2, p2, _, _ := setupCatalogOnly(t, 1, 1, "local")
	w2.cfg.Maintenance.CatalogPublish, w2.catalog.cfg.Maintenance.CatalogPublish = "evidence_bounded", "evidence_bounded"
	p2.result = catalogOnlyDescriptionProposal
	if err := w2.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status1, err := ReadCatalogStatus(w2.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if status1.LastOutcome != "published" {
		t.Fatalf("published outcome missing: %#v", status1)
	}
	if err := w2.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p2.calls.Load() != 1 {
		t.Fatalf("own publication caused a model feedback loop: calls=%d", p2.calls.Load())
	}
	status2, err := ReadCatalogStatus(w2.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if status2.LastOutcome != "no_changes" || status2.LastDispatchAt != status1.LastDispatchAt {
		t.Fatalf("no-change tick falsified dispatch status: before=%#v after=%#v", status1, status2)
	}
}

func TestCatalogOnlyPublishedOriginalProjectionPreventsLoopAndTracksClientChanges(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 2, 2, "local")
	w.cfg.Maintenance.CatalogPublish, w.catalog.cfg.Maintenance.CatalogPublish = "evidence_bounded", "evidence_bounded"
	cat, base, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cat.Entries {
		cat.Entries[i].Aliases = []string{"original-alias-" + cat.Entries[i].Tag}
		cat.Entries[i].Boundaries = []string{"original-boundary-" + cat.Entries[i].Tag}
	}
	cat.ParentHash = base
	if _, err := contextcatalog.Publish(cfg.CatalogDir, cat, base); err != nil {
		t.Fatal(err)
	}
	modelLabel := "first"
	p.result = func(in Input) Proposal {
		proposal := catalogOnlyDescriptionProposal(in)
		for i := range proposal.Catalog.Entries {
			proposal.Catalog.Entries[i].Summary = "description-" + modelLabel
			proposal.Catalog.Entries[i].Aliases = []string{"generated-alias-" + modelLabel}
			proposal.Catalog.Entries[i].Boundaries = []string{"generated-boundary-" + modelLabel}
		}
		return proposal
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstInput := <-p.seen
	if firstInput.Catalog.Entries[0].Summary != "Declared project-00" || firstInput.Catalog.Entries[0].Aliases[0] != "original-alias-project-00" || firstInput.Catalog.Entries[0].Boundaries[0] != "original-boundary-project-00" {
		t.Fatal("first inference did not receive client-authored description context")
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("model-authored context caused an inference loop: %d", p.calls.Load())
	}
	if err := os.RemoveAll(filepath.Join(w.catalog.proposalWorker.dir, "proposals")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(w.catalog.proposalWorker.dir, "proposals"), 0700); err != nil {
		t.Fatal(err)
	}
	modelLabel = "second"
	profiles := cloneTestProfiles(w.cfg.Profiles)
	profiles["local"] = structProfileModel(profiles["local"], "operator/changed-model")
	w.cfg.Profiles, w.catalog.cfg.Profiles, w.catalog.proposalWorker.cfg.Profiles = profiles, profiles, profiles
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 2 {
		t.Fatalf("profile change after publication did not trigger inference: %d", p.calls.Load())
	}
	secondInput := <-p.seen
	for _, card := range secondInput.Catalog.Entries {
		if card.Summary != "Declared "+card.Tag || len(card.Aliases) != 1 || card.Aliases[0] != "original-alias-"+card.Tag || len(card.Boundaries) != 1 || card.Boundaries[0] != "original-boundary-"+card.Tag {
			t.Fatalf("archived proposal caused generated context feedback: %#v", card)
		}
	}
	active, activeBase, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	active.Entries[0].DescriptionSource = "client_declared"
	active.Entries[0].Summary = "Client revised project-00"
	active.Entries[0].Aliases = []string{"client revised alias"}
	active.Entries[0].Boundaries = []string{"new client boundary"}
	active.ParentHash = activeBase
	if _, err := contextcatalog.Publish(cfg.CatalogDir, active, activeBase); err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 3 {
		t.Fatalf("client boundary change did not invalidate inference: %d", p.calls.Load())
	}
}

func TestCatalogOnlyFailureSuccessFailureResetsBackoff(t *testing.T) {
	w, p, _, _ := setupCatalogOnly(t, 1, 1, "local")
	p.err = errors.New("synthetic provider failure")
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("expected initial provider failure")
	}
	state, err := w.catalog.load()
	if err != nil || state.FailureStreak != 1 || state.NextAllowedAt == "" {
		t.Fatalf("initial failure did not set backoff: state=%#v err=%v", state, err)
	}
	state.NextAllowedAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if err := w.catalog.save(state); err != nil {
		t.Fatal(err)
	}
	p.err = nil
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = w.catalog.load()
	if err != nil || state.FailureStreak != 0 || state.NextAllowedAt != "" {
		t.Fatalf("successful tick did not reset backoff: state=%#v err=%v", state, err)
	}
	state.Fingerprints = map[string]string{}
	if err := w.catalog.save(state); err != nil {
		t.Fatal(err)
	}
	profiles := cloneTestProfiles(w.cfg.Profiles)
	profiles["local"] = structProfileModel(profiles["local"], "operator/recovery-model")
	w.cfg.Profiles, w.catalog.cfg.Profiles, w.catalog.proposalWorker.cfg.Profiles = profiles, profiles, profiles
	p.err = errors.New("synthetic provider failure")
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("expected failure after recovery")
	}
	state, err = w.catalog.load()
	if err != nil || state.FailureStreak != 1 {
		t.Fatalf("post-recovery failure did not restart streak: state=%#v err=%v", state, err)
	}
}

func TestFiniteCatalogReasonUsesSpecificFailureCodes(t *testing.T) {
	cases := map[string]string{
		"cannot read catalog maintenance state":                                                    "state_unavailable",
		"proposal retention limit reached; archive reviewed proposals before resuming maintenance": "proposal_capacity",
		"private catalog source projections are invalid":                                           "state_unavailable",
		"active catalog unavailable":                                                               "catalog_unavailable",
		"proposal validation failed":                                                               "provider_or_validation_failure",
	}
	for message, expected := range cases {
		if actual := finiteCatalogReason(errors.New(message)); actual != expected {
			t.Errorf("finiteCatalogReason(%q) = %q, want %q", message, actual, expected)
		}
	}
}

func TestCatalogProjectionCacheRejectsCorruptionAndGeneratedOriginals(t *testing.T) {
	w, _, _, _ := setupCatalogOnly(t, 1, 1, "local")
	active, _, err := contextcatalog.LoadActive(w.cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	original := active.Entries[0]
	hash, err := catalogEntryHash(original)
	if err != nil {
		t.Fatal(err)
	}
	cache := catalogProjectionCache{SchemaVersion: 1, Entries: []catalogProjectionRecord{{Namespace: original.Namespace, Tag: original.Tag, Original: original, LiveHashes: []string{hash}}}}
	if err := w.catalog.saveProjectionCache(cache); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(w.catalog.projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	var wire catalogProjectionCache
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	wire.Entries[0].Original.Boundaries = []string{"tampered"}
	corrupt, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.catalog.projectionPath, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.catalog.loadProjectionCache(); err == nil {
		t.Fatal("projection cache accepted checksum corruption")
	}
	wire.Entries[0].Original = original
	wire.Entries[0].Original.DescriptionSource = "model_derived"
	if err := w.catalog.saveProjectionCache(wire); err != nil {
		t.Fatal(err)
	}
	if _, err := w.catalog.loadProjectionCache(); err == nil {
		t.Fatal("projection cache accepted model-derived fields as original context")
	}
}

func TestCatalogOnlyCanceledPublicationKeepsReceiptPending(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 1, 1, "local")
	w.cfg.Maintenance.CatalogPublish, w.catalog.cfg.Maintenance.CatalogPublish, cfg.Maintenance.CatalogPublish = "evidence_bounded", "evidence_bounded", "evidence_bounded"
	p.result = catalogOnlyDescriptionProposal
	ctx, cancel := context.WithCancel(context.Background())
	p.after = func(Input) { p.after = nil; cancel() }
	if err := w.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation to leave publication pending, got %v", err)
	}
	state, err := w.catalog.load()
	if err != nil {
		t.Fatal(err)
	}
	state.NextAllowedAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if err := w.catalog.save(state); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWorker(cfg, nil, p, w.budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	active, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil || active.Entries[0].DescriptionSource != "model_derived" || p.calls.Load() != 1 {
		t.Fatalf("saved proposal was not safely published after cancellation: active=%#v calls=%d err=%v", active, p.calls.Load(), err)
	}
}

func TestCatalogOnlyUnavailableCatalogKeepsReceiptPending(t *testing.T) {
	w, p, cfg, root := setupCatalogOnly(t, 1, 1, "local")
	w.cfg.Maintenance.CatalogPublish, w.catalog.cfg.Maintenance.CatalogPublish, cfg.Maintenance.CatalogPublish = "evidence_bounded", "evidence_bounded", "evidence_bounded"
	p.result = catalogOnlyDescriptionProposal
	moved := filepath.Join(root, "catalog-temporary")
	p.after = func(Input) {
		p.after = nil
		_ = os.Rename(cfg.CatalogDir, moved)
	}
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("expected active catalog read failure")
	}
	if err := os.Rename(moved, cfg.CatalogDir); err != nil {
		t.Fatal(err)
	}
	state, err := w.catalog.load()
	if err != nil {
		t.Fatal(err)
	}
	state.NextAllowedAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if err := w.catalog.save(state); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWorker(cfg, nil, p, w.budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	active, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil || active.Entries[0].DescriptionSource != "model_derived" || p.calls.Load() != 1 {
		t.Fatalf("saved proposal was not reused after catalog recovery: active=%#v calls=%d err=%v", active, p.calls.Load(), err)
	}
}

func TestRegisteredReviewCatalogUnavailableRetainsReceiptWithoutCompletion(t *testing.T) {
	w, provider, cfg := newRegisteredCatalogWorker(t, 1)
	created := ensureRegisteredFixture(t, cfg.CatalogDir, "owner-a", "repo:project-a", "project-a")
	if !contextcatalog.RegisteredIdentity(created) {
		t.Fatal("fresh registered fixture did not pass admission")
	}
	moved := cfg.CatalogDir + ".temporary"
	provider.after = func(Input) {
		provider.after = nil
		if err := os.Rename(cfg.CatalogDir, moved); err != nil {
			t.Errorf("hide active catalog: %v", err)
		}
	}
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("catalog disappearance after inference was accepted as completed")
	}
	_ = receiveCatalogInput(t, provider.seen)
	if provider.calls.Load() != 1 || proposalCount(t, filepath.Join(cfg.StateDir, "maintenance", "proposals")) != 1 {
		t.Fatal("completed inference receipt was not retained during temporary catalog unavailability")
	}
	state, err := w.catalog.load()
	if err != nil || state.LastCompleted != 0 || state.LastOutcome != "failed" {
		t.Fatalf("unavailable catalog was acknowledged as completed: state=%#v err=%v", state, err)
	}
	if err := os.Rename(moved, cfg.CatalogDir); err != nil {
		t.Fatal(err)
	}
	state.NextAllowedAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if err := w.catalog.save(state); err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = w.catalog.load()
	if err != nil || state.LastCompleted != 1 || state.LastOutcome != "proposed" || provider.calls.Load() != 1 {
		t.Fatalf("same worker failed to reuse receipt after source recovery: state=%#v calls=%d err=%v", state, provider.calls.Load(), err)
	}
}

func TestCatalogOnlyPostPublicationStateFailureReusesInferenceWithoutRebase(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 1, 1, "local")
	w.cfg.Maintenance.CatalogPublish, w.catalog.cfg.Maintenance.CatalogPublish = "evidence_bounded", "evidence_bounded"
	p.result = catalogOnlyDescriptionProposal
	statePath := filepath.Join(cfg.StateDir, "maintenance-catalog", "state.json")
	p.after = func(Input) { p.after = nil; _ = os.Remove(statePath); _ = os.Mkdir(statePath, 0700) }
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("expected final state write failure")
	}
	active, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	if active.Entries[0].DescriptionSource != "model_derived" {
		t.Fatal("fixture did not publish")
	}
	if err := os.RemoveAll(statePath); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWorker(w.cfg, nil, p, w.budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("published receipt repeated inference: %d", p.calls.Load())
	}
}

func TestCatalogOnlyMixedPublicationReceiptRecoversExactUnchangedCard(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 2, 2, "local")
	w.cfg.Maintenance.CatalogPublish, w.catalog.cfg.Maintenance.CatalogPublish = "evidence_bounded", "evidence_bounded"
	p.result = func(in Input) Proposal {
		proposal := catalogOnlyDescriptionProposal(in)
		for i, selected := range in.Catalog.Entries {
			if selected.Tag != "project-01" {
				continue
			}
			candidate := &proposal.Catalog.Entries[i]
			candidate.Summary = selected.Summary
			candidate.Aliases = cloneStrings(selected.Aliases)
			candidate.OwnedComponents = cloneStrings(selected.OwnedComponents)
			candidate.Boundaries = cloneStrings(selected.Boundaries)
			candidate.Uses = cloneStrings(selected.Uses)
			candidate.SharedWith = cloneStrings(selected.SharedWith)
			candidate.PositiveExamples = cloneStrings(selected.PositiveExamples)
			candidate.NegativeExamples = cloneStrings(selected.NegativeExamples)
			candidate.EvidenceRefs = []string{}
		}
		return proposal
	}
	statePath := filepath.Join(cfg.StateDir, "maintenance-catalog", "state.json")
	p.after = func(Input) { p.after = nil; _ = os.Remove(statePath); _ = os.Mkdir(statePath, 0700) }
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("expected final state write failure")
	}
	active, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	if active.Entries[0].DescriptionSource != "model_derived" {
		t.Fatal("changed selected card was not published")
	}
	if active.Entries[1].DescriptionSource != "client_declared" {
		t.Fatal("unchanged selected card lost client provenance")
	}
	if err := os.RemoveAll(statePath); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWorker(w.cfg, nil, p, w.budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("mixed-batch published receipt did not recover without inference: %d", p.calls.Load())
	}
}

func TestCatalogOnlyDispatchStateIsDurableBeforeProviderCall(t *testing.T) {
	w, p, _, _ := setupCatalogOnly(t, 1, 1, "local")
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	p.after = func(Input) { close(started); <-release }
	go func() { done <- w.RunOnce(context.Background()) }()
	<-started
	status, err := ReadCatalogStatus(w.cfg)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if status.LastOutcome != "dispatching" || status.LastDispatchAt == "" || status.Queued != 1 || status.Completed != 0 {
		close(release)
		t.Fatalf("dispatch state was not visible while provider was blocked: %#v", status)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCatalogOnlyStateSaveFailureDoesNotDispatchOrLeakReservation(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 1, 1, "local")
	statePath := filepath.Join(cfg.StateDir, "maintenance-catalog", "state.json")
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("expected state save failure")
	}
	if p.calls.Load() != 0 {
		t.Fatal("provider dispatched despite failed dispatch-state persistence")
	}
	reservation, err := w.budget.Begin(time.Now())
	if err != nil {
		t.Fatalf("reservation leaked after pre-dispatch state failure: %v", err)
	}
	reservation.Finish(0, true)
}

func TestCatalogOnlyReceiptFilenameChecksumIsEnforced(t *testing.T) {
	w, p, _, _ := setupCatalogOnly(t, 1, 1, "local")
	p.result = catalogOnlyDescriptionProposal
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	in := <-p.seen
	state, err := w.catalog.load()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(w.dir, "proposals", state.LatestProposal)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var artifact privateProposal
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	artifact.Proposal.Catalog.Entries[0].Summary += " tampered"
	corrupt, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, found := w.catalog.findReceipt(w.catalog.receiptKey(in), in, in.Catalog, in.baseCatalogHash)
	if found {
		t.Fatal("modified receipt accepted under its old checksum filename")
	}
}

func TestCatalogOnlyUnresolvedProposalCompletesWithoutPublicationOrRepeat(t *testing.T) {
	w, p, _, _ := setupCatalogOnly(t, 1, 1, "local")
	w.cfg.Maintenance.CatalogPublish, w.catalog.cfg.Maintenance.CatalogPublish = "evidence_bounded", "evidence_bounded"
	p.result = func(in Input) Proposal {
		proposal := catalogOnlyDescriptionProposal(in)
		proposal.Catalog.Entries[0].ReviewStatus = "unresolved"
		proposal.MissingEvidence = []string{"insufficient_evidence"}
		return proposal
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("valid unresolved proposal rejected: %v", err)
	}
	status, err := ReadCatalogStatus(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if status.Completed != 1 || status.LastOutcome != "proposed" || status.ReasonCode != "insufficient_evidence" {
		t.Fatalf("unresolved completion not recorded: %#v", status)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("unresolved proposal was repeated: %d", p.calls.Load())
	}
}

func TestReadCatalogStatusRejectsUnsupportedScopeWithoutEcho(t *testing.T) {
	cfg := aipolicy.Off()
	cfg.Maintenance.Scope = "/private/status-marker"
	_, err := ReadCatalogStatus(cfg)
	if err == nil || strings.Contains(err.Error(), "status-marker") {
		t.Fatalf("unsupported scope was not rejected safely: %v", err)
	}
}

func TestCatalogOnlyHostedProtocolsUseSelectedModelsAndAcceptResponses(t *testing.T) {
	for _, tc := range []struct{ protocol, endpoint string }{
		{"openai-responses", "https://provider.invalid/v1/responses"},
		{"openai-compatible-chat", "https://provider.invalid/v1/chat/completions"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			w, _, cfg, _ := setupCatalogOnly(t, 1, 1, "local")
			cat, base, err := contextcatalog.LoadActive(cfg.CatalogDir)
			if err != nil {
				t.Fatal(err)
			}
			input := catalogOnlyInput(cat.Entries, cat, cat.Entries, cfg, base)
			proposalBytes, err := json.Marshal(catalogOnlyDescriptionProposal(input))
			if err != nil {
				t.Fatal(err)
			}
			model, returned := "operator-selected-model", "provider-returned-alias"
			keyFile := filepath.Join(t.TempDir(), "provider-key")
			if err := os.WriteFile(keyFile, []byte("synthetic-test-key"), 0600); err != nil {
				t.Fatal(err)
			}
			profile := aipolicy.Profile{Protocol: tc.protocol, Model: model, ResponseModels: []string{returned}, Endpoint: tc.endpoint, KeyFile: keyFile, TimeoutMS: 1000}
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				var request map[string]any
				if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request["model"] != model {
					t.Fatalf("request used model %v; want %s", request["model"], model)
				}
				var body any
				if tc.protocol == "openai-responses" {
					body = map[string]any{"model": returned, "status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": string(proposalBytes)}}}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 3}}
				} else {
					body = map[string]any{"model": returned, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(proposalBytes)}}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 3}}
				}
				raw, _ := json.Marshal(body)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
			})
			provider, err := NewProvider(profile, "synthetic-test-key", transport)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Profiles["local"] = profile
			w.cfg, w.catalog.cfg, w.catalog.proposalWorker.cfg = cfg, cfg, cfg
			w.provider, w.catalog.provider = provider, provider
			if err := w.RunOnce(context.Background()); err != nil {
				t.Fatalf("custom provider protocol response rejected: %v", err)
			}
			status, err := ReadCatalogStatus(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if status.Model != model || status.Completed != 1 {
				t.Fatalf("selected model/result not accepted: %#v", status)
			}
		})
	}
}

func TestCatalogOnlyConcurrentRegistrationBlocksStalePublication(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 1, 1, "local")
	w.cfg.Maintenance.CatalogPublish, w.catalog.cfg.Maintenance.CatalogPublish = "evidence_bounded", "evidence_bounded"
	p.result = catalogOnlyDescriptionProposal
	p.after = func(Input) {
		p.after = nil
		current, base, err := contextcatalog.LoadActive(cfg.CatalogDir)
		if err != nil {
			t.Errorf("load catalog in simulated registration: %v", err)
			return
		}
		current.ParentHash = base
		current.Entries = append(current.Entries, catalogFixture(2).Entries[1])
		if _, err := contextcatalog.Publish(cfg.CatalogDir, current, base); err != nil {
			t.Errorf("simulated concurrent registration: %v", err)
		}
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	active, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	if active.Entries[0].Summary == "Proposed description for project-00" || len(active.Entries) != 2 {
		t.Fatal("stale proposal published over concurrent registration")
	}
	status, err := ReadCatalogStatus(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastOutcome != "conflict_or_unpublished" || status.ReasonCode != "base_or_dependency_changed" {
		t.Fatalf("stale base outcome missing: %#v", status)
	}
}

func TestCatalogOnlyValidationRejectsIdentityCrossCardCitationAndGrouping(t *testing.T) {
	w, p, _, _ := setupCatalogOnly(t, 2, 2, "local")
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	in := <-p.seen
	proposal := catalogOnlyDescriptionProposal(in)
	if err := validateProposal(in, proposal); err != nil {
		t.Fatal(err)
	}
	identity := *proposal.Catalog
	identity.Entries = append([]contextcatalog.Entry{}, identity.Entries...)
	identity.Entries[0].ProjectID = strings.Repeat("a", 64)
	proposal.Catalog = &identity
	if validateProposal(in, proposal) == nil {
		t.Fatal("model-supplied registry identity accepted")
	}
	proposal = catalogOnlyDescriptionProposal(in)
	proposal.Catalog.Entries[0].EvidenceRefs = []string{"c002"}
	if validateProposal(in, proposal) == nil {
		t.Fatal("cross-card evidence citation accepted")
	}
	proposal = catalogOnlyDescriptionProposal(in)
	proposal.Grouping = []GroupingProposal{{Alias: "f000", RelatedTags: []string{}, EvidenceAliases: []string{"f000"}}}
	if validateProposal(in, proposal) == nil {
		t.Fatal("catalog-only grouping proposal accepted")
	}
}

type blockingCatalogProvider struct {
	started chan struct{}
	calls   atomic.Int32
}

func (p *blockingCatalogProvider) Propose(ctx context.Context, _ Input) (Proposal, aijudgment.Usage, error) {
	p.calls.Add(1)
	p.started <- struct{}{}
	<-ctx.Done()
	return Proposal{}, aijudgment.Usage{}, ctx.Err()
}

func TestCatalogOnlySchedulerWaitsForFirstTickPreventsOverlapAndStops(t *testing.T) {
	w, _, _, _ := setupCatalogOnly(t, 1, 1, "local")
	provider := &blockingCatalogProvider{started: make(chan struct{}, 2)}
	w.provider = provider
	w.catalog.provider = provider
	w.interval = 25 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if provider.calls.Load() != 0 {
		t.Fatal("catalog-only scheduler dispatched before its first interval")
	}
	select {
	case <-provider.started:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("first scheduled tick did not dispatch")
	}
	time.Sleep(75 * time.Millisecond)
	if provider.calls.Load() != 1 {
		t.Fatalf("overlapping scheduled runs dispatched: %d", provider.calls.Load())
	}
	cancel()
	if err := w.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.Stop()
	if provider.calls.Load() != 1 {
		t.Fatalf("unexpected dispatch after shutdown: %d", provider.calls.Load())
	}
}

func setupCatalogOnly(t *testing.T, n, max int, profileName string) (*Worker, *fakeProvider, aipolicy.Config, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalogDir := filepath.Join(root, "catalog")
	snap := catalogFixture(n)
	if _, err := contextcatalog.Publish(catalogDir, snap, ""); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	cfg := workerConfig(t, catalogDir, stateDir)
	tags := make([]string, 0, n)
	for _, e := range snap.Entries {
		tags = append(tags, e.Tag)
	}
	cfg.Egress.AllowedProjectTags = tags
	cfg.Maintenance.Scope = "catalog_only"
	cfg.Maintenance.MaxProjectsPerRun = max
	cfg.Maintenance.Profile = profileName
	cfg.Profiles[profileName] = cfg.Profiles["local"]
	budget, err := aipolicy.NewBudget(filepath.Join(stateDir, "budget"), cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{seen: make(chan Input, 10), result: func(in Input) Proposal {
		return Proposal{SchemaVersion: 1, BaseCatalogHash: in.baseCatalogHash, Catalog: nil, Grouping: []GroupingProposal{}, MissingEvidence: []string{}}
	}}
	w, err := NewWorker(cfg, nil, provider, budget)
	if err != nil {
		t.Fatal(err)
	}
	return w, provider, cfg, root
}

func TestRegisteredCatalogAdmissionFollowsActiveRegistrationWithoutRestart(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalogDir := filepath.Join(root, "catalog")
	stateDir := filepath.Join(root, "state")
	cfg := workerConfig(t, catalogDir, stateDir)
	cfg.Maintenance.Scope = "catalog_only"
	cfg.Maintenance.ProjectPolicy = "registered"
	cfg.Maintenance.CatalogPublish = "review"
	cfg.Maintenance.MaxProjectsPerRun = 1
	cfg.Egress = aipolicy.Egress{AllowedNamespaces: []string{"projects"}, AllowedProjectTags: []string{"manual-static", "invalid-static", "project-c", "draft-static", "unresolved-static"}, DeniedProjectTags: []string{"project-c"}}
	budget, err := aipolicy.NewBudget(filepath.Join(stateDir, "budget"), cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{seen: make(chan Input, 4), result: func(in Input) Proposal {
		return Proposal{SchemaVersion: 1, BaseCatalogHash: in.baseCatalogHash, Grouping: []GroupingProposal{}, MissingEvidence: []string{}}
	}}
	w, err := NewWorker(cfg, nil, provider, budget)
	if err != nil {
		t.Fatal(err)
	}
	registration := func(key, tag, summary string) contextcatalog.RegistrationInput {
		return contextcatalog.RegistrationInput{ProjectKey: key, Namespace: "projects", Name: tag, Tag: tag, Summary: summary, Evidence: []contextcatalog.Evidence{{Kind: "client_readme", Text: "Original source for " + tag}}}
	}
	inputA := registration("repo:project-a", "project-a", "Original summary A.")
	inputA.Evidence = nil // The original declared summary alone is a valid source.
	createdA, err := contextcatalog.EnsureProject(catalogDir, "owner-a", inputA)
	if err != nil || createdA.Status != "created" {
		t.Fatalf("register A: %#v %v", createdA, err)
	}
	beforeA, _, err := contextcatalog.LoadActive(catalogDir)
	if err != nil {
		t.Fatal(err)
	}
	registeredA := beforeA.Entries[0]
	if !contextcatalog.RegisteredIdentity(registeredA) {
		t.Fatal("EnsureProject result did not satisfy registered identity validation")
	}
	derivedWithOriginal := registeredA
	derivedWithOriginal.DescriptionSource = "model_derived"
	derivedWithOriginal.DeclaredSummary = registeredA.Summary
	derivedWithOriginal.Summary = "Generated summary"
	if !contextcatalog.RegisteredIdentity(derivedWithOriginal) {
		t.Fatal("model-derived card with retained original client declaration was rejected")
	}
	derivedWithoutOriginal := derivedWithOriginal
	derivedWithoutOriginal.DeclaredSummary = ""
	if contextcatalog.RegisteredIdentity(derivedWithoutOriginal) {
		t.Fatal("model-derived card without retained original declaration was admitted")
	}
	for name, mutate := range map[string]func(*contextcatalog.Entry){
		"owner":       func(entry *contextcatalog.Entry) { entry.Owner = "different-owner" },
		"project key": func(entry *contextcatalog.Entry) { entry.ProjectKey = "repo:other-project" },
		"digest":      func(entry *contextcatalog.Entry) { entry.ProjectID = strings.Repeat("0", 64) },
		"namespace":   func(entry *contextcatalog.Entry) { entry.Namespace = "personal" },
	} {
		candidate := registeredA
		mutate(&candidate)
		if contextcatalog.RegisteredIdentity(candidate) {
			t.Errorf("registered identity accepted malformed %s binding", name)
		}
	}
	deniedNamespace := w.cfg
	deniedNamespace.Egress.AllowedNamespaces = nil
	if maintenanceAdmits(registeredA, deniedNamespace, registeredTagsForCards(beforeA.Entries, deniedNamespace)) {
		t.Fatal("registered identity bypassed namespace denial")
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := receiveCatalogInput(t, provider.seen)
	if len(first.Catalog.Entries) != 1 || first.Catalog.Entries[0].Tag != "project-a" || len(first.Facts) != 0 {
		t.Fatalf("fresh registered A was not processed catalog-only: %#v", first)
	}
	if !catalogHasEvidence(first, "Original summary A.") {
		t.Fatal("summary-only registration was not retained as original declaration evidence")
	}
	wire, err := BuildRequest(cfg.Profiles["local"], first)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"owner-a", createdA.ProjectID, "repo:project-a"} {
		if strings.Contains(string(wire), private) {
			t.Fatalf("private registration identity reached provider: %q", private)
		}
	}
	activeAfterA, hashAfterA, err := contextcatalog.LoadActive(catalogDir)
	if err != nil || len(activeAfterA.Entries) != len(beforeA.Entries) {
		t.Fatalf("catalog-only review changed catalog: %v", err)
	}
	initialHash, err := contextcatalog.Hash(beforeA)
	if err != nil || hashAfterA != initialHash {
		t.Fatal("catalog-only review published a catalog change")
	}

	createdB, err := contextcatalog.EnsureProject(catalogDir, "owner-a", registration("repo:project-b", "project-b", "Original summary B."))
	if err != nil || createdB.Status != "created" {
		t.Fatalf("register B: %#v %v", createdB, err)
	}
	update := registration("repo:project-b", "project-b", "Proposed but not active summary B.")
	proposed, err := contextcatalog.EnsureProject(catalogDir, "owner-a", update)
	if err != nil || proposed.Status != "proposed_update" {
		t.Fatalf("changed declaration should remain proposal-only: %#v %v", proposed, err)
	}
	createdC, err := contextcatalog.EnsureProject(catalogDir, "owner-a", registration("repo:project-c", "project-c", "Revoked summary C."))
	if err != nil || createdC.Status != "created" {
		t.Fatalf("register C: %#v %v", createdC, err)
	}

	active, _, err := contextcatalog.LoadActive(catalogDir)
	if err != nil {
		t.Fatal(err)
	}
	manual := contextcatalog.Entry{Namespace: "projects", Tag: "manual-static", Name: "Manual card", Summary: "Manual summary.", ReviewStatus: "declared", DescriptionSource: "manual"}
	draft := contextcatalog.Entry{Namespace: "projects", Tag: "draft-static", Name: "Draft card", Summary: "Draft summary.", ReviewStatus: "draft"}
	unresolved := contextcatalog.Entry{Namespace: "projects", Tag: "unresolved-static", Name: "Unresolved card", Summary: "Unresolved summary.", ReviewStatus: "unresolved"}
	invalid := contextcatalog.Entry{Namespace: "projects", Tag: "invalid-static", Name: "Invalid registered identity", Summary: "Invalid summary.", ReviewStatus: "declared", ProjectID: strings.Repeat("a", 64), ProjectKey: "repo:invalid", Owner: "owner-a", DescriptionSource: "client_declared", DeclaredSummary: "Invalid summary.", ClientEvidence: []contextcatalog.Evidence{{Kind: "client_readme", Text: "Not a valid digest binding."}}}
	admissionProbe := active
	admissionProbe.Entries = append(append([]contextcatalog.Entry(nil), active.Entries...), manual, draft, unresolved, invalid)
	w.cfg.Egress.DeniedProjectTags = []string{"project-c"}
	w.catalog.cfg.Egress.DeniedProjectTags = []string{"project-c"}
	eligible := admittedCatalogEntries(admissionProbe, w.cfg)
	got := make([]string, 0, len(eligible))
	for _, entry := range eligible {
		got = append(got, entry.Tag)
	}
	if strings.Join(got, ",") != "project-a,project-b" {
		t.Fatalf("registered admission ignored identity/review/deny boundaries: %v", got)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := receiveCatalogInput(t, provider.seen)
	if len(second.Catalog.Entries) != 1 || second.Catalog.Entries[0].Tag != "project-b" {
		t.Fatalf("same worker did not discover newly registered B: %#v", second.Catalog.Entries)
	}
	if !catalogHasEvidence(second, "Original summary B.") || catalogHasEvidence(second, "Proposed but not active summary B.") {
		t.Fatal("proposed update replaced the active original declaration")
	}
	if len(second.Facts) != 0 || len(second.CatalogEvidence) == 0 {
		t.Fatal("registered catalog-only request read facts or omitted original evidence")
	}
	activeAfterB, hashAfterB, err := contextcatalog.LoadActive(catalogDir)
	if err != nil {
		t.Fatal(err)
	}
	if hashAfterB == "" || !containsTag(activeAfterB.Entries, "project-a") || !containsTag(activeAfterB.Entries, "project-b") {
		t.Fatal("review-only processing unexpectedly changed registration state")
	}

	createdD, err := contextcatalog.EnsureProject(catalogDir, "owner-a", registration("repo:project-d", "project-d", "Original summary D."))
	if err != nil || createdD.Status != "created" {
		t.Fatalf("register D: %#v %v", createdD, err)
	}
	beforeStale := proposalCount(t, filepath.Join(stateDir, "maintenance", "proposals"))
	provider.after = func(Input) {
		provider.after = nil
		current, currentHash, loadErr := contextcatalog.LoadActive(catalogDir)
		if loadErr != nil {
			t.Errorf("load catalog during callback: %v", loadErr)
			return
		}
		for i := range current.Entries {
			if current.Entries[i].Tag == "project-d" {
				current.Entries[i].Owner = "revoked-owner"
			}
		}
		current.ParentHash = currentHash
		if _, publishErr := contextcatalog.Publish(catalogDir, current, currentHash); publishErr != nil {
			t.Errorf("revoke D during callback: %v", publishErr)
		}
	}
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("source revocation during inference was accepted")
	}
	_ = receiveCatalogInput(t, provider.seen)
	if afterStale := proposalCount(t, filepath.Join(stateDir, "maintenance", "proposals")); afterStale != beforeStale {
		t.Fatalf("stale inference receipt persisted after revocation: %d -> %d", beforeStale, afterStale)
	}
}

func catalogHasEvidence(input Input, text string) bool {
	for _, evidence := range input.CatalogEvidence {
		if evidence.Text == text {
			return true
		}
	}
	return false
}

func receiveCatalogInput(t *testing.T, inputs <-chan Input) Input {
	t.Helper()
	select {
	case input := <-inputs:
		return input
	case <-time.After(2 * time.Second):
		t.Fatal("catalog maintenance provider was not called")
		return Input{}
	}
}

func containsTag(entries []contextcatalog.Entry, tag string) bool {
	for _, entry := range entries {
		if entry.Tag == tag {
			return true
		}
	}
	return false
}

func proposalCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "proposal-") && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count
}

func newRegisteredCatalogWorker(t *testing.T, max int) (*Worker, *fakeProvider, aipolicy.Config) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := workerConfig(t, filepath.Join(root, "catalog"), filepath.Join(root, "state"))
	cfg.Maintenance.Scope = "catalog_only"
	cfg.Maintenance.ProjectPolicy = "registered"
	cfg.Maintenance.CatalogPublish = "review"
	cfg.Maintenance.MaxProjectsPerRun = max
	cfg.Egress = aipolicy.Egress{AllowedNamespaces: []string{"projects"}, AllowedProjectTags: []string{"project-a", "project-b", "project-z"}}
	budget, err := aipolicy.NewBudget(filepath.Join(cfg.StateDir, "budget"), cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{seen: make(chan Input, 4), result: func(in Input) Proposal {
		return Proposal{SchemaVersion: 1, BaseCatalogHash: in.baseCatalogHash, Grouping: []GroupingProposal{}, MissingEvidence: []string{}}
	}}
	w, err := NewWorker(cfg, nil, provider, budget)
	if err != nil {
		t.Fatal(err)
	}
	return w, provider, cfg
}

func ensureRegisteredFixture(t *testing.T, catalogDir, owner, key, tag string) contextcatalog.Entry {
	t.Helper()
	result, err := contextcatalog.EnsureProject(catalogDir, owner, contextcatalog.RegistrationInput{
		ProjectKey: key, Namespace: "projects", Name: tag, Tag: tag, Summary: "Original summary for " + tag,
		Evidence: []contextcatalog.Evidence{{Kind: "client_readme", Text: "Original evidence for " + tag}},
	})
	if err != nil || result.Status != "created" {
		t.Fatalf("register %s: %#v %v", tag, result, err)
	}
	snapshot, _, err := contextcatalog.LoadActive(catalogDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range snapshot.Entries {
		if entry.Tag == tag {
			return entry
		}
	}
	t.Fatalf("registered entry %s missing", tag)
	return contextcatalog.Entry{}
}

func TestRegisteredAutomaticPublicationSurvivesRestoreWithoutReusingDerivedSource(t *testing.T) {
	w, provider, cfg := newRegisteredCatalogWorker(t, 1)
	provider.result = func(in Input) Proposal {
		return registeredDescriptionProposal(in, "Automatically maintained description")
	}
	w.cfg.Maintenance.CatalogPublish = "evidence_bounded"
	w.catalog.cfg.Maintenance.CatalogPublish = "evidence_bounded"
	original := ensureRegisteredFixture(t, cfg.CatalogDir, "owner-a", "repo:project-a", "project-a")
	_, originalHash, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	afterPublish, publishedHash, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	published := afterPublish.Entries[0]
	if published.Summary != "Automatically maintained description" || published.DescriptionSource != "model_derived" || published.ProjectID != original.ProjectID || published.ProjectKey != original.ProjectKey || published.Owner != original.Owner || published.DeclaredSummary != original.Summary {
		t.Fatalf("automatic publication changed source identity or failed to publish description: %#v", published)
	}
	versions, err := contextcatalog.ListVersions(cfg.CatalogDir, original.ProjectID, "", 100)
	if err != nil || len(versions.Versions) == 0 || versions.Versions[0].Provenance != "maintenance" || versions.Versions[0].RunID == "" || versions.Versions[0].ProposalChecksum == "" || versions.Versions[0].Model == "" || versions.Versions[0].Rules == "" {
		t.Fatalf("automatic publication provenance missing: %#v %v", versions, err)
	}
	currentEntryHash, err := catalogEntryHash(published)
	if err != nil {
		t.Fatal(err)
	}
	originalEntryHash, err := catalogEntryHash(original)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := contextcatalog.Restore(cfg.CatalogDir, contextcatalog.RestoreRequest{Reason: "test description rollback", OperationID: strings.Repeat("d", 64), ProjectID: original.ProjectID, HistoricalHash: originalHash, HistoricalEntryHash: originalEntryHash, ExpectedCurrentHash: publishedHash, ExpectedCurrentEntryHash: currentEntryHash, ExpectedPolicyRevision: 0})
	if err != nil || restored.Status != "restored" {
		t.Fatalf("description-only restore failed: %#v %v", restored, err)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("held project reached provider after restore: calls=%d", provider.calls.Load())
	}
	restoredCatalog, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	restoredEntry := restoredCatalog.Entries[0]
	if restoredEntry.Summary != original.Summary || restoredEntry.DescriptionSource != "model_derived" || restoredEntry.ProjectID != original.ProjectID || restoredEntry.ClientEvidence[0] != original.ClientEvidence[0] {
		t.Fatalf("restore did not preserve stable source identity: %#v", restoredEntry)
	}
	policy, err := contextcatalog.LoadProjectPolicy(cfg.CatalogDir, original.ProjectID)
	if err != nil || !policy.Hold || policy.Revision != 1 {
		t.Fatalf("restore did not leave an explicit publication hold: %#v %v", policy, err)
	}
	restoredEntryHash, err := catalogEntryHash(restoredEntry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcatalog.SetProjectPolicy(cfg.CatalogDir, contextcatalog.ProjectPolicy{ProjectID: original.ProjectID, Hold: false, Reason: "test resume"}, restoredEntryHash, policy.Revision); err != nil {
		t.Fatalf("resume automatic publication: %v", err)
	}
	provider.result = func(in Input) Proposal {
		foundOriginalEvidence := false
		for _, evidence := range in.CatalogEvidence {
			if evidence.Kind == original.ClientEvidence[0].Kind && evidence.Text == original.ClientEvidence[0].Text {
				foundOriginalEvidence = true
				break
			}
		}
		if !foundOriginalEvidence || len(in.Catalog.Entries) != 1 || in.Catalog.Entries[0].Summary != original.Summary {
			t.Fatalf("restored/model-derived text replaced original source evidence: %#v", in)
		}
		return registeredDescriptionProposal(in, "Automatic update after restore")
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("policy revision failed to invalidate prior receipt/fingerprint: calls=%d", provider.calls.Load())
	}
	finalCatalog, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	final := finalCatalog.Entries[0]
	if final.Summary != "Automatic update after restore" || final.DescriptionSource != "model_derived" || final.DeclaredSummary != original.Summary || final.ProjectID != original.ProjectID || final.ProjectKey != original.ProjectKey || final.Owner != original.Owner || final.ClientEvidence[0] != original.ClientEvidence[0] {
		t.Fatalf("successive publication did not preserve original sources: %#v", final)
	}
}

func TestRegisteredHoldOmitsOnlyHeldCardBeforeModelSelection(t *testing.T) {
	w, provider, cfg := newRegisteredCatalogWorker(t, 2)
	provider.result = func(in Input) Proposal { return registeredDescriptionProposal(in, "Maintained description") }
	w.cfg.Maintenance.CatalogPublish = "evidence_bounded"
	w.catalog.cfg.Maintenance.CatalogPublish = "evidence_bounded"
	held := ensureRegisteredFixture(t, cfg.CatalogDir, "owner-a", "repo:project-a", "project-a")
	active := ensureRegisteredFixture(t, cfg.CatalogDir, "owner-b", "repo:project-b", "project-b")
	hash, err := catalogEntryHash(held)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcatalog.SetPublicationHold(cfg.CatalogDir, held.ProjectID, true, "test hold", hash, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("eligible card did not run while another card was held: calls=%d", provider.calls.Load())
	}
	select {
	case input := <-provider.seen:
		if len(input.Catalog.Entries) != 1 || input.Catalog.Entries[0].ProjectID != active.ProjectID {
			t.Fatalf("held card reached model selection or active card omitted: %#v", input.Catalog.Entries)
		}
	default:
		t.Fatal("provider input was not captured")
	}
	updated, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	if catalogEntryByTag(t, updated, "project-a").Summary != held.Summary || catalogEntryByTag(t, updated, "project-b").Summary != "Maintained description" {
		t.Fatalf("hold affected the wrong card or blocked the eligible card: %#v", updated.Entries)
	}
}

func TestRegisteredHistoricalOnlyPolicyBlocksCurrentUsesAtPublish(t *testing.T) {
	w, provider, cfg := newRegisteredCatalogWorker(t, 1)
	consumer := ensureRegisteredFixture(t, cfg.CatalogDir, "owner-a", "repo:project-a", "project-a")
	ensureRegisteredFixture(t, cfg.CatalogDir, "owner-b", "repo:project-b", "project-b")
	entryHash, err := catalogEntryHash(consumer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcatalog.SetProjectPolicy(cfg.CatalogDir, contextcatalog.ProjectPolicy{ProjectID: consumer.ProjectID, HistoricalOnly: true, Reason: "test historical boundary"}, entryHash, 0); err != nil {
		t.Fatal(err)
	}
	provider.result = func(in Input) Proposal {
		proposal := registeredDescriptionProposal(in, "Maintained description")
		proposal.Catalog.Entries[0].Uses = []string{"project-b"}
		return proposal
	}
	w.cfg.Maintenance.CatalogPublish = "evidence_bounded"
	w.catalog.cfg.Maintenance.CatalogPublish = "evidence_bounded"
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("historical-only policy allowed an unqualified current uses relationship")
	}
	drafts, err := os.ReadDir(filepath.Join(cfg.StateDir, "maintenance", "proposals"))
	if err != nil || len(drafts) != 1 {
		t.Fatalf("rejected proposal was not retained as a draft: %d %v", len(drafts), err)
	}
	active, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalogEntryByTag(t, active, "project-a").Uses) != 0 {
		t.Fatalf("historical-only rejection changed current relationships: %#v", catalogEntryByTag(t, active, "project-a"))
	}
}

func catalogEntryByTag(t *testing.T, snapshot contextcatalog.Snapshot, tag string) contextcatalog.Entry {
	t.Helper()
	for _, entry := range snapshot.Entries {
		if entry.Tag == tag {
			return entry
		}
	}
	t.Fatalf("catalog entry %s is missing", tag)
	return contextcatalog.Entry{}
}

func registeredDescriptionProposal(in Input, summary string) Proposal {
	catalog := cloneSnapshot(in.Catalog)
	catalog.ParentHash = in.baseCatalogHash
	catalog.Version = "automatic-description"
	for i := range catalog.Entries {
		entry := &catalog.Entries[i]
		entry.Summary = summary
		entry.ReviewStatus = "draft"
		entry.ProjectID, entry.ProjectKey, entry.Owner = "", "", ""
		entry.DescriptionSource, entry.DeclaredSummary = "", ""
		entry.ClientEvidence = nil
		entry.EvidenceRefs = []string{"c000"}
	}
	return Proposal{SchemaVersion: 1, BaseCatalogHash: in.baseCatalogHash, Catalog: &catalog, Grouping: []GroupingProposal{}, MissingEvidence: []string{}}
}

func TestRegisteredSourceRevalidationSortsAndIgnoresUnselectedBlockedProjection(t *testing.T) {
	w, provider, cfg := newRegisteredCatalogWorker(t, 1)
	ensureRegisteredFixture(t, cfg.CatalogDir, "owner-b", "repo:project-b", "project-b")
	ensureRegisteredFixture(t, cfg.CatalogDir, "owner-a", "repo:project-a", "project-a")
	ensureRegisteredFixture(t, cfg.CatalogDir, "owner-z", "repo:project-z", "project-z")
	current, base, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	orderedByTag := map[string]contextcatalog.Entry{}
	for _, card := range current.Entries {
		orderedByTag[card.Tag] = card
	}
	available, sourcesCurrent := w.catalog.catalogSourcesState([]contextcatalog.Entry{orderedByTag["project-b"], orderedByTag["project-a"]})
	if !available || !sourcesCurrent {
		t.Fatal("selected source validation depended on cursor-rotated catalog ordering")
	}
	for i := range current.Entries {
		if current.Entries[i].Tag == "project-z" {
			current.Entries[i].DeclaredSummary = current.Entries[i].Summary
			current.Entries[i].DescriptionSource = "model_derived"
			current.Entries[i].Summary = "Generated description without a retained projection receipt"
		}
	}
	current.ParentHash = base
	if _, err := contextcatalog.Publish(cfg.CatalogDir, current, base); err != nil {
		t.Fatal(err)
	}
	if !contextcatalog.RegisteredIdentity(current.Entries[2]) {
		t.Fatal("unselected model-derived fixture lacks a genuine retained original")
	}
	restored, blocked, err := w.catalog.restorePublishedOriginals(current, admittedCatalogEntries(current, cfg))
	if err != nil || !blocked["projects/project-z"] || !containsTag(restored, "project-z") {
		t.Fatalf("unselected model-derived card did not exercise blocked projection fallback: blocked=%v restored=%v err=%v", blocked, containsTag(restored, "project-z"), err)
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	input := receiveCatalogInput(t, provider.seen)
	if len(input.Catalog.Entries) != 1 || input.Catalog.Entries[0].Tag != "project-a" {
		t.Fatalf("unexpected first stable batch: %#v", input.Catalog.Entries)
	}
}

func TestRegisteredProjectionRejectsCachedManualOriginal(t *testing.T) {
	w, _, cfg := newRegisteredCatalogWorker(t, 1)
	original := ensureRegisteredFixture(t, cfg.CatalogDir, "owner-a", "repo:project-a", "project-a")
	current, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	live := current.Entries[0]
	live.DescriptionSource = "model_derived"
	live.DeclaredSummary = original.Summary
	live.Summary = "Generated live description"
	hash, err := catalogEntryHash(live)
	if err != nil {
		t.Fatal(err)
	}
	manual := original
	manual.DescriptionSource = "manual"
	if err := w.catalog.saveProjectionCache(catalogProjectionCache{SchemaVersion: 1, Entries: []catalogProjectionRecord{{Namespace: live.Namespace, Tag: live.Tag, Original: manual, LiveHashes: []string{hash}}}}); err != nil {
		t.Fatal(err)
	}
	restored, blocked, err := w.catalog.restorePublishedOriginals(current, []contextcatalog.Entry{live})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 0 || !blocked[cardKey(live)] {
		t.Fatalf("registered projection inherited a manual original: restored=%#v blocked=%v", restored, blocked)
	}
}

func TestRegisteredModeImportsLegacyOriginalReceiptWithoutProjectionCache(t *testing.T) {
	w, _, cfg := newRegisteredCatalogWorker(t, 1)
	original := ensureRegisteredFixture(t, cfg.CatalogDir, "owner-a", "repo:project-a", "project-a")
	originalSnapshot, base, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	current := originalSnapshot
	current.Entries = append([]contextcatalog.Entry(nil), originalSnapshot.Entries...)
	current.Entries[0].DescriptionSource = "model_derived"
	current.Entries[0].DeclaredSummary = original.Summary
	current.Entries[0].Summary = "Published generated description"
	current.ParentHash = base
	if _, err := contextcatalog.Publish(cfg.CatalogDir, current, base); err != nil {
		t.Fatal(err)
	}
	live, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	liveHash, err := catalogEntryHash(live.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if contextcatalog.Validate(originalSnapshot) != nil || !contextcatalog.RegisteredIdentity(original) || !sameCatalogSourceIdentity(original, live.Entries[0]) || !contextcatalog.RegisteredIdentity(live.Entries[0]) {
		t.Fatalf("legacy receipt fixture has invalid source binding: original_valid=%v live_valid=%v source_matches=%v", contextcatalog.RegisteredIdentity(original), contextcatalog.RegisteredIdentity(live.Entries[0]), sameCatalogSourceIdentity(original, live.Entries[0]))
	}
	legacyReceipt := privateProposal{SchemaVersion: 1, CatalogOriginalAll: &originalSnapshot, CatalogPublished: []privateCatalogPublished{{Namespace: original.Namespace, Tag: original.Tag, EntryHash: liveHash}}}
	if _, err := w.catalog.proposalWorker.saveProposal(legacyReceipt); err != nil {
		t.Fatal(err)
	}
	cache, changed, err := w.catalog.importReceiptProjections(live, catalogProjectionCache{SchemaVersion: 1, Entries: []catalogProjectionRecord{}})
	if err != nil || !changed || len(cache.Entries) != 1 {
		t.Fatalf("legacy original receipt was not imported: changed=%v cache=%#v err=%v", changed, cache, err)
	}
	if cache.Entries[0].Original.DescriptionSource != "client_declared" || !contextcatalog.RegisteredIdentity(cache.Entries[0].Original) {
		t.Fatal("legacy receipt recovery lost the validated client original")
	}
}

func TestCatalogOnlyUsesCompleteFairBatchesAndRestartDeduplicates(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 32, 4, "local")
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := <-p.seen
	if len(first.Facts) != 0 || len(first.Catalog.Entries) != 4 || len(first.CatalogEvidence) != 8 {
		t.Fatalf("bad first batch: cards=%d evidence=%d facts=%d", len(first.Catalog.Entries), len(first.CatalogEvidence), len(first.Facts))
	}
	original, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Catalog.CreatedAt != original.CreatedAt || first.Catalog.Version != original.Version {
		t.Fatal("selected input did not preserve source snapshot metadata")
	}
	seen := map[string]bool{}
	for _, e := range first.Catalog.Entries {
		seen[e.Tag] = true
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := <-p.seen
	for _, e := range second.Catalog.Entries {
		if seen[e.Tag] {
			t.Fatalf("cursor repeated %s before covering later cards", e.Tag)
		}
		seen[e.Tag] = true
	}
	if len(seen) != 8 {
		t.Fatalf("expected two fair batches, got %d cards", len(seen))
	}
	// A fresh worker instance shares the private state and must not infer the completed batch again.
	budget, err := aipolicy.NewBudget(filepath.Join(w.cfg.StateDir, "budget-restart"), w.cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWorker(w.cfg, nil, p, budget)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := restarted.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		<-p.seen
	}
	if p.calls.Load() != 8 {
		t.Fatalf("expected one dispatch per changed card batch (8 total cards), calls=%d", p.calls.Load())
	}
}

func TestCatalogOnlyFingerprintTracksOriginalSourcesAndProfileButNotOwnPublication(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 1, 4, "local")
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-p.seen
	entry := catalogFixture(1).Entries[0]
	if catalogCardFingerprint(entry, cfg) != catalogCardFingerprint(entry, cfg) {
		t.Fatal("unstable fingerprint")
	}
	derived := entry
	derived.Summary = "model generated description"
	derived.DescriptionSource = "model_derived"
	derived.DeclaredSummary = entry.Summary
	if catalogCardFingerprint(entry, cfg) != catalogCardFingerprint(derived, cfg) {
		t.Fatal("own publication must not invalidate original-source fingerprint")
	}
	changed := entry
	changed.ClientEvidence = []contextcatalog.Evidence{{Kind: "client_readme", Text: "new original source"}}
	if catalogCardFingerprint(entry, cfg) == catalogCardFingerprint(changed, cfg) {
		t.Fatal("source changes did not invalidate card")
	}
	other := catalogFixture(2).Entries[1]
	contextCfg := cfg
	contextCfg.Egress.AllowedProjectTags = append(append([]string{}, cfg.Egress.AllowedProjectTags...), other.Tag)
	if catalogCardFingerprintWithContext(entry, []contextcatalog.Entry{entry}, contextCfg) == catalogCardFingerprintWithContext(entry, []contextcatalog.Entry{entry, other}, contextCfg) {
		t.Fatal("other-card original context did not invalidate receipt identity")
	}
	other.ClientEvidence = []contextcatalog.Evidence{{Kind: "client_agents", Text: "changed boundary source"}}
	if catalogCardFingerprintWithContext(entry, []contextcatalog.Entry{entry, catalogFixture(2).Entries[1]}, contextCfg) == catalogCardFingerprintWithContext(entry, []contextcatalog.Entry{entry, other}, contextCfg) {
		t.Fatal("other-card source change did not invalidate receipt identity")
	}
	profileCfg := cfg
	profileCfg.Profiles = make(map[string]aipolicy.Profile, len(cfg.Profiles))
	for name, profile := range cfg.Profiles {
		profileCfg.Profiles[name] = profile
	}
	profileCfg.Profiles["local"] = structProfileModel(profileCfg.Profiles["local"], "operator/other-model")
	if catalogCardFingerprint(entry, w.cfg) == catalogCardFingerprint(entry, profileCfg) {
		t.Fatal("profile model changes did not invalidate card")
	}
}

func structProfileModel(p aipolicy.Profile, model string) aipolicy.Profile { p.Model = model; return p }

func TestCatalogStatusIsSafeAndDisabledDoesNotReadState(t *testing.T) {
	status, err := ReadCatalogStatus(aipolicy.Off())
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.Scope != "facts_and_catalog" || status.Model != "" {
		t.Fatalf("unexpected disabled status: %#v", status)
	}
	_, _, cfg, _ := setupCatalogOnly(t, 1, 1, "local")
	status, err = ReadCatalogStatus(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || status.Scope != "catalog_only" || status.Model != "local-model" {
		t.Fatalf("unexpected configured status: %#v", status)
	}

	stateDir := filepath.Join(cfg.StateDir, "maintenance-catalog")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	forged := `{"schema_version":1,"fingerprints":{},"last_outcome":"private marker should not be echoed","last_reason":"private marker reason"}`
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), []byte(forged), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = ReadCatalogStatus(cfg)
	if err == nil || strings.Contains(err.Error(), "private marker") {
		t.Fatalf("forged status should fail with a generic error: %v", err)
	}
	raw, err := jsonMarshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"endpoint", "key_file", "project-00", "fixture-owner", "Original client evidence"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("status leaked %q", forbidden)
		}
	}
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func TestCatalogOnlyProjectionWriteFailurePreservesInferenceReceipt(t *testing.T) {
	w, p, cfg, _ := setupCatalogOnly(t, 1, 1, "local")
	w.cfg.Maintenance.CatalogPublish = "evidence_bounded"
	w.catalog.cfg.Maintenance.CatalogPublish = "evidence_bounded"
	p.result = catalogOnlyDescriptionProposal
	p.after = func(Input) {
		p.after = nil
		if err := os.Mkdir(w.catalog.projectionPath, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("expected projection persistence failure")
	}
	artifacts, err := os.ReadDir(filepath.Join(w.dir, "proposals"))
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("completed inference receipt was not retained: %v", err)
	}
	if err := os.RemoveAll(w.catalog.projectionPath); err != nil {
		t.Fatal(err)
	}
	state, err := w.catalog.load()
	if err != nil {
		t.Fatal(err)
	}
	state.NextAllowedAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if err := w.catalog.save(state); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWorker(w.cfg, nil, p, w.budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("completed inference repeated after projection persistence failure: %d", p.calls.Load())
	}
	active, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	if active.Entries[0].DescriptionSource != "model_derived" {
		t.Fatal("reused receipt did not publish after projection recovery")
	}
}

func TestRegisteredReviewAcceptsUnchangedSourcesAfterUnrelatedCatalogEdit(t *testing.T) {
	for _, kind := range []string{"denied", "manual", "non-project"} {
		t.Run(kind, func(t *testing.T) {
			w, provider, cfg := newRegisteredCatalogWorker(t, 1)
			ensureRegisteredFixture(t, cfg.CatalogDir, "owner-a", "repo:project-a", "project-a")
			if kind == "denied" {
				ensureRegisteredFixture(t, cfg.CatalogDir, "owner-z", "repo:project-z", "project-z")
				w.cfg.Egress.DeniedProjectTags = []string{"project-z"}
				w.catalog.cfg.Egress.DeniedProjectTags = []string{"project-z"}
			} else {
				snapshot, base, err := contextcatalog.LoadActive(cfg.CatalogDir)
				if err != nil {
					t.Fatal(err)
				}
				entry := contextcatalog.Entry{Namespace: "projects", Tag: "project-z", Name: "Unrelated", Summary: "Original unrelated summary.", ReviewStatus: "declared", DescriptionSource: "manual"}
				if kind == "non-project" {
					entry.Namespace = "tech"
				}
				snapshot.Entries = append(snapshot.Entries, entry)
				snapshot.ParentHash = base
				if _, err := contextcatalog.Publish(cfg.CatalogDir, snapshot, base); err != nil {
					t.Fatal(err)
				}
			}
			_, before, err := contextcatalog.LoadActive(cfg.CatalogDir)
			if err != nil {
				t.Fatal(err)
			}
			provider.after = func(Input) {
				snapshot, base, err := contextcatalog.LoadActive(cfg.CatalogDir)
				if err != nil {
					t.Fatal(err)
				}
				for i := range snapshot.Entries {
					if snapshot.Entries[i].Tag == "project-z" {
						snapshot.Entries[i].Summary = "Updated unrelated summary."
					}
				}
				snapshot.ParentHash = base
				if _, err := contextcatalog.Publish(cfg.CatalogDir, snapshot, base); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			in := receiveCatalogInput(t, provider.seen)
			if len(in.Catalog.Entries) != 1 || in.Catalog.Entries[0].Tag != "project-a" {
				t.Fatal("unrelated entry reached provider")
			}
			_, after, err := contextcatalog.LoadActive(cfg.CatalogDir)
			if err != nil || before == after {
				t.Fatal("test did not change catalog generation")
			}
			status, err := ReadCatalogStatus(cfg)
			if err != nil || status.LastOutcome != "proposed" || status.Completed != 1 {
				t.Fatalf("unchanged admitted sources not completed: %#v %v", status, err)
			}
			if proposalCount(t, filepath.Join(cfg.StateDir, "maintenance", "proposals")) != 1 {
				t.Fatal("review receipt missing")
			}
		})
	}
}
