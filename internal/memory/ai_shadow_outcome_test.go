package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
)

func shadowJournal(t *testing.T, dir string) aiWriteJournal {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		files, _ := filepath.Glob(filepath.Join(dir, "write-audits", "*.json"))
		if len(files) > 0 {
			raw, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			info, _ := os.Stat(files[0])
			if info.Mode().Perm() != 0600 || strings.Contains(string(raw), "PRIVATE_ASSERTION") || strings.Contains(string(raw), "SECRET_ERROR") {
				t.Fatal("unsafe journal")
			}
			var j aiWriteJournal
			if err = json.Unmarshal(raw, &j); err != nil {
				t.Fatal(err)
			}
			return j
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("shadow journal missing")
	return aiWriteJournal{}
}

func TestShadowConfiguredDeadlineAndFailureJournal(t *testing.T) {
	for _, variant := range []string{"success-after-one-second", "timeout", "unsafe-error", "invalid-response", "egress", "budget"} {
		t.Run(variant, func(t *testing.T) {
			cfg := aiTestConfig(t, "shadow", "off")
			cfg.Write.Admission = "client_opt_in"
			cfg.Write.ShadowTimeoutMS = 2000
			if variant == "timeout" {
				cfg.Write.ShadowTimeoutMS = 10
			}
			if variant == "egress" {
				cfg.Egress.UnassignedNamespaces = nil
			}
			if variant == "budget" {
				cfg.Limits.DailyCalls = 1
			}
			b := &aiTestBackend{}
			s := newAIServer(t, b)
			p := &fakeAIProvider{classify: func(ctx context.Context, in aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
				switch variant {
				case "success-after-one-second":
					select {
					case <-time.After(1100 * time.Millisecond):
					case <-ctx.Done():
						return aijudgment.ClassifyResult{}, aijudgment.Usage{}, ctx.Err()
					}
				case "timeout":
					<-ctx.Done()
					return aijudgment.ClassifyResult{}, aijudgment.Usage{}, ctx.Err()
				case "unsafe-error":
					return aijudgment.ClassifyResult{}, aijudgment.Usage{}, errors.New("SECRET_ERROR /private/path credential")
				case "invalid-response":
					return aijudgment.ClassifyResult{Status: "invalid"}, aijudgment.Usage{}, nil
				}
				return aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "alpha"}, aijudgment.Usage{Known: true, InputTokens: 10}, nil
			}}
			if err := configureAIForTest(t, s, context.Background(), cfg, p, nil); err != nil {
				t.Fatal(err)
			}
			if variant == "budget" {
				for i := int64(0); i < cfg.Limits.DailyCalls; i++ {
					res, err := s.AIBudget().Begin(time.Now())
					if err != nil {
						t.Fatal(err)
					}
					res.Finish(0, true)
				}
			}
			result := aiStoredResult(t, s, map[string]interface{}{"fact": "PRIVATE_ASSERTION", "namespace": "projects", "allow_ai_processing": true})
			if !result.Stored || result.AI == nil || result.AI.Applied {
				t.Fatal("storage/shadow contract changed")
			}
			j := shadowJournal(t, cfg.StateDir)
			want := map[string]string{"timeout": "timeout", "unsafe-error": "provider_transport", "invalid-response": "invalid_response", "egress": "egress_denied", "budget": "budget_unavailable"}[variant]
			if variant == "success-after-one-second" {
				if j.Status != "decided" || j.After.Primary != "alpha" || j.DurationMS < 1000 {
					t.Fatalf("configured timeout ineffective: %+v", j)
				}
			} else if j.Status != "failed" || j.ReasonCode != want {
				t.Fatalf("wrong failure %+v", j)
			}
			if value, _ := b.stored.Payload["primary_tag"].(string); value != "" {
				t.Fatal("shadow changed stored grouping")
			}
			if variant == "egress" || variant == "budget" {
				if p.writes.Load() != 0 {
					t.Fatal("rejected job dispatched")
				}
			}
		})
	}
}

func TestShadowQueueFullAndCancellationJournals(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "canceled"}[canceled], func(t *testing.T) {
			cfg := aiTestConfig(t, "shadow", "off")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := &aiRuntime{cfg: cfg, ctx: ctx, jobs: make(chan aiShadowJob, aiShadowQueueSize)}
			for i := 0; i < aiShadowQueueSize; i++ {
				a.jobs <- aiShadowJob{run: func(context.Context) {}, timeout: time.Second}
			}
			if canceled {
				cancel()
			}
			d := &aiWriteDecision{runtime: a, outcome: &AIOutcome{Mode: "shadow", Status: "shadow"}, before: aiGrouping{Tags: []string{}}, after: aiGrouping{Tags: []string{}}}
			d.shadow("synthetic-point", "PRIVATE_ASSERTION", "projects", nil)
			j := shadowJournal(t, cfg.StateDir)
			want := "queue_full"
			if canceled {
				want = "canceled"
			}
			if j.Status != "failed" || j.ReasonCode != want {
				t.Fatalf("wrong journal %+v", j)
			}
		})
	}
}

func TestReadShadowKeepsDefaultDeadline(t *testing.T) {
	cfg := aiTestConfig(t, "shadow", "off")
	cfg.Write.ShadowTimeoutMS = 5000
	s := newAIServer(t, &aiTestBackend{})
	if err := configureAIForTest(t, s, context.Background(), cfg, &fakeAIProvider{}, nil); err != nil {
		t.Fatal(err)
	}
	observed := make(chan time.Duration, 1)
	s.aiState().enqueue(func(ctx context.Context) { deadline, _ := ctx.Deadline(); observed <- time.Until(deadline) })
	select {
	case duration := <-observed:
		if duration <= 0 || duration > time.Second {
			t.Fatalf("read job budget changed: %s", duration)
		}
	case <-time.After(time.Second):
		t.Fatal("no job")
	}
}

func TestShadowQueuedCancellationRecordsWithoutDispatch(t *testing.T) {
	cfg := aiTestConfig(t, "shadow", "off")
	cfg.Write.ShadowTimeoutMS = 5000
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newAIServer(t, &aiTestBackend{})
	started := make(chan struct{})
	p := &fakeAIProvider{classify: func(ctx context.Context, _ aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
		close(started)
		<-ctx.Done()
		return aijudgment.ClassifyResult{}, aijudgment.Usage{}, ctx.Err()
	}}
	if err := configureAIForTest(t, s, ctx, cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	aiStoredResult(t, s, map[string]interface{}{"fact": "PRIVATE_ASSERTION running", "namespace": "projects"})
	<-started
	aiStoredResult(t, s, map[string]interface{}{"fact": "PRIVATE_ASSERTION queued", "namespace": "projects"})
	cancel()
	select {
	case <-s.aiState().done:
	case <-time.After(time.Second):
		t.Fatal("shutdown stalled")
	}
	paths, _ := filepath.Glob(filepath.Join(cfg.StateDir, "write-audits", "*.json"))
	if len(paths) != 2 || p.writes.Load() != 1 {
		t.Fatalf("cancellation coverage journals=%d dispatches=%d", len(paths), p.writes.Load())
	}
	for _, path := range paths {
		raw, _ := os.ReadFile(path)
		var j aiWriteJournal
		if json.Unmarshal(raw, &j) != nil || j.Status != "failed" || j.ReasonCode != "canceled" {
			t.Fatalf("wrong cancellation journal: %s", raw)
		}
	}
}
