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
	_, _, _, found := w.catalog.findReceipt(w.catalog.receiptKey(in), in, in.Catalog, in.baseCatalogHash)
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
