package memory

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
)

const aiOperationTimeout = time.Second
const aiShadowQueueSize = 8

type aiRuntime struct {
	owner       string
	cfg         aipolicy.Config
	write, read aijudgment.Provider
	budget      *aipolicy.Budget
	ctx         context.Context
	cancel      context.CancelFunc
	jobs        chan func(context.Context)
	done        chan struct{}
	mu          sync.Mutex
	closed      bool
}

// AIOutcome describes optional grouping independently of the storage outcome.
type AIOutcome struct {
	SubjectDecision string `json:"subject_decision,omitempty"`
	Mode            string `json:"mode"`
	Status          string `json:"status"`
	CatalogHash     string `json:"catalog_hash,omitempty"`
	Applied         bool   `json:"applied"`
	OperationRef    string `json:"operation_ref,omitempty"`
}

// ConfigureAI pins operator configuration. It performs no provider calls and
// reads keys only for active read/write profiles. Storage failure disables
// optional inference, while invalid profiles or keys reject configuration.
func (s *Server) ConfigureAI(ctx context.Context, cfg aipolicy.Config) error {
	return s.configureAI(ctx, cfg, nil, nil, false)
}

// ConfigureAIWithProviders injects isolated judgment adapters without reading
// secrets. Production uses ConfigureAI instead.
func (s *Server) ConfigureAIWithProviders(ctx context.Context, cfg aipolicy.Config, write, read aijudgment.Provider) error {
	return s.configureAI(ctx, cfg, write, read, true)
}

func (s *Server) configureAI(ctx context.Context, cfg aipolicy.Config, write, read aijudgment.Provider, injected bool) error {
	raw, err := json.Marshal(cfg)
	cfg = aipolicy.Config{}
	if err != nil || json.Unmarshal(raw, &cfg) != nil {
		return errors.New("invalid optional AI configuration")
	}
	cfg.Defaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	if !cfg.Active() {
		if !cfg.Registration.Active() {
			return s.replaceAI(nil)
		}
		return s.configureRegistry(ctx, cfg)
	}
	if !injected {
		for _, slot := range []struct {
			feature aipolicy.Feature
			target  *aijudgment.Provider
		}{{cfg.Write, &write}, {cfg.Read, &read}} {
			if !slot.feature.Active() {
				continue
			}
			p := cfg.Profiles[slot.feature.Profile]
			key, err := p.ReadKey()
			if err != nil {
				return err
			}
			adapter, err := aijudgment.New(slot.feature.Provider, aijudgment.Config{Model: p.Model, Endpoint: p.Endpoint, APIKey: key, Timeout: p.Timeout(aiOperationTimeout)})
			if err != nil {
				return err
			}
			*slot.target = adapter
		}
	}
	if (cfg.Write.Active() && write == nil) || (cfg.Read.Active() && read == nil) {
		return errors.New("active judgment adapter missing")
	}
	budget, err := aipolicy.NewBudget(cfg.StateDir, cfg.Limits)
	if err != nil {
		slog.Warn("optional AI state unavailable; baseline enabled")
		if cfg.Registration.Active() {
			cfg.Read = aipolicy.Feature{Mode: "off", Provider: "none"}
			cfg.Write = aipolicy.Feature{Mode: "off", Provider: "none"}
			cfg.Maintenance.Enabled = false
			return s.configureRegistry(ctx, cfg)
		}
		return s.replaceAI(nil)
	}
	runtimeCtx, cancel := context.WithCancel(ctx)
	a := &aiRuntime{owner: s.user, cfg: cfg, write: write, read: read, budget: budget, ctx: runtimeCtx, cancel: cancel, jobs: make(chan func(context.Context), aiShadowQueueSize), done: make(chan struct{})}
	go a.run()
	return s.replaceAI(a)
}

// Registry-only configuration performs no storage writes, key reads, or inference.
func (s *Server) configureRegistry(ctx context.Context, cfg aipolicy.Config) error {
	runtimeCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	close(done)
	return s.replaceAI(&aiRuntime{owner: s.user, cfg: cfg, ctx: runtimeCtx, cancel: cancel, done: done})
}

func (s *Server) replaceAI(next *aiRuntime) error {
	s.aiMu.Lock()
	previous := s.ai
	s.ai = next
	s.aiMu.Unlock()
	if previous != nil {
		previous.stop()
	}
	s.cache.Invalidate()
	return nil
}
func (s *Server) aiState() *aiRuntime {
	s.aiMu.RLock()
	defer s.aiMu.RUnlock()
	return s.ai
}

// AIBudget is shared with the maintenance worker; it never owns a second ledger.
func (s *Server) AIBudget() *aipolicy.Budget {
	if a := s.aiState(); a != nil {
		return a.budget
	}
	return nil
}
func (s *Server) ShutdownAI(ctx context.Context) error {
	a := s.aiState()
	if a == nil {
		return nil
	}
	a.stop()
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (a *aiRuntime) stop() { a.mu.Lock(); a.closed = true; a.cancel(); a.mu.Unlock() }
func (a *aiRuntime) enqueue(job func(context.Context)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.ctx.Err() != nil {
		return
	}
	select {
	case a.jobs <- job:
	default:
	}
}
func (a *aiRuntime) run() {
	defer close(a.done)
	for {
		select {
		case <-a.ctx.Done():
			return
		case job := <-a.jobs:
			if a.ctx.Err() != nil {
				return
			}
			ctx, cancel := context.WithTimeout(a.ctx, aiOperationTimeout)
			job(ctx)
			cancel()
		}
	}
}

// Eligible project descriptions can leave the server. Evidence references
// remain local even when using an injected provider.
func (a *aiRuntime) catalog() (contextcatalog.Snapshot, string, bool) {
	snap, hash, err := contextcatalog.LoadActive(a.cfg.CatalogDir)
	if err != nil {
		return contextcatalog.Snapshot{}, "", false
	}
	safe := snap
	safe.Entries = nil
	for _, e := range snap.Entries {
		if e.Namespace != "projects" || !contextcatalog.Eligible(e) || (e.Owner != "" && e.Owner != a.owner) {
			continue
		}
		if !a.cfg.Egress.Allows(e.Namespace, []string{e.Tag}) {
			return contextcatalog.Snapshot{}, "", false
		}
		e = safeCatalogEntry(e)
		safe.Entries = append(safe.Entries, e)
	}
	return safe, hash, true
}

// Drop local identity and source excerpts before crossing even an injected
// inference boundary. The review status retains declared-versus-reviewed scope.
func safeCatalogEntry(e contextcatalog.Entry) contextcatalog.Entry {
	e.EvidenceRefs = nil
	e.ClientEvidence = nil
	e.ProjectID = ""
	e.ProjectKey = ""
	e.Owner = ""
	e.DeclaredSummary = ""
	return e
}
