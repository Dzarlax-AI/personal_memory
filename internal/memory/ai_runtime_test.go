package memory

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

type fakeAIProvider struct {
	classify      func(context.Context, aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error)
	rank          func(context.Context, aijudgment.RankInput) (aijudgment.RankResult, aijudgment.Usage, error)
	writes, reads atomic.Int64
}

func (f *fakeAIProvider) Classify(ctx context.Context, in aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
	f.writes.Add(1)
	if f.classify != nil {
		return f.classify(ctx, in)
	}
	return aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "alpha", RelatedTags: []string{"beta"}}, aijudgment.Usage{Known: true, InputTokens: 10}, nil
}
func (f *fakeAIProvider) Rank(ctx context.Context, in aijudgment.RankInput) (aijudgment.RankResult, aijudgment.Usage, error) {
	f.reads.Add(1)
	if f.rank != nil {
		return f.rank(ctx, in)
	}
	scores := map[string]float64{}
	for i, c := range in.Candidates {
		scores[c.Alias] = float64(i+1) / float64(len(in.Candidates)+1)
	}
	return aijudgment.RankResult{Status: "decided", Scores: scores}, aijudgment.Usage{Known: true, InputTokens: 10}, nil
}

type aiTestBackend struct {
	mu                      sync.Mutex
	points                  []qdrant.Point
	stored                  *qdrant.Point
	gets, searches, upserts int
	getFails                bool
}

func (b *aiTestBackend) handle(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/points/search"):
		b.searches++
		points := b.points
		if points == nil {
			points = []qdrant.Point{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": points})
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/points/"):
		b.gets++
		if b.getFails {
			w.WriteHeader(503)
			return
		}
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		for _, p := range b.points {
			if p.ID == id {
				_ = json.NewEncoder(w).Encode(map[string]any{"result": p})
				return
			}
		}
		if b.stored != nil && b.stored.ID == id {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": b.stored})
			return
		}
		w.WriteHeader(404)
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/points"):
		var body struct {
			Points []qdrant.Point `json:"points"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Points) != 1 {
			w.WriteHeader(400)
			return
		}
		b.upserts++
		p := body.Points[0]
		b.stored = &p
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"status": "completed"}})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"status": "completed"}})
	}
}
func aiPrivateRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/private/tmp", "ai-memory-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
func aiTestConfig(t *testing.T, write, read string) aipolicy.Config {
	t.Helper()
	dir := aiPrivateRoot(t)
	cfg := aipolicy.Off()
	cfg.CatalogDir = filepath.Join(dir, "catalog")
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.Write = aipolicy.Feature{Mode: write, Provider: "decisions", Profile: "judge"}
	cfg.Read = aipolicy.Feature{Mode: read, Provider: "decisions", Profile: "judge"}
	cfg.Profiles = map[string]aipolicy.Profile{"judge": {Protocol: "decisions", Model: aijudgment.DecisionsModel, Endpoint: aijudgment.DecisionsURL, KeyFile: filepath.Join(dir, "missing-key")}}
	cfg.Egress = aipolicy.Egress{AllowedNamespaces: []string{"projects"}, AllowedProjectTags: []string{"alpha", "beta"}, UnassignedNamespaces: []string{"projects"}}
	cfg.Defaults()
	snap := contextcatalog.Snapshot{SchemaVersion: 1, Version: "test-v1", CreatedAt: "2026-10-07T00:00:00Z", Entries: []contextcatalog.Entry{
		{Namespace: "projects", Tag: "alpha", Name: "Alpha", Summary: "Owns alpha components", EvidenceRefs: []string{"/private/local-evidence.md"}, ReviewStatus: "approved"},
		{Namespace: "projects", Tag: "beta", Name: "Beta", Summary: "Owns beta components", ReviewStatus: "approved"},
	}}
	if _, err := contextcatalog.Publish(cfg.CatalogDir, snap, ""); err != nil {
		t.Fatal(err)
	}
	return cfg
}
func newAIServer(t *testing.T, b *aiTestBackend) *Server {
	t.Helper()
	qs := httptest.NewServer(http.HandlerFunc(b.handle))
	t.Cleanup(qs.Close)
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[[0.1,0.2]]`)) }))
	t.Cleanup(es.Close)
	s := NewServer(qdrant.NewClient(qs.URL, "memory"), embeddings.NewClient(es.URL), NewCache(time.Minute), "synthetic", .97, .60, .90)
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	t.Cleanup(func() {
		cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s
}
func aiPoint(id string, score float64, state lifecycle.State, canonical bool) qdrant.Point {
	payload := map[string]interface{}{"text": "synthetic fact " + id, "namespace": "projects", "tags": []string{"alpha"}, "primary_tag": "alpha", "recall_count": 0}
	if state != "" {
		payload["lifecycle_state"] = string(state)
		if canonical {
			payload["canonical"] = true
		}
		if state == lifecycle.Superseded {
			payload["superseded_by"] = []string{"9"}
		}
	}
	return qdrant.Point{ID: id, Score: score, Payload: payload}
}
func aiStoredResult(t *testing.T, s *Server, args map[string]interface{}) StoreFactResult {
	t.Helper()
	result, err := s.storeFact(context.Background(), toolRequest(args))
	if err != nil || result.IsError {
		t.Fatalf("store: %v %#v", err, result)
	}
	got, ok := result.StructuredContent.(StoreFactResult)
	if !ok {
		t.Fatalf("structured result %T", result.StructuredContent)
	}
	return got
}
func aiRecallResult(t *testing.T, s *Server, args map[string]interface{}) RecallFactsResult {
	t.Helper()
	result, err := s.recallFacts(context.Background(), toolRequest(args))
	if err != nil || result.IsError {
		t.Fatalf("recall: %v %#v", err, result)
	}
	got, ok := result.StructuredContent.(RecallFactsResult)
	if !ok {
		t.Fatalf("structured result %T", result.StructuredContent)
	}
	return got
}

func TestAIOffDoesNotReadKeysOrCreateState(t *testing.T) {
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	cfg := aipolicy.Off()
	cfg.StateDir = filepath.Join(aiPrivateRoot(t), "absent")
	cfg.Profiles = map[string]aipolicy.Profile{"bad": {KeyFile: "/missing"}}
	if err := s.ConfigureAI(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if s.AIBudget() != nil {
		t.Fatal("off created budget")
	}
	if _, err := os.Stat(cfg.StateDir); !os.IsNotExist(err) {
		t.Fatal("off created state")
	}
	result := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic off", "namespace": "projects"})
	if result.AI != nil {
		t.Fatal("baseline acquired AI metadata")
	}
}
func TestAIActiveKeyErrorsAndStorageFailure(t *testing.T) {
	cfg := aiTestConfig(t, "on", "off")
	s := newAIServer(t, &aiTestBackend{})
	if err := s.ConfigureAI(context.Background(), cfg); err == nil {
		t.Fatal("missing active key accepted")
	}
	blocker := filepath.Join(aiPrivateRoot(t), "file")
	if err := os.WriteFile(blocker, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.StateDir = filepath.Join(blocker, "state")
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, &fakeAIProvider{}, nil); err != nil {
		t.Fatal(err)
	}
	if s.AIBudget() != nil {
		t.Fatal("unsafe state retained inference")
	}
}
func TestAIMaintenanceOnlySharesBudgetWithoutJudgmentKeys(t *testing.T) {
	cfg := aiTestConfig(t, "off", "off")
	cfg.Maintenance = aipolicy.Maintenance{Enabled: true, Profile: "maintenance", IntervalSeconds: 60, MaxFactsPerRun: 1, CatalogPublish: "review", GroupingApply: "manual"}
	cfg.Limits.ReservationTokens = 131072
	cfg.Profiles["maintenance"] = aipolicy.Profile{Protocol: "openai-compatible-chat", Model: "explicit", Endpoint: "http://localhost:1234", Local: true, KeyFile: "/missing/maintenance-key"}
	s := newAIServer(t, &aiTestBackend{})
	if err := s.ConfigureAI(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if s.AIBudget() == nil {
		t.Fatal("shared budget missing")
	}
}
func TestAIWriteInferencePreservesMetadataAndPrivateAudit(t *testing.T) {
	cfg := aiTestConfig(t, "on", "off")
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	p := &fakeAIProvider{}
	p.classify = func(ctx context.Context, in aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
		for _, e := range in.Catalog.Entries {
			if len(e.EvidenceRefs) != 0 {
				t.Error("evidence leaked")
			}
		}
		if in.Origin != nil {
			t.Error("recording origin leaked into subject classification")
		}
		return aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "alpha", RelatedTags: []string{"beta"}}, aijudgment.Usage{Known: true, InputTokens: 10}, nil
	}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	result := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic alpha source", "namespace": "projects", "source_project": "beta", "source_kind": "client_declared", "permanent": true, "valid_until": "2099-01-01"})
	if !result.Stored || result.AI == nil || !result.AI.Applied || result.AI.OperationRef == "" {
		t.Fatalf("result %+v", result)
	}
	payload := b.stored.Payload
	if origin := originPayload(payload); origin == nil || origin.SourceProject != "beta" || origin.SourceKind != "client_declared" {
		t.Fatalf("stored origin missing or replaced: %#v", payload["origin"])
	}
	if payload["text"] != "synthetic alpha source" || payload["namespace"] != "projects" || payload["permanent"] != true || payload["valid_until"] != "2099-01-01" || payload["primary_tag"] != "alpha" {
		t.Fatalf("changed non-grouping metadata %#v", payload)
	}
	path := filepath.Join(cfg.StateDir, "write-audits", result.AI.OperationRef+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 || strings.Contains(string(raw), "synthetic alpha source") || !strings.Contains(string(raw), `"status":"stored"`) {
		t.Fatalf("unsafe audit %s", raw)
	}
	if payload["ai_grouping_ref"] != result.AI.OperationRef {
		t.Fatal("audit reference missing")
	}
}
func TestAIWriteCallerGroupingAndNamespaceWin(t *testing.T) {
	cases := []map[string]interface{}{
		{"fact": "synthetic one", "namespace": "projects", "tags": "alpha"},
		{"fact": "synthetic explicit", "namespace": "projects", "tags": "alpha,beta", "primary_tag": "beta"},
		{"fact": "synthetic personal", "namespace": "personal"},
	}
	for _, args := range cases {
		cfg := aiTestConfig(t, "on", "off")
		b := &aiTestBackend{}
		s := newAIServer(t, b)
		p := &fakeAIProvider{}
		if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
			t.Fatal(err)
		}
		result := aiStoredResult(t, s, args)
		if result.AI != nil || p.writes.Load() != 0 {
			t.Fatal("explicit baseline classification overridden")
		}
	}
}
func TestAIWriteFallbacksAndEgressRefusal(t *testing.T) {
	cases := []struct {
		name   string
		result aijudgment.ClassifyResult
		fail   bool
		tags   string
		deny   bool
		origin bool
	}{
		{name: "abstention", result: aijudgment.ClassifyResult{Status: "abstained"}},
		{name: "foreign-primary", result: aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "unknown"}},
		{name: "foreign-related", result: aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "alpha", RelatedTags: []string{"unknown"}}},
		{name: "duplicate-related", result: aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "alpha", RelatedTags: []string{"beta", "beta"}}},
		{name: "provider-error", fail: true},
		{name: "original-tags", tags: "alpha,denied"},
		{name: "catalog-egress", deny: true},
		{name: "origin-egress", origin: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := aiTestConfig(t, "on", "off")
			if tt.deny {
				cfg.Egress.AllowedProjectTags = []string{"alpha"}
			}
			b := &aiTestBackend{}
			s := newAIServer(t, b)
			p := &fakeAIProvider{classify: func(context.Context, aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
				if tt.fail {
					return tt.result, aijudgment.Usage{}, errors.New("private provider diagnostic")
				}
				return tt.result, aijudgment.Usage{}, nil
			}}
			if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
				t.Fatal(err)
			}
			args := map[string]interface{}{"fact": "synthetic fallback", "namespace": "projects", "tags": tt.tags}
			if tt.origin {
				args["source_project"] = "denied"
				args["source_kind"] = "user_declared"
			}
			result := aiStoredResult(t, s, args)
			if b.stored.Payload["primary_tag"] != "" || result.AI.Applied {
				t.Fatal("fallback modified grouping")
			}
			if (tt.deny || tt.origin || tt.tags != "") && p.writes.Load() != 0 {
				t.Fatal("denied payload sent")
			}
		})
	}
}
func TestAIWriteDuplicateRemainsDuplicate(t *testing.T) {
	cfg := aiTestConfig(t, "on", "off")
	b := &aiTestBackend{points: []qdrant.Point{aiPoint("1", .99, "", false)}}
	s := newAIServer(t, b)
	p := &fakeAIProvider{}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	result := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic duplicate", "namespace": "projects"})
	if result.Status != "duplicate" || result.Stored || result.AI.Applied || b.upserts != 0 {
		t.Fatalf("duplicate changed %+v", result)
	}
}

func TestAIReadUsesWholePoolAndPreservesLifecycleAuthority(t *testing.T) {
	cfg := aiTestConfig(t, "off", "on")
	points := []qdrant.Point{aiPoint("1", .99, lifecycle.Historical, false), aiPoint("2", .98, lifecycle.Disputed, false), aiPoint("3", .97, lifecycle.Current, false), aiPoint("4", .96, lifecycle.Current, true), aiPoint("5", .95, lifecycle.Current, false), aiPoint("6", .94, lifecycle.Superseded, false)}
	b := &aiTestBackend{points: points}
	s := newAIServer(t, b)
	p := &fakeAIProvider{}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, nil, p); err != nil {
		t.Fatal(err)
	}
	result := aiRecallResult(t, s, map[string]interface{}{"query": "synthetic query", "namespace": "projects", "lifecycle_mode": "history", "limit": 6})
	want := []string{"4", "5", "3", "2", "1", "6"}
	for i, id := range want {
		f := result.Facts[i]
		if f.PointID != id || f.AIRelevance == nil || f.FinalRank != i+1 {
			t.Fatalf("rank[%d] %+v", i, f)
		}
		original := map[string]int{"1": 1, "2": 2, "3": 3, "4": 4, "5": 5, "6": 6}
		if f.SemanticRank != original[id] {
			t.Fatal("semantic rank modified")
		}
	}
	if !result.AI.Applied || b.gets != 6 {
		t.Fatal("did not verify complete candidate pool")
	}
	// Limiting follows complete-pool inference: candidate 5 can enter a top-2 response.
	limited := aiRecallResult(t, s, map[string]interface{}{"query": "synthetic query", "namespace": "projects", "lifecycle_mode": "history", "limit": 2})
	if limited.Facts[1].PointID != "5" || p.reads.Load() != 2 || b.searches != 2 {
		t.Fatal("rerank narrowed candidates or cached derived response")
	}
}
func TestAIReadFallbackWholeBaseline(t *testing.T) {
	cases := []struct {
		name    string
		output  aijudgment.RankResult
		failure bool
	}{
		{name: "partial", output: aijudgment.RankResult{Status: "decided", Scores: map[string]float64{"c1": .1}}},
		{name: "extra", output: aijudgment.RankResult{Status: "decided", Scores: map[string]float64{"c1": .1, "c2": .9, "foreign": .5}}},
		{name: "nan", output: aijudgment.RankResult{Status: "decided", Scores: map[string]float64{"c1": math.NaN(), "c2": .9}}},
		{name: "out-of-range", output: aijudgment.RankResult{Status: "decided", Scores: map[string]float64{"c1": -.1, "c2": .9}}},
		{name: "none", output: aijudgment.RankResult{Status: "decided", Scores: map[string]float64{"c1": .1, "c2": .9}, NoneRelevant: true}},
		{name: "abstained", output: aijudgment.RankResult{Status: "abstained"}},
		{name: "error", failure: true},
		{name: "ties", output: aijudgment.RankResult{Status: "decided", Scores: map[string]float64{"c1": .5, "c2": .5}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := aiTestConfig(t, "off", "on")
			b := &aiTestBackend{points: []qdrant.Point{aiPoint("1", .9, "", false), aiPoint("2", .8, "", false)}}
			s := newAIServer(t, b)
			p := &fakeAIProvider{rank: func(context.Context, aijudgment.RankInput) (aijudgment.RankResult, aijudgment.Usage, error) {
				if tt.failure {
					return tt.output, aijudgment.Usage{}, errors.New("synthetic failure")
				}
				return tt.output, aijudgment.Usage{}, nil
			}}
			if err := s.ConfigureAIWithProviders(context.Background(), cfg, nil, p); err != nil {
				t.Fatal(err)
			}
			result := aiRecallResult(t, s, map[string]interface{}{"query": "synthetic query", "namespace": "projects", "limit": 1})
			if result.Facts[0].PointID != "1" || b.gets != 2 {
				t.Fatalf("partial rerank or missing verify %+v", result)
			}
		})
	}
}
func TestAIReadRefusesEntireEgressAndOversizedPool(t *testing.T) {
	for _, kind := range []string{"candidate", "catalog", "missing-catalog", "corrupt-catalog", "cap"} {
		t.Run(kind, func(t *testing.T) {
			cfg := aiTestConfig(t, "off", "on")
			points := []qdrant.Point{aiPoint("1", .9, "", false), aiPoint("2", .8, "", false)}
			if kind == "candidate" {
				points[1].Payload["namespace"] = "personal"
			}
			if kind == "catalog" {
				cfg.Egress.AllowedProjectTags = []string{"alpha"}
			}
			if kind == "missing-catalog" {
				_ = os.RemoveAll(cfg.CatalogDir)
			}
			if kind == "corrupt-catalog" {
				if err := os.WriteFile(filepath.Join(cfg.CatalogDir, "active.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "cap" {
				for i := 3; i <= 21; i++ {
					points = append(points, aiPoint(string(rune('a'+i)), .7, "", false))
				}
			}
			b := &aiTestBackend{points: points}
			s := newAIServer(t, b)
			p := &fakeAIProvider{}
			if err := s.ConfigureAIWithProviders(context.Background(), cfg, nil, p); err != nil {
				t.Fatal(err)
			}
			result := aiRecallResult(t, s, map[string]interface{}{"query": "synthetic query", "limit": 5})
			if p.reads.Load() != 0 || result.AI.Applied || result.Facts[0].PointID != "1" {
				t.Fatal("unauthorized or partial request sent")
			}
		})
	}
}
func TestAIReadConcurrentMutationsReturnRefreshedEligibleBaseline(t *testing.T) {
	for _, kind := range []string{"quarantine", "delete", "namespace", "tag", "historical", "expiry", "text", "counter"} {
		t.Run(kind, func(t *testing.T) {
			cfg := aiTestConfig(t, "off", "on")
			b := &aiTestBackend{points: []qdrant.Point{aiPoint("1", .9, "", false), aiPoint("2", .8, "", false)}}
			s := newAIServer(t, b)
			p := &fakeAIProvider{rank: func(ctx context.Context, in aijudgment.RankInput) (aijudgment.RankResult, aijudgment.Usage, error) {
				b.mu.Lock()
				defer b.mu.Unlock()
				// Copy before mutation: the semantic search response already owns old payloads.
				switch kind {
				case "quarantine":
					b.points[0].Payload["maintenance_status"] = "quarantined"
				case "delete":
					b.points = b.points[1:]
				case "namespace":
					b.points[0].Payload["namespace"] = "personal"
				case "tag":
					b.points[0].Payload["tags"] = []string{"beta"}
				case "historical":
					b.points[0].Payload["lifecycle_state"] = "historical"
				case "expiry":
					b.points[0].Payload["valid_until"] = "2000-01-01"
				case "text":
					b.points[0].Payload["text"] = "synthetic updated fact"
				case "counter":
					b.points[0].Payload["recall_count"] = 7
				}
				return aijudgment.RankResult{Status: "decided", Scores: map[string]float64{"c1": .1, "c2": .9}}, aijudgment.Usage{Known: true, InputTokens: 10}, nil
			}}
			if err := s.ConfigureAIWithProviders(context.Background(), cfg, nil, p); err != nil {
				t.Fatal(err)
			}
			args := map[string]interface{}{"query": "synthetic query", "namespace": "projects", "tags": "alpha", "limit": 2}
			result := aiRecallResult(t, s, args)
			if kind == "counter" {
				if !result.AI.Applied || result.Facts[0].PointID != "2" || result.Facts[1].RecallCount != 8 {
					t.Fatalf("counter-only mutation broke ranking %+v", result)
				}
				return
			}
			if result.AI.Applied || result.AI.Status != "candidate_changed" {
				t.Fatalf("stale ranking kept %+v", result)
			}
			if kind == "text" {
				if result.Facts[0].Text != "synthetic updated fact" || result.Facts[0].PointID != "1" {
					t.Fatal("fresh baseline lost updated content")
				}
				return
			}
			if result.Count != 1 || result.Facts[0].PointID != "2" {
				t.Fatalf("ineligible stale fact returned %+v", result)
			}
		})
	}
}
func TestAIReadUnverifiableCandidateFailsSafely(t *testing.T) {
	cfg := aiTestConfig(t, "off", "on")
	b := &aiTestBackend{points: []qdrant.Point{aiPoint("1", .9, "", false)}}
	s := newAIServer(t, b)
	p := &fakeAIProvider{rank: func(context.Context, aijudgment.RankInput) (aijudgment.RankResult, aijudgment.Usage, error) {
		b.mu.Lock()
		b.getFails = true
		b.mu.Unlock()
		return aijudgment.RankResult{Status: "decided", Scores: map[string]float64{"c1": .9}}, aijudgment.Usage{}, nil
	}}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, nil, p); err != nil {
		t.Fatal(err)
	}
	result, _ := s.recallFacts(context.Background(), toolRequest(map[string]interface{}{"query": "synthetic query"}))
	if !result.IsError || strings.Contains(toolResultText(t, result), "synthetic fact") {
		t.Fatal("unverified content returned")
	}
}
func TestAIShadowWritesBaselineAndWorkerStops(t *testing.T) {
	cfg := aiTestConfig(t, "shadow", "shadow")
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	started := make(chan struct{})
	release := make(chan struct{})
	p := &fakeAIProvider{classify: func(ctx context.Context, in aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
		if in.Origin != nil {
			t.Error("shadow classification received recording origin")
		}
		close(started)
		select {
		case <-ctx.Done():
			return aijudgment.ClassifyResult{}, aijudgment.Usage{}, ctx.Err()
		case <-release:
		}
		return aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "alpha"}, aijudgment.Usage{}, nil
	}}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, p); err != nil {
		t.Fatal(err)
	}
	result := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic shadow", "namespace": "projects", "source_project": "beta", "source_kind": "client_declared"})
	if b.stored.Payload["primary_tag"] != "" || result.AI.Applied || b.upserts != 1 {
		t.Fatal("shadow mutated baseline")
	}
	if origin := originPayload(b.stored.Payload); origin == nil || origin.SourceProject != "beta" {
		t.Fatal("shadow did not preserve stored origin")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("shadow did not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.ShutdownAI(ctx); err != nil {
		t.Fatal(err)
	}
	close(release)
	if b.upserts != 1 {
		t.Fatal("shadow performed mutation")
	}
}
func TestAIShadowRecallDoesNotCountOrRequery(t *testing.T) {
	cfg := aiTestConfig(t, "off", "shadow")
	b := &aiTestBackend{points: []qdrant.Point{aiPoint("1", .9, "", false), aiPoint("2", .8, "", false)}}
	s := newAIServer(t, b)
	p := &fakeAIProvider{}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, nil, p); err != nil {
		t.Fatal(err)
	}
	result := aiRecallResult(t, s, map[string]interface{}{"query": "synthetic query", "limit": 1})
	if result.Facts[0].PointID != "1" || result.Facts[0].RecallCount != 1 {
		t.Fatal("shadow altered baseline recall")
	}
	deadline := time.Now().Add(time.Second)
	for p.reads.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.ShutdownAI(ctx); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if p.reads.Load() != 1 || b.searches != 1 || b.gets != 0 {
		t.Fatal("shadow repeated recall reads")
	}
}
func TestAISharedBudgetAndImmutableConfiguration(t *testing.T) {
	cfg := aiTestConfig(t, "on", "on")
	cfg.Limits.DailyCalls = 1
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	p := &fakeAIProvider{}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, p); err != nil {
		t.Fatal(err)
	}
	cfg.Egress.AllowedProjectTags[0] = "denied"
	delete(cfg.Profiles, "judge")
	result := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic budget", "namespace": "projects"})
	if !result.AI.Applied {
		t.Fatal("caller mutated pinned config")
	}
	b.mu.Lock()
	b.points = []qdrant.Point{aiPoint("1", .9, "", false)}
	b.mu.Unlock()
	recall := aiRecallResult(t, s, map[string]interface{}{"query": "synthetic query"})
	if p.reads.Load() != 0 || recall.AI.Applied {
		t.Fatal("write/read did not share budget")
	}
}
func TestAIWriteCanceledCallerDoesNotMutate(t *testing.T) {
	cfg := aiTestConfig(t, "on", "off")
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	p := &fakeAIProvider{classify: func(context.Context, aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
		cancel()
		return aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "alpha"}, aijudgment.Usage{}, nil
	}}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	result, _ := s.storeFact(ctx, toolRequest(map[string]interface{}{"fact": "synthetic canceled", "namespace": "projects"}))
	if !result.IsError || b.upserts != 0 {
		t.Fatal("canceled caller stored fact")
	}
}
func TestAIProviderDeadlineFallsBack(t *testing.T) {
	cfg := aiTestConfig(t, "on", "off")
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	p := &fakeAIProvider{classify: func(ctx context.Context, in aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("missing deadline")
		}
		<-ctx.Done()
		return aijudgment.ClassifyResult{}, aijudgment.Usage{}, ctx.Err()
	}}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	result := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic timeout", "namespace": "projects"})
	if result.AI.Applied || b.stored.Payload["primary_tag"] != "" || time.Since(begin) > 2*time.Second {
		t.Fatal("deadline failed to fall open")
	}
}
func TestAIShadowQueueIsBoundedAndConfigShutdownCancels(t *testing.T) {
	cfg := aiTestConfig(t, "on", "off")
	s := newAIServer(t, &aiTestBackend{})
	p := &fakeAIProvider{}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	a := s.aiState()
	started := make(chan struct{})
	count := atomic.Int64{}
	a.enqueue(func(ctx context.Context) { close(started); <-ctx.Done() })
	<-started
	for i := 0; i < 100; i++ {
		a.enqueue(func(context.Context) { count.Add(1) })
	}
	if len(a.jobs) != aiShadowQueueSize {
		t.Fatal("unbounded shadow queue")
	}
	if err := s.ConfigureAI(context.Background(), aipolicy.Off()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.done:
	case <-time.After(time.Second):
		t.Fatal("worker survived disable")
	}
	if count.Load() != 0 {
		t.Fatal("queued shadow job ran after disable")
	}
}
func TestAIFingerprintIgnoresOnlyCounters(t *testing.T) {
	p := map[string]interface{}{"text": "synthetic", "namespace": "projects", "tags": []string{"alpha"}, "primary_tag": "alpha", "recall_count": 1}
	before := aiFingerprint(p)
	p["recall_count"] = 100
	p["last_recalled_at"] = "changed"
	if aiFingerprint(p) != before {
		t.Fatal("counter became semantic dependency")
	}
	p["primary_tag"] = "beta"
	if aiFingerprint(p) == before {
		t.Fatal("grouping excluded from dependency")
	}
}
func TestAICacheCloneOwnsOptionalFields(t *testing.T) {
	score := .5
	original := RecallFactsResult{AI: &AIOutcome{Status: "decided"}, Facts: []RecallFact{{AIRelevance: &score}}}
	copy := cloneRecallFactsResult(original)
	copy.AI.Status = "invalid"
	*copy.Facts[0].AIRelevance = .9
	if original.AI.Status != "decided" || !reflect.DeepEqual(*original.Facts[0].AIRelevance, .5) {
		t.Fatal("AI cache fields alias input")
	}
}

func TestAIProductionConstructorReadsOnlyActiveKeyAndNoCatalogAtStartup(t *testing.T) {
	cfg := aiTestConfig(t, "on", "off")
	key := filepath.Join(filepath.Dir(cfg.StateDir), "active-key")
	if err := os.WriteFile(key, []byte("synthetic-offline-key"), 0600); err != nil {
		t.Fatal(err)
	}
	profile := cfg.Profiles["judge"]
	profile.KeyFile = key
	cfg.Profiles["judge"] = profile
	cfg.Profiles["inactive"] = aipolicy.Profile{KeyFile: "/missing/inactive-key"}
	cfg.Read.Profile = "inactive"
	if err := os.RemoveAll(cfg.CatalogDir); err != nil {
		t.Fatal(err)
	}
	s := newAIServer(t, &aiTestBackend{})
	if err := s.ConfigureAI(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if s.aiState() == nil || s.aiState().write == nil || s.aiState().read != nil {
		t.Fatal("wrong profiles initialized")
	}
	// There is no active catalog. Storage uses baseline and performs no model call.
	result := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic no catalog", "namespace": "projects"})
	if result.AI.Applied {
		t.Fatal("missing catalog inferred grouping")
	}
}
func TestAIUpdateAndImportNeverClassify(t *testing.T) {
	cfg := aiTestConfig(t, "on", "off")
	point := aiPoint("1", .8, "", false)
	point.Payload["tags"] = []string{}
	point.Payload["primary_tag"] = ""
	b := &aiTestBackend{points: []qdrant.Point{point}}
	s := newAIServer(t, b)
	p := &fakeAIProvider{}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	updated, err := s.updateFact(context.Background(), toolRequest(map[string]interface{}{"point_id": "1", "new_fact": "synthetic edited", "namespace": "projects"}))
	if err != nil || updated.IsError {
		t.Fatalf("update %v %#v", err, updated)
	}
	imported, err := s.importFacts(context.Background(), toolRequest(map[string]interface{}{"facts": `[{"text":"synthetic imported","namespace":"projects","tags":[]}]`}))
	if err != nil || imported.IsError {
		t.Fatalf("import %v %#v", err, imported)
	}
	if p.writes.Load() != 0 || b.stored.Payload["primary_tag"] != "" {
		t.Fatal("update/import performed inference")
	}
}
func TestAIWriteAuditFailureReturnsBaseline(t *testing.T) {
	cfg := aiTestConfig(t, "on", "off")
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	p := &fakeAIProvider{}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "write-audits"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	result := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic unavailable audit", "namespace": "projects", "tags": "alpha,beta"})
	if result.AI.Applied || result.AI.OperationRef != "" || result.AI.Status != "unavailable" || b.stored.Payload["primary_tag"] != "" {
		t.Fatalf("unauditable grouping applied %+v", result)
	}
	tags := relatedCandidateTags(b.stored.Payload["tags"])
	if !reflect.DeepEqual(tags, []string{"alpha", "beta"}) {
		t.Fatal("baseline tags discarded")
	}
}
func TestAIWriteInferenceAddsGroupingWithoutRemovingCallerTags(t *testing.T) {
	cfg := aiTestConfig(t, "on", "off")
	cfg.Egress.AllowedProjectTags = append(cfg.Egress.AllowedProjectTags, "topic-a", "topic-b")
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	p := &fakeAIProvider{}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	result := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic topics", "namespace": "projects", "tags": "topic-a,topic-b"})
	if !result.AI.Applied || !reflect.DeepEqual(relatedCandidateTags(b.stored.Payload["tags"]), []string{"topic-a", "topic-b", "beta", "alpha"}) {
		t.Fatal("caller topical tags lost")
	}
}

func TestAIReadWithoutContextDiscardsChangedCatalogRanking(t *testing.T) {
	cfg := aiTestConfig(t, "off", "on")
	b := &aiTestBackend{points: []qdrant.Point{aiPoint("1", .9, "", false), aiPoint("2", .8, "", false)}}
	s := newAIServer(t, b)
	before, originalHash, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeAIProvider{rank: func(ctx context.Context, in aijudgment.RankInput) (aijudgment.RankResult, aijudgment.Usage, error) {
		if in.ProjectContext != nil {
			t.Error("old-client request unexpectedly acquired project context")
		}
		next := before
		next.Version = "concurrent-description-update"
		next.ParentHash = originalHash
		next.Entries = append([]contextcatalog.Entry{}, before.Entries...)
		next.Entries[0].Summary = "Changed project description while relevance inference was running"
		if _, err := contextcatalog.Publish(cfg.CatalogDir, next, originalHash); err != nil {
			t.Error(err)
		}
		return aijudgment.RankResult{Status: "decided", Scores: map[string]float64{"c1": .1, "c2": .9}}, aijudgment.Usage{Known: true, InputTokens: 5}, nil
	}}
	if err := s.ConfigureAIWithProviders(context.Background(), cfg, nil, provider); err != nil {
		t.Fatal(err)
	}
	got := aiRecallResult(t, s, map[string]interface{}{"query": "synthetic query", "namespace": "projects"})
	if got.AI == nil || got.AI.Status != "catalog_changed" || got.AI.Applied || got.AI.CatalogHash != originalHash {
		t.Fatalf("stale catalog judgment applied: %+v", got)
	}
	if got.Count != 2 || got.Facts[0].PointID != "1" || got.Facts[1].PointID != "2" || got.Facts[0].AIRelevance != nil || got.Facts[1].AIRelevance != nil {
		t.Fatalf("baseline order not restored: %+v", got)
	}
	if provider.reads.Load() != 1 {
		t.Fatal("unexpected inference retries")
	}
	b.mu.Lock()
	gets := b.gets
	b.mu.Unlock()
	if gets != 2 {
		t.Fatalf("candidate freshness omitted: gets=%d", gets)
	}
}
