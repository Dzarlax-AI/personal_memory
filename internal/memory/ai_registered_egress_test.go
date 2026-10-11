package memory

import (
	"context"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
)

func registeredConfig(t *testing.T, write, read string) (*Server, *fakeAIProvider) {
	t.Helper()
	s := newAIServer(t, &aiTestBackend{})
	cfg := aiTestConfig(t, write, read)
	cfg.CatalogDir = filepath.Join(aiPrivateRoot(t), "fresh-registry")
	cfg.Registration.Mode = "client_declared"
	cfg.Egress.ProjectPolicy = "registered"
	cfg.Egress.AllowedProjectTags = nil
	cfg.Write.Admission = "client_opt_in"
	p := &fakeAIProvider{}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, p); err != nil {
		t.Fatal(err)
	}
	return s, p
}

func TestRegisteredEgressFreshCatalogAndOptIn(t *testing.T) {
	s, p := registeredConfig(t, "on", "off")
	p.classify = func(_ context.Context, in aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
		for _, e := range in.Catalog.Entries {
			if e.Owner != "" || e.ProjectKey != "" || e.ProjectID != "" || len(e.ClientEvidence) != 0 || len(e.EvidenceRefs) != 0 || e.DeclaredSummary != "" {
				t.Fatal("private catalog fields left server")
			}
		}
		return aijudgment.ClassifyResult{Status: "decided", PrimaryTag: in.Catalog.Entries[len(in.Catalog.Entries)-1].Tag}, aijudgment.Usage{Known: true, InputTokens: 1}, nil
	}
	a := s.aiState()
	for i, tag := range []string{"alpha", "beta"} {
		out := ensureTestProject(t, s, registrationTestArgs("stable-key-"+tag, tag))
		if out.Status != "created" {
			t.Fatal(out)
		}
		if p.writes.Load() != int64(i) {
			t.Fatal("registration dispatched inference")
		}
		if d := s.aiClassify(context.Background(), "Synthetic project assertion", "projects", nil, "", nil, nil, false); d != nil {
			t.Fatal("opt-in absent")
		}
		if d := s.aiClassify(context.Background(), "Synthetic life fact", "personal", nil, "", nil, nil, true); d != nil {
			t.Fatal("life fact classified as project")
		}
		d := s.aiClassify(context.Background(), "Synthetic project assertion", "projects", nil, "", nil, nil, true)
		if d == nil || d.outcome.Status != "decided" || d.after.Primary != tag || s.aiState() != a {
			t.Fatalf("fresh project not usable without restart: %#v", d)
		}
		cat, _, ok := a.catalog()
		if !ok || len(cat.Entries) != i+1 {
			t.Fatal("new generation missing")
		}
	}
	if p.writes.Load() != 2 {
		t.Fatal("unexpected inference count")
	}
}

func TestRegisteredCatalogExclusionsAndScope(t *testing.T) {
	s, p := registeredConfig(t, "on", "on")
	for _, tag := range []string{"alpha", "beta"} {
		ensureTestProject(t, s, registrationTestArgs("stable-key-"+tag, tag))
	}
	cfg := s.aiState().cfg
	cfg.Egress.DeniedProjectTags = []string{"beta"}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, p); err != nil {
		t.Fatal(err)
	}
	a := s.aiState()
	snap, hash, err := contextcatalog.LoadActive(a.cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	// Eligibility alone does not grant access to identity-free manual cards.
	snap.Entries = append(snap.Entries, contextcatalog.Entry{Namespace: "projects", Tag: "legacy", Name: "Legacy", Summary: "Manual card", ReviewStatus: "approved"})
	// Published snapshots reject draft/unresolved cards before runtime loading.
	snap.ParentHash = hash
	if _, err = contextcatalog.Publish(a.cfg.CatalogDir, snap, hash); err != nil {
		t.Fatal(err)
	}
	if _, err = contextcatalog.EnsureProject(a.cfg.CatalogDir, "foreign", contextcatalog.RegistrationInput{Namespace: "projects", ProjectKey: "foreign-key", Name: "Foreign", Tag: "foreign", Summary: "Foreign card"}); err != nil {
		t.Fatal(err)
	}
	cat, _, ok := a.catalog()
	if !ok || len(cat.Entries) != 1 || cat.Entries[0].Tag != "alpha" {
		t.Fatalf("unexpected projection: %#v", cat)
	}
	if a.allowsCatalogScope(cat, "projects", []string{"beta"}) || a.allowsCatalogScope(cat, "projects", []string{"foreign"}) || a.allowsCatalogScope(cat, "projects", []string{"unknown-topic"}) || a.allowsCatalogScope(cat, "personal", []string{"alpha"}) {
		t.Fatal("automatic scope escaped projected catalog")
	}
	p.classify = func(context.Context, aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
		return aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "legacy"}, aijudgment.Usage{Known: true}, nil
	}
	d := s.aiClassify(context.Background(), "Synthetic assertion", "projects", nil, "", nil, nil, true)
	if d.outcome.Status != "invalid" {
		t.Fatal("excluded target accepted")
	}
	before := p.writes.Load()
	d = s.aiClassify(context.Background(), "Synthetic assertion", "projects", []string{"beta"}, "", nil, nil, true)
	if d.reason != "egress_denied" || p.writes.Load() != before {
		t.Fatal("forbidden fact dispatched")
	}
	d = s.aiClassify(context.Background(), "Synthetic assertion", "projects", nil, "", &FactOrigin{SourceProject: "beta"}, nil, true)
	if d.reason != "egress_denied" || p.writes.Load() != before {
		t.Fatal("forbidden origin dispatched")
	}
	point := aiPoint("1", .9, "", false)
	point.Payload["tags"] = []string{"beta"}
	candidates := presentLifecycleRecallCandidates([]qdrant.Point{point}, LifecycleRecallOptions{}, time.Now())
	_, _, dispatched := a.rank(context.Background(), "Synthetic query", LifecycleRecallOptions{}, candidates)
	if dispatched || p.reads.Load() != 0 {
		t.Fatal("forbidden read candidate dispatched")
	}
	cfg.Egress.AllowedProjectTags = []string{"legacy"}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, p); err != nil {
		t.Fatal(err)
	}
	d = s.aiClassify(context.Background(), "Synthetic assertion", "projects", nil, "", nil, nil, true)
	if d.outcome.Status != "decided" || d.after.Primary != "legacy" {
		t.Fatal("explicit legacy grant unavailable in registered mode")
	}
}

func TestRegisteredDenialsAndExplicitCompatibility(t *testing.T) {
	s, p := registeredConfig(t, "on", "off")
	ensureTestProject(t, s, registrationTestArgs("stable-key-alpha", "alpha"))
	ensureTestProject(t, s, registrationTestArgs("stable-key-beta", "beta"))
	cfg := s.aiState().cfg
	cfg.Egress.DeniedProjectTags = []string{"beta"}
	cfg.Egress.AllowedProjectTags = []string{"beta"}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	cat, _, ok := s.aiState().catalog()
	if !ok || len(cat.Entries) != 1 || cat.Entries[0].Tag != "alpha" {
		t.Fatal("denial did not override both grants")
	}
	cfg.Egress.DeniedProjectTags = []string{"alpha", "beta"}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, ok = s.aiState().catalog(); ok {
		t.Fatal("empty permitted catalog accepted")
	}
	cfg.Egress.ProjectPolicy = "explicit"
	cfg.Egress.DeniedProjectTags = nil
	cfg.Egress.AllowedProjectTags = []string{"alpha"}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	d := s.aiClassify(context.Background(), "Synthetic assertion", "projects", nil, "", nil, nil, true)
	if d.reason != "egress_denied" || p.writes.Load() != 0 {
		t.Fatal("explicit whole-catalog rejection changed")
	}
}

func TestRegisteredWriteRejectsStaleCatalogAndRuntime(t *testing.T) {
	for _, change := range []string{"registration", "remove", "policy-replacement"} {
		t.Run(change, func(t *testing.T) {
			s, p := registeredConfig(t, "on", "off")
			ensureTestProject(t, s, registrationTestArgs("stable-key-alpha", "alpha"))
			p.classify = func(context.Context, aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
				return aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "alpha"}, aijudgment.Usage{Known: true, InputTokens: 1}, nil
			}
			d := s.aiClassify(context.Background(), "Synthetic assertion", "projects", nil, "", nil, nil, true)
			if d.outcome.Status != "decided" {
				t.Fatal(d.outcome)
			}
			switch change {
			case "registration":
				ensureTestProject(t, s, registrationTestArgs("stable-key-beta", "beta"))
			case "remove":
				a := s.aiState()
				snap, hash, err := contextcatalog.LoadActive(a.cfg.CatalogDir)
				if err != nil {
					t.Fatal(err)
				}
				snap.Entries = nil
				snap.ParentHash = hash
				if _, err = contextcatalog.Publish(a.cfg.CatalogDir, snap, hash); err != nil {
					t.Fatal(err)
				}
			case "policy-replacement":
				cfg := s.aiState().cfg
				cfg.Egress.DeniedProjectTags = []string{"alpha"}
				if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
					t.Fatal(err)
				}
			}
			if d.prepare("synthetic-point") || d.outcome.Status != "catalog_changed" || d.after.Primary != "" {
				t.Fatal("stale grouping prepared")
			}
		})
	}
}

func TestRegisteredReadAcceptsNewProjectAndRejectsConcurrentGeneration(t *testing.T) {
	s, p := registeredConfig(t, "off", "on")
	ensureTestProject(t, s, registrationTestArgs("stable-key-alpha", "alpha"))
	point := aiPoint("1", .9, "", false)
	candidates := presentLifecycleRecallCandidates([]qdrant.Point{point}, LifecycleRecallOptions{}, time.Now())
	_, out, dispatched := s.aiState().rank(context.Background(), "Synthetic query", LifecycleRecallOptions{}, candidates)
	if !dispatched || out.Status != "decided" {
		t.Fatal("registered candidate not ranked")
	}
	p.rank = func(context.Context, aijudgment.RankInput) (aijudgment.RankResult, aijudgment.Usage, error) {
		ensureTestProject(t, s, registrationTestArgs("stable-key-beta", "beta"))
		return aijudgment.RankResult{Status: "decided", Scores: map[string]float64{"c1": 1}}, aijudgment.Usage{Known: true, InputTokens: 1}, nil
	}
	_, out, dispatched = s.aiState().rank(context.Background(), "Synthetic query", LifecycleRecallOptions{}, candidates)
	if !dispatched || out.Status != "catalog_changed" || out.Applied {
		t.Fatal("stale read result accepted")
	}
}

func TestRegisteredShadowStoreRetainsBaseline(t *testing.T) {
	s, p := registeredConfig(t, "shadow", "off")
	ensureTestProject(t, s, registrationTestArgs("stable-key-alpha", "alpha"))
	p.classify = func(context.Context, aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
		return aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "alpha"}, aijudgment.Usage{Known: true, InputTokens: 1}, nil
	}
	result := aiStoredResult(t, s, map[string]interface{}{"fact": "Synthetic isolated registered project assertion", "namespace": "projects", "allow_ai_processing": true})
	if !result.Stored || result.AI == nil || result.AI.Applied {
		t.Fatal("shadow store contract changed")
	}
	journal := shadowJournal(t, s.aiState().cfg.StateDir)
	if journal.Status != "decided" || journal.After.Primary != "alpha" || journal.Before.Primary != "" {
		t.Fatal("registered shadow did not propose project")
	}
}

func TestRegisteredReadDeniesOrigin(t *testing.T) {
	s, p := registeredConfig(t, "off", "on")
	ensureTestProject(t, s, registrationTestArgs("stable-key-alpha", "alpha"))
	point := aiPoint("1", .9, "", false)
	point.Payload["origin"] = FactOrigin{SourceProject: "forbidden", SourceKind: "client_declared", RecordedAt: nowISO()}
	candidates := presentLifecycleRecallCandidates([]qdrant.Point{point}, LifecycleRecallOptions{}, time.Now())
	_, _, dispatched := s.aiState().rank(context.Background(), "Synthetic query", LifecycleRecallOptions{}, candidates)
	if dispatched || p.reads.Load() != 0 {
		t.Fatal("forbidden origin leaked through read")
	}
}

func TestRegisteredReadPolicyReplacementDuringRefresh(t *testing.T) {
	s, p := registeredConfig(t, "off", "on")
	ensureTestProject(t, s, registrationTestArgs("stable-key-alpha", "alpha"))
	point := aiPoint("1", .9, "", false)
	backend := &aiTestBackend{points: []qdrant.Point{point}}
	cfg := s.aiState().cfg
	cfg.Egress.DeniedProjectTags = []string{"alpha"}
	var once sync.Once
	qs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			if err := s.ConfigureAIWithProviders(context.Background(), cfg, nil, p); err != nil {
				t.Error(err)
			}
		})
		backend.handle(w, r)
	}))
	defer qs.Close()
	s.qdrant = qdrant.NewClient(qs.URL, "memory")
	candidates := presentLifecycleRecallCandidates([]qdrant.Point{point}, LifecycleRecallOptions{}, time.Now())
	_, out, err := s.aiRerank(context.Background(), "Synthetic query", "projects", nil, LifecycleRecallOptions{}, candidates)
	if err != nil || out == nil || out.Applied || out.Status != "catalog_changed" {
		t.Fatalf("old policy applied: %+v %v", out, err)
	}
}
