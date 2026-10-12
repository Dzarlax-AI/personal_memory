package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func recommendationFixture(t *testing.T, p *fakeAIProvider) (*Server, *aiTestBackend, aipolicy.Config, string) {
	t.Helper()
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	cfg := aiTestConfig(t, "shadow", "off")
	cfg.Write.Admission = "client_opt_in"
	if err := configureAIForTest(t, s, context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	r := aiStoredResult(t, s, map[string]interface{}{"fact": "PRIVATE_ASSERTION", "namespace": "projects", "allow_ai_processing": true, "subject_scope": "unknown", "subject_context": "Synthetic archive component"})
	if r.AI == nil || !recommendationRefPattern.MatchString(r.AI.OperationRef) {
		t.Fatalf("missing early ref: %+v", r.AI)
	}
	shadowJournal(t, cfg.StateDir)
	return s, b, cfg, r.AI.OperationRef
}

func editRecommendationJournal(t *testing.T, cfg aipolicy.Config, ref string, edit func(*aiWriteJournal)) {
	t.Helper()
	path := filepath.Join(cfg.StateDir, "write-audits", ref+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var j aiWriteJournal
	if err = json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	edit(&j)
	raw, err = json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if err = aipolicy.AtomicWrite(filepath.Dir(path), filepath.Base(path), raw); err != nil {
		t.Fatal(err)
	}
}

func TestWriteRecommendationMCPProtocol(t *testing.T) {
	for _, provider := range []string{"decisions", "jev"} {
		t.Run(provider, func(t *testing.T) {
			b := &aiTestBackend{}
			s := newAIServer(t, b)
			cfg := aiTestConfig(t, "shadow", "off")
			cfg.Write.Admission = "client_opt_in"
			if provider == "jev" {
				cfg.Write.Provider = "jev"
				cfg.Profiles["judge"] = aipolicy.Profile{Protocol: "jev", Model: aijudgment.JevModel, Endpoint: aijudgment.JevURL, KeyFile: cfg.Profiles["judge"].KeyFile}
			}
			started := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			p := &fakeAIProvider{classify: func(ctx context.Context, in aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return aijudgment.ClassifyResult{}, aijudgment.Usage{}, ctx.Err()
				}
				return aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "alpha", RelatedTags: []string{"beta"}}, aijudgment.Usage{Known: true, InputTokens: 1}, nil
			}}
			if err := configureAIForTest(t, s, context.Background(), cfg, p, nil); err != nil {
				t.Fatal(err)
			}
			ms := server.NewMCPServer("recommendation-test", "1", server.WithToolCapabilities(true))
			s.RegisterTools(ms)
			hs := server.NewTestStreamableHTTPServer(ms)
			defer hs.Close()
			c, err := client.NewStreamableHttpClient(hs.URL)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err = c.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION, ClientInfo: mcp.Implementation{Name: "sample-client", Version: "1"}}}); err != nil {
				t.Fatal(err)
			}
			call := func(name string, args map[string]interface{}) *mcp.CallToolResult {
				t.Helper()
				req := toolRequest(args)
				req.Params.Name = name
				r, e := c.CallTool(ctx, req)
				if e != nil || r.IsError {
					t.Fatalf("%s: %v %+v", name, e, r)
				}
				return r
			}
			storeReply := call("store_fact", map[string]interface{}{"fact": "PRIVATE_ASSERTION", "namespace": "projects", "tags": "alpha,beta", "allow_ai_processing": true})
			raw, _ := json.Marshal(storeReply.StructuredContent)
			var stored StoreFactResult
			if json.Unmarshal(raw, &stored) != nil || !stored.Stored || stored.AI == nil || stored.AI.Applied || !recommendationRefPattern.MatchString(stored.AI.OperationRef) {
				t.Fatalf("bad response %s", raw)
			}
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("no provider dispatch")
			}
			ref := stored.AI.OperationRef
			fallbackFound := false
			for _, content := range storeReply.Content {
				if text, ok := content.(mcp.TextContent); ok && strings.Contains(text.Text, "ai_operation_ref: "+ref) {
					fallbackFound = true
				}
			}
			if !fallbackFound {
				t.Fatal("fallback omitted early operation reference")
			}
			read := func() WriteRecommendation {
				raw, _ := json.Marshal(call("get_write_recommendation", map[string]interface{}{"operation_ref": ref}).StructuredContent)
				for _, private := range []string{"PRIVATE_ASSERTION", "synthetic", stored.PointID, "input_fingerprint", "model", "point_id"} {
					if strings.Contains(string(raw), private) {
						t.Fatalf("private response: %s", raw)
					}
				}
				var got WriteRecommendation
				if json.Unmarshal(raw, &got) != nil {
					t.Fatal("bad result")
				}
				return got
			}
			if got := read(); got.Status != "pending" || got.Applied || got.SuggestedPrimaryTag != "" {
				t.Fatalf("pending %+v", got)
			}
			// Provider completion is released without closing twice in the deferred cleanup.
			release <- struct{}{}
			shadowJournal(t, cfg.StateDir)
			got := read()
			if got.Status != "decided" || got.SuggestedPrimaryTag != "alpha" || !reflect.DeepEqual(got.RecommendedRelatedTags, []string{"beta"}) || got.Applied {
				t.Fatalf("result %+v", got)
			}
			read()
			if p.writes.Load() != 1 {
				t.Fatal("lookup dispatched inference")
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.upserts != 1 || b.searches != 2 || b.stored.Payload["primary_tag"] != "" || !reflect.DeepEqual(b.stored.Payload["tags"], []interface{}{"alpha", "beta"}) || b.stored.Payload["recall_count"] != float64(0) {
				t.Fatalf("lookup mutated baseline: %+v", b.stored)
			}
		})
	}
}

func TestWriteRecommendationAbstentionsAndFailures(t *testing.T) {
	for _, choice := range []string{"non_project", "other_project", "multiple_projects", "insufficient_context"} {
		t.Run(choice, func(t *testing.T) {
			codes := []string(nil)
			if choice == "insufficient_context" {
				codes = []string{"insufficient_context"}
			}
			p := &fakeAIProvider{classify: func(context.Context, aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
				return aijudgment.ClassifyResult{Status: "abstained", SubjectDecision: choice, MissingContextCodes: codes}, aijudgment.Usage{Known: true}, nil
			}}
			s, _, _, ref := recommendationFixture(t, p)
			got := s.writeRecommendation(context.Background(), ref)
			if got.Status != "abstained" || got.SubjectDecision != choice || !reflect.DeepEqual(got.MissingContextCodes, codes) || got.SuggestedPrimaryTag != "" || len(got.RecommendedRelatedTags) != 0 {
				t.Fatalf("abstention %+v", got)
			}
		})
	}
	s, _, _, ref := recommendationFixture(t, &fakeAIProvider{classify: func(context.Context, aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
		return aijudgment.ClassifyResult{}, aijudgment.Usage{}, errors.New("SECRET_ERROR")
	}})
	if got := s.writeRecommendation(context.Background(), ref); got.Status != "failed" || got.ReasonCode != "provider_transport" {
		t.Fatalf("failure %+v", got)
	}
}

func TestWriteRecommendationFreshness(t *testing.T) {
	for _, change := range []string{"text", "subject", "tags", "primary_tag", "origin", "deleted", "expired", "quarantined", "historical", "catalog", "recall"} {
		t.Run(change, func(t *testing.T) {
			s, b, cfg, ref := recommendationFixture(t, &fakeAIProvider{})
			b.mu.Lock()
			switch change {
			case "text":
				b.stored.Payload["text"] = "changed"
			case "subject":
				b.stored.Payload["subject"] = map[string]interface{}{"scope": "project", "context": "another component"}
			case "tags":
				b.stored.Payload["tags"] = []string{"beta"}
			case "primary_tag":
				b.stored.Payload["primary_tag"] = "beta"
			case "origin":
				b.stored.Payload["origin"] = map[string]interface{}{"source_project": "beta"}
			case "deleted":
				b.stored = nil
			case "expired":
				b.stored.Payload["valid_until"] = "2000-01-01"
			case "quarantined":
				b.stored.Payload["maintenance_status"] = "quarantined"
			case "historical":
				b.stored.Payload["lifecycle_state"] = "historical"
			case "recall":
				b.stored.Payload["recall_count"] = 17
				b.stored.Payload["last_recalled_at"] = nowISO()
			}
			b.mu.Unlock()
			if change == "catalog" {
				snap, hash, err := contextcatalog.LoadActive(cfg.CatalogDir)
				if err != nil {
					t.Fatal(err)
				}
				snap.Version = "test-v2"
				snap.ParentHash = hash
				if _, err = contextcatalog.Publish(cfg.CatalogDir, snap, hash); err != nil {
					t.Fatal(err)
				}
			}
			got := s.writeRecommendation(context.Background(), ref)
			if change == "recall" {
				if got.Status != "decided" {
					t.Fatalf("telemetry invalidated %+v", got)
				}
			} else if got.Status != "outdated" || got.SuggestedPrimaryTag != "" || len(got.RecommendedRelatedTags) != 0 {
				t.Fatalf("stale %+v", got)
			}
		})
	}
}

func TestWriteRecommendationPrivateJournal(t *testing.T) {
	for _, variant := range []string{"unknown", "owner", "point-owner", "symlink", "dir-symlink", "permissions", "oversized", "v2", "bad-subject", "bad-reason", "bad-tag", "bad-related", "bad-missing", "backend"} {
		t.Run(variant, func(t *testing.T) {
			s, b, cfg, ref := recommendationFixture(t, &fakeAIProvider{})
			path := filepath.Join(cfg.StateDir, "write-audits", ref+".json")
			expected := "unavailable"
			switch variant {
			case "unknown":
				ref = strings.Repeat("0", 32)
				expected = "not_found"
			case "owner":
				editRecommendationJournal(t, cfg, ref, func(j *aiWriteJournal) { j.Owner = "another" })
				expected = "not_found"
			case "point-owner":
				b.mu.Lock()
				b.stored.Payload["user"] = "another"
				b.mu.Unlock()
				expected = "not_found"
			case "symlink":
				target := filepath.Join(cfg.StateDir, "target.json")
				raw, _ := os.ReadFile(path)
				if err := os.WriteFile(target, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "dir-symlink":
				dir := filepath.Dir(path)
				if err := os.Rename(dir, dir+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+"-real", dir); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(path, []byte(strings.Repeat("x", (64<<10)+1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "v2":
				editRecommendationJournal(t, cfg, ref, func(j *aiWriteJournal) { j.SchemaVersion = 2 })
			case "bad-subject":
				editRecommendationJournal(t, cfg, ref, func(j *aiWriteJournal) { j.SubjectDecision = "SECRET_ERROR" })
			case "bad-reason":
				editRecommendationJournal(t, cfg, ref, func(j *aiWriteJournal) { j.Status = "failed"; j.ReasonCode = "SECRET_ERROR" })
			case "bad-tag":
				editRecommendationJournal(t, cfg, ref, func(j *aiWriteJournal) { j.After.Primary = "SECRET_ERROR" })
			case "bad-related":
				editRecommendationJournal(t, cfg, ref, func(j *aiWriteJournal) { j.RecommendedRelatedTags = []string{"alpha"} })
			case "bad-missing":
				editRecommendationJournal(t, cfg, ref, func(j *aiWriteJournal) { j.MissingContextCodes = []string{"SECRET_ERROR"} })
			case "backend":
				b.mu.Lock()
				b.getFails = true
				b.mu.Unlock()
			}
			got := s.writeRecommendation(context.Background(), ref)
			if got.Status != expected || got.SuggestedPrimaryTag != "" || len(got.RecommendedRelatedTags) != 0 {
				t.Fatalf("unsafe %+v", got)
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "SECRET_ERROR") || strings.Contains(string(raw), "PRIVATE_ASSERTION") {
				t.Fatalf("leak %s", raw)
			}
		})
	}
}

func TestWriteRecommendationPendingRecovery(t *testing.T) {
	for _, variant := range []string{"restart", "deadline", "completed-restart"} {
		t.Run(variant, func(t *testing.T) {
			s, _, cfg, ref := recommendationFixture(t, &fakeAIProvider{})
			editRecommendationJournal(t, cfg, ref, func(j *aiWriteJournal) {
				if variant != "completed-restart" {
					j.Status = "pending"
				}
				if variant == "deadline" {
					j.PendingUntil = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
				}
			})
			if variant != "deadline" {
				if err := configureAIForTest(t, s, context.Background(), cfg, &fakeAIProvider{}, nil); err != nil {
					t.Fatal(err)
				}
			}
			got := s.writeRecommendation(context.Background(), ref)
			expected := map[string]string{"restart": "interrupted", "deadline": "result_expired"}[variant]
			if variant == "completed-restart" {
				if got.Status != "decided" {
					t.Fatalf("completion lost %+v", got)
				}
			} else if got.Status != "failed" || got.ReasonCode != expected {
				t.Fatalf("pending never terminates %+v", got)
			}
		})
	}
}

func TestWriteRecommendationMalformedAndOff(t *testing.T) {
	s := newAIServer(t, &aiTestBackend{})
	for _, args := range []map[string]interface{}{{}, {"operation_ref": 42}, {"operation_ref": "../secret"}, {"operation_ref": strings.Repeat("A", 32)}, {"operation_ref": strings.Repeat("a", 32), "extra": true}} {
		r, err := s.getWriteRecommendation(context.Background(), toolRequest(args))
		if err != nil || !r.IsError {
			t.Fatal("malformed accepted")
		}
	}
	for _, mode := range []string{"off", "on", "shadow"} {
		t.Run(mode, func(t *testing.T) {
			cfg := aiTestConfig(t, mode, "off")
			if err := configureAIForTest(t, s, context.Background(), cfg, &fakeAIProvider{}, nil); err != nil {
				t.Fatal(err)
			}
			ms := server.NewMCPServer("test", "1")
			s.RegisterTools(ms)
			_, present := ms.ListTools()["get_write_recommendation"]
			if present != (mode == "shadow") {
				t.Fatalf("mode %s present %t", mode, present)
			}
		})
	}
}

func TestWriteRecommendationJournalFailurePreservesStoredFact(t *testing.T) {
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	cfg := aiTestConfig(t, "shadow", "off")
	p := &fakeAIProvider{}
	if err := configureAIForTest(t, s, context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "write-audits"), []byte("block directory"), 0600); err != nil {
		t.Fatal(err)
	}
	r := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic fact", "namespace": "projects"})
	if !r.Stored || r.AI == nil || r.AI.Status != "unavailable" || r.AI.OperationRef != "" || p.writes.Load() != 0 {
		t.Fatalf("storage failed open incorrectly %+v", r)
	}
}

func TestWriteRecommendationBypassAndDuplicate(t *testing.T) {
	for _, variant := range []string{"denied", "primary", "single-tag", "non-project", "duplicate"} {
		t.Run(variant, func(t *testing.T) {
			b := &aiTestBackend{}
			s := newAIServer(t, b)
			cfg := aiTestConfig(t, "shadow", "off")
			cfg.Write.Admission = "client_opt_in"
			p := &fakeAIProvider{}
			if err := configureAIForTest(t, s, context.Background(), cfg, p, nil); err != nil {
				t.Fatal(err)
			}
			args := map[string]interface{}{"fact": "synthetic fact", "namespace": "projects", "allow_ai_processing": true}
			switch variant {
			case "denied":
				delete(args, "allow_ai_processing")
			case "primary":
				args["tags"] = "alpha,beta"
				args["primary_tag"] = "alpha"
			case "single-tag":
				args["tags"] = "alpha"
			case "non-project":
				args["namespace"] = "personal"
				args["subject_scope"] = "non_project"
			case "duplicate":
				b.points = []qdrant.Point{{ID: "existing", Score: 1, Payload: map[string]interface{}{"text": "synthetic fact", "namespace": "projects", "user": "synthetic"}}}
			}
			r := aiStoredResult(t, s, args)
			if (r.AI != nil && r.AI.OperationRef != "") || p.writes.Load() != 0 {
				t.Fatalf("bypass dispatched %+v", r)
			}
			files, _ := filepath.Glob(filepath.Join(cfg.StateDir, "write-audits", "*.json"))
			if len(files) != 0 {
				t.Fatal("bypass created journal")
			}
		})
	}
}

func TestWriteRecommendationTerminalSaveFailure(t *testing.T) {
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	cfg := aiTestConfig(t, "shadow", "off")
	started := make(chan struct{})
	release := make(chan struct{})
	p := &fakeAIProvider{classify: func(ctx context.Context, _ aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return aijudgment.ClassifyResult{}, aijudgment.Usage{}, ctx.Err()
		}
		return aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "alpha"}, aijudgment.Usage{Known: true}, nil
	}}
	if err := configureAIForTest(t, s, context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	r := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic terminal failure", "namespace": "projects"})
	<-started
	ref := r.AI.OperationRef
	path := filepath.Join(cfg.StateDir, "write-audits", ref+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(cfg.StateDir, "operator-target")
	if err = os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	close(release)
	// Drain the runtime to prove terminal persistence has already been attempted.
	drained := make(chan struct{})
	if !s.aiState().enqueue(func(context.Context) { close(drained) }) {
		t.Fatal("queue rejected")
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("worker stalled")
	}
	if got := s.writeRecommendation(context.Background(), ref); got.Status != "unavailable" {
		t.Fatalf("unsafe failed save %+v", got)
	}
	unchanged, _ := os.ReadFile(target)
	if string(unchanged) != "unchanged" {
		t.Fatal("symlink target overwritten")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	editRecommendationJournal(t, cfg, ref, func(j *aiWriteJournal) { j.PendingUntil = time.Now().Add(-time.Second).Format(time.RFC3339Nano) })
	if got := s.writeRecommendation(context.Background(), ref); got.Status != "failed" || got.ReasonCode != "result_expired" {
		t.Fatalf("lost terminal save hangs %+v", got)
	}
	if !r.Stored || p.writes.Load() != 1 {
		t.Fatal("failed journal changed store or retried provider")
	}
}

func TestWriteRecommendationQueuedCatalogChange(t *testing.T) {
	s := newAIServer(t, &aiTestBackend{})
	cfg := aiTestConfig(t, "shadow", "off")
	p := &fakeAIProvider{}
	if err := configureAIForTest(t, s, context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if !s.aiState().enqueue(func(ctx context.Context) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}) {
		t.Fatal("queue rejected")
	}
	<-started
	r := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic queued change", "namespace": "projects"})
	snap, hash, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	snap.Version = "next"
	snap.ParentHash = hash
	if _, err = contextcatalog.Publish(cfg.CatalogDir, snap, hash); err != nil {
		t.Fatal(err)
	}
	close(release)
	j := shadowJournal(t, cfg.StateDir)
	if j.OperationRef != r.AI.OperationRef || j.Status != "failed" || j.ReasonCode != "catalog_changed" || p.writes.Load() != 0 {
		t.Fatalf("queue dispatched stale catalog %+v", j)
	}
}

func TestWriteRecommendationSharedQueueDeadline(t *testing.T) {
	s := newAIServer(t, &aiTestBackend{})
	cfg := aiTestConfig(t, "shadow", "off")
	cfg.Write.ShadowTimeoutMS = 10
	started := make(chan struct{})
	release := make(chan struct{})
	p := &fakeAIProvider{classify: func(ctx context.Context, _ aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return aijudgment.ClassifyResult{}, aijudgment.Usage{}, ctx.Err()
	}}
	if err := configureAIForTest(t, s, context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	r := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic deadline", "namespace": "projects"})
	<-started
	raw, err := os.ReadFile(filepath.Join(cfg.StateDir, "write-audits", r.AI.OperationRef+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var j aiWriteJournal
	if err = json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	until, err := time.Parse(time.RFC3339Nano, j.PendingUntil)
	if err != nil {
		t.Fatal(err)
	}
	if until.Before(before.Add(10 * time.Second)) {
		t.Fatal("pending deadline ignores shared read jobs")
	}
	close(release)
}

func TestWriteRecommendationUnavailableUsesFiniteCodes(t *testing.T) {
	for _, variant := range []string{"budget", "egress", "timeout", "invalid"} {
		t.Run(variant, func(t *testing.T) {
			s := newAIServer(t, &aiTestBackend{})
			cfg := aiTestConfig(t, "shadow", "off")
			if variant == "egress" {
				cfg.Egress.UnassignedNamespaces = nil
			}
			if variant == "budget" {
				cfg.Limits.DailyCalls = 1
			}
			if variant == "timeout" {
				cfg.Write.ShadowTimeoutMS = 10
			}
			p := &fakeAIProvider{classify: func(ctx context.Context, _ aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
				if variant == "timeout" {
					<-ctx.Done()
					return aijudgment.ClassifyResult{}, aijudgment.Usage{}, ctx.Err()
				}
				return aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "outside"}, aijudgment.Usage{Known: true}, nil
			}}
			if err := configureAIForTest(t, s, context.Background(), cfg, p, nil); err != nil {
				t.Fatal(err)
			}
			if variant == "budget" {
				r, err := s.AIBudget().Begin(time.Now())
				if err != nil {
					t.Fatal(err)
				}
				r.Finish(0, true)
			}
			r := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic failure", "namespace": "projects"})
			shadowJournal(t, cfg.StateDir)
			got := s.writeRecommendation(context.Background(), r.AI.OperationRef)
			want := map[string]string{"budget": "budget_unavailable", "egress": "egress_denied", "timeout": "timeout", "invalid": "invalid_response"}[variant]
			if got.Status != "failed" || got.ReasonCode != want || got.SuggestedPrimaryTag != "" {
				t.Fatalf("failure %+v", got)
			}
			if (variant == "budget" || variant == "egress") && p.writes.Load() != 0 {
				t.Fatal("denied job dispatched")
			}
		})
	}
}
