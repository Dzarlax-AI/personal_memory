package aimaintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
)

func historyTestConfig(t *testing.T) aipolicy.Config {
	t.Helper()
	cfg := aipolicy.Off()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.StateDir = root
	cfg.Maintenance = aipolicy.Maintenance{Enabled: true, Scope: "catalog_only", ProjectPolicy: "registered", Profile: "maint", CatalogPublish: "review"}
	cfg.Profiles = map[string]aipolicy.Profile{"maint": {Model: "local-model", Protocol: "openai-compatible-chat"}}
	return cfg
}

func TestHistoryCreateOncePermissionsPaginationAndDraftValidation(t *testing.T) {
	cfg := historyTestConfig(t)
	store, err := newHistoryStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	one, err := store.newRun(cfg, "catalog-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := one.event(runEvent{Type: "provider_attempt_started"}); err != nil {
		t.Fatal(err)
	}
	proposal := []byte(`{"private":"draft"}`)
	sum := sha256.Sum256(proposal)
	name := "proposal-" + hex.EncodeToString(sum[:]) + ".json"
	proposalDir := filepath.Join(cfg.StateDir, "maintenance", "proposals")
	if err := os.MkdirAll(proposalDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proposalDir, name), proposal, 0600); err != nil {
		t.Fatal(err)
	}
	if err := recordDraftRef(context.WithValue(context.Background(), recorderKey{}, one), name, proposal, false); err != nil {
		t.Fatal(err)
	}
	if err := one.finish("succeeded", ""); err != nil {
		t.Fatal(err)
	}
	two, err := store.newRun(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := two.finish("skipped", "no_changes"); err != nil {
		t.Fatal(err)
	}
	page, err := ReadRunHistory(cfg, 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 1 || page.NextCursor == "" || page.Runs[0].RunID != two.started.RunID {
		t.Fatalf("unexpected first page: %+v", page)
	}
	older, err := ReadRunHistory(cfg, 10, page.NextCursor, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Runs) != 1 || older.Runs[0].RunID != one.started.RunID || len(older.Runs[0].Drafts) != 1 || older.Runs[0].Drafts[0].Status != "present" {
		t.Fatalf("unexpected continuation: %+v", older)
	}
	if err := store.writeRunFile(one.started.RunID, "started.json", one.started); err == nil {
		t.Fatal("create-once started record was overwritten")
	}
	info, err := os.Stat(filepath.Join(store.dir, one.started.RunID, "started.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("started mode = %o", info.Mode().Perm())
	}
	info, err = os.Stat(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("history directory mode = %o", info.Mode().Perm())
	}
	if err := os.Remove(filepath.Join(proposalDir, name)); err != nil {
		t.Fatal(err)
	}
	page, err = ReadRunHistory(cfg, 10, "", one.started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if page.Runs[0].Drafts[0].Status != "missing" {
		t.Fatalf("missing draft not surfaced: %+v", page.Runs[0].Drafts)
	}
}

func TestHistoryRunIDsSortByFractionalTimeAcrossSessions(t *testing.T) {
	cfg := historyTestConfig(t)
	one, err := newHistoryStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	two, err := newHistoryStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	one.now = func() time.Time { return base.Add(100 * time.Nanosecond) }
	two.now = func() time.Time { return base.Add(200 * time.Nanosecond) }
	r1, err := one.newRun(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := two.newRun(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if !(r1.started.RunID < r2.started.RunID) {
		t.Fatalf("same-second run IDs are not time-sortable: %s >= %s", r1.started.RunID, r2.started.RunID)
	}
	_ = r1.finish("succeeded", "")
	_ = r2.finish("succeeded", "")
}

func TestHistoryFiniteOutcomeTable(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		hint       string
		reason     string
		wantStatus string
		wantReason string
	}{
		{name: "no changes", hint: "skipped", reason: "no_changes", wantStatus: "skipped", wantReason: "no_changes"},
		{name: "no eligible", hint: "skipped", reason: "no_eligible", wantStatus: "skipped", wantReason: "no_eligible"},
		{name: "backoff", hint: "skipped", reason: "backoff", wantStatus: "skipped", wantReason: "backoff"},
		{name: "evidence skip", hint: "skipped", reason: "evidence_skip", wantStatus: "skipped", wantReason: "evidence_skip"},
		{name: "cancel", err: context.Canceled, wantStatus: "failed", wantReason: "cancel"},
		{name: "budget", err: errors.New("inference budget exhausted"), wantStatus: "failed", wantReason: "budget"},
		{name: "capacity", err: errors.New("inference queue full"), wantStatus: "failed", wantReason: "capacity"},
		{name: "provider auth", err: &ProviderFailure{Kind: "auth"}, wantStatus: "failed", wantReason: "auth"},
		{name: "reused artifact is work", wantStatus: "succeeded", wantReason: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &runRecorder{}
			r.hintOutcome, r.hintReason = tt.hint, tt.reason
			status, reason := finiteOutcome(tt.err, r)
			if status != tt.wantStatus || reason != tt.wantReason {
				t.Fatalf("finite outcome = %q/%q, want %q/%q", status, reason, tt.wantStatus, tt.wantReason)
			}
		})
	}
}

func TestHistoryRecoveryMarksOnlyUnfinishedRunInterruptedWithoutDuration(t *testing.T) {
	cfg := historyTestConfig(t)
	store, err := newHistoryStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.newRun(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.event(runEvent{Type: "provider_attempt_started"}); err != nil {
		t.Fatal(err)
	}
	releaseHistoryLock(r.liveLock)
	r.liveLock = nil // Simulate a prior process that no longer owns the run.
	lock, ok, err := acquireHistoryLock(store.dir)
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	defer releaseHistoryLock(lock)
	if err := store.recoverUnfinished(""); err != nil {
		t.Fatal(err)
	}
	page, err := ReadRunHistory(cfg, 1, "", r.started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	c := page.Runs[0].Completion
	if c == nil || c.Outcome != "interrupted" || c.ProviderAttempts != 1 || c.DurationMillis != nil || c.RecoveredAt == "" {
		t.Fatalf("bad interrupted record: %+v", c)
	}
	if err := store.recoverUnfinished(""); err != nil {
		t.Fatal(err)
	}
	page, err = ReadRunHistory(cfg, 1, "", r.started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if page.Runs[0].Completion.RecoveredAt != c.RecoveredAt {
		t.Fatal("completed run was rewritten")
	}
}

func TestHistoryRecoveryPreservesCorruptCompletionAndDoesNotBlockNewRuns(t *testing.T) {
	cfg := historyTestConfig(t)
	store, err := newHistoryStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.newRun(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	releaseHistoryLock(r.liveLock)
	r.liveLock = nil
	completionPath := filepath.Join(store.dir, r.started.RunID, "completion.json")
	bad := []byte(`{"schema_version":1,"outcome":"partial"}`)
	if err := os.WriteFile(completionPath, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.recoverUnfinished(""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(completionPath)
	if err != nil || string(got) != string(bad) {
		t.Fatalf("corrupt immutable completion changed: %q, %v", got, err)
	}
	page, err := ReadRunHistory(cfg, 10, "", r.started.RunID)
	if err != nil || page.Runs[0].HistoryStatus != "corrupt_completion" {
		t.Fatalf("corrupt completion hidden: %+v, %v", page, err)
	}
	if _, err := store.newRun(cfg, ""); err != nil {
		t.Fatalf("corrupt old completion blocked new run: %v", err)
	}
}

func TestHistoryRecoveryRereadsCompletionAfterAcquiringLivenessLock(t *testing.T) {
	cfg := historyTestConfig(t)
	store, err := newHistoryStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.newRun(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	store.beforeRecoveryLock = func(runID string) {
		if runID == r.started.RunID {
			if err := r.finish("skipped", "overlap"); err != nil {
				t.Errorf("finish overlap before recovery lock: %v", err)
			}
		}
	}
	if err := store.recoverUnfinished(""); err != nil {
		t.Fatalf("completed invocation raced recovery: %v", err)
	}
	page, err := ReadRunHistory(cfg, 1, "", r.started.RunID)
	if err != nil || page.Runs[0].Completion == nil || page.Runs[0].Completion.Outcome != "skipped" || page.Runs[0].Completion.ReasonCode != "overlap" {
		t.Fatalf("recovery overwrote a concurrent completion: %+v, %v", page, err)
	}
}

func TestHistoryRecoverySkipsLiveRunBetweenStartPublicationAndFinish(t *testing.T) {
	cfg := historyTestConfig(t)
	store, err := newHistoryStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	write := store.writeRunFileImmutable
	startedPublished := make(chan struct{})
	continueRun := make(chan struct{})
	store.persist = func(runID, relative string, value any) error {
		if err := write(runID, relative, value); err != nil {
			return err
		}
		if relative == "started.json" {
			close(startedPublished)
			<-continueRun
		}
		return nil
	}
	result := make(chan *runRecorder, 1)
	go func() {
		r, _ := store.newRun(cfg, "")
		result <- r
	}()
	<-startedPublished
	if err := store.recoverUnfinished(""); err != nil {
		t.Fatal(err)
	}
	close(continueRun)
	r := <-result
	if r == nil {
		t.Fatal("run start failed")
	}
	page, err := ReadRunHistory(cfg, 1, "", r.started.RunID)
	if err != nil || page.Runs[0].HistoryStatus != "unfinished" {
		t.Fatalf("live overlap was misclassified: %+v, %v", page, err)
	}
	if err := r.finish("skipped", "overlap"); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryReaderSurfacesCorruptionAndRejectsUnsafePaths(t *testing.T) {
	cfg := historyTestConfig(t)
	store, err := newHistoryStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.newRun(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, r.started.RunID, "completion.json"), []byte(`{"outcome":"secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := ReadRunHistory(cfg, 10, "", r.started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if page.Runs[0].HistoryStatus != "corrupt_completion" {
		t.Fatalf("corruption hidden: %+v", page.Runs[0])
	}
	if _, err := ReadRunHistory(cfg, 10, "", "../../etc/passwd"); err == nil {
		t.Fatal("path-like run id accepted")
	}
	if _, err := ReadRunHistory(cfg, 10, "../../etc/passwd", ""); err == nil {
		t.Fatal("path-like cursor accepted")
	}
	linkDir := filepath.Join(cfg.StateDir, "linked-history")
	if err := os.Symlink(store.dir, linkDir); err != nil {
		t.Fatal(err)
	}
	if _, err := (&historyStore{dir: linkDir}).Read(10, "", ""); err == nil {
		t.Fatal("history symlink accepted")
	}
}

func TestHistoryReaderDetectsDeletedEventSequence(t *testing.T) {
	cfg := historyTestConfig(t)
	store, err := newHistoryStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.newRun(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := r.event(runEvent{Type: "phase"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.finish("succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.dir, r.started.RunID, "events", "0001.json")); err != nil {
		t.Fatal(err)
	}
	page, err := ReadRunHistory(cfg, 1, "", r.started.RunID)
	if err != nil || page.Runs[0].HistoryStatus != "corrupt_events" {
		t.Fatalf("event gap appeared clean: %+v, %v", page, err)
	}
}

func TestHistoryReaderIgnoresActualCreateTempCrashFile(t *testing.T) {
	cfg := historyTestConfig(t)
	store, err := newHistoryStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.newRun(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	eventsDir := filepath.Join(store.dir, r.started.RunID, "events")
	if err := os.MkdirAll(eventsDir, 0700); err != nil {
		t.Fatal(err)
	}
	tmp, err := os.CreateTemp(eventsDir, ".history-")
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(tmp.Name())
	if !historyTempRE.MatchString(name) {
		t.Fatalf("real CreateTemp filename not recognized: %q", name)
	}
	if _, err := tmp.Write([]byte("partial json")); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := ReadRunHistory(cfg, 1, "", r.started.RunID)
	if err != nil || page.Runs[0].HistoryStatus != "unfinished" {
		t.Fatalf("temp crash file corrupted run history: %+v, %v", page, err)
	}
	_ = r.finish("succeeded", "")
}

type historyBlockingProvider struct {
	entered chan struct{}
	release chan struct{}
}

func (p *historyBlockingProvider) Propose(ctx context.Context, in Input) (Proposal, aijudgment.Usage, error) {
	close(p.entered)
	select {
	case <-ctx.Done():
		return Proposal{}, aijudgment.Usage{}, ctx.Err()
	case <-p.release:
		return Proposal{}, aijudgment.Usage{}, errors.New("provider failed")
	}
}

func TestHistoryOverlapIsRecordedAndDoesNotDispatchTwice(t *testing.T) {
	provider := &historyBlockingProvider{entered: make(chan struct{}), release: make(chan struct{})}
	w, _, _, _ := setupCatalogOnly(t, 1, 1, "local")
	w.provider = provider
	w.catalog.provider = provider
	first := make(chan error, 1)
	go func() { first <- w.RunOnce(context.Background()) }()
	select {
	case <-provider.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	if err := w.RunOnce(context.Background()); !errors.Is(err, errSweepInProgress) {
		t.Fatalf("overlap error = %v", err)
	}
	close(provider.release)
	<-first
	page, err := ReadRunHistory(w.cfg, 10, "", "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, run := range page.Runs {
		if run.Completion != nil && run.Completion.ReasonCode == "overlap" {
			found = true
		}
	}
	if !found {
		t.Fatalf("overlap invocation not journaled: %+v", page)
	}
}

func TestHistorySecondWorkerDoesNotInterruptActiveSession(t *testing.T) {
	provider := &historyBlockingProvider{entered: make(chan struct{}), release: make(chan struct{})}
	w1, _, cfg, stateDir := setupCatalogOnly(t, 1, 1, "local")
	w1.provider = provider
	w1.catalog.provider = provider
	budget, err := aipolicy.NewBudget(filepath.Join(stateDir, "budget"), cfg.Limits)
	if err != nil {
		t.Fatal(err)
	}
	w2, err := NewWorker(cfg, nil, &fakeProvider{result: func(in Input) Proposal { return catalogOnlyDescriptionProposal(in) }}, budget)
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- w1.RunOnce(context.Background()) }()
	select {
	case <-provider.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first worker did not reach provider")
	}
	if err := w2.RunOnce(context.Background()); !errors.Is(err, errSweepInProgress) {
		t.Fatalf("second worker result = %v", err)
	}
	page, err := ReadRunHistory(cfg, 10, "", "")
	if err != nil {
		t.Fatal(err)
	}
	active, overlap := 0, 0
	for _, run := range page.Runs {
		if run.Completion == nil && run.Started != nil {
			active++
		}
		if run.Completion != nil && run.Completion.ReasonCode == "overlap" {
			overlap++
		}
	}
	if active != 1 || overlap != 1 {
		t.Fatalf("second worker changed active run: active=%d overlap=%d page=%+v", active, overlap, page)
	}
	close(provider.release)
	<-first
}

func TestHistoryStartFailurePreventsProviderDispatch(t *testing.T) {
	w, p, _, _ := setupCatalogOnly(t, 1, 1, "local")
	write := w.history.writeRunFileImmutable
	w.history.persist = func(runID, relative string, value any) error {
		if relative == "started.json" {
			return errors.New("disk full")
		}
		return write(runID, relative, value)
	}
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("expected start persistence failure")
	}
	if p.calls.Load() != 0 {
		t.Fatalf("provider dispatched %d times after start write failure", p.calls.Load())
	}
}

func TestHistoryPostDispatchEventFailureStopsRunAndRetainsStart(t *testing.T) {
	w, p, _, _ := setupCatalogOnly(t, 1, 1, "local")
	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)
	write := w.history.writeRunFileImmutable
	w.history.persist = func(runID, relative string, value any) error {
		if relative == filepath.Join("events", "0003.json") {
			return errors.New("disk full")
		}
		return write(runID, relative, value)
	}
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("expected post-dispatch history failure")
	}
	if p.calls.Load() != 1 {
		t.Fatalf("provider calls = %d", p.calls.Load())
	}
	if !strings.Contains(logs.String(), "maintenance run history write failed; invocation outcome may be incomplete") || strings.Contains(logs.String(), "injected") {
		t.Fatalf("history persistence failure warning was not fixed/safe: %s", logs.String())
	}
	page, err := ReadRunHistory(w.cfg, 10, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 1 || page.Runs[0].Started == nil || page.Runs[0].Completion == nil || page.Runs[0].Completion.Outcome != "failed" || page.Runs[0].Completion.ReasonCode != "state" || len(page.Runs[0].Events) != 2 {
		t.Fatalf("post-dispatch failure record: %+v", page)
	}
}

func TestHistoryPredispatchEventFailureReleasesKnownZeroUsageReservation(t *testing.T) {
	w, p, _, _ := setupCatalogOnly(t, 1, 1, "local")
	write := w.history.writeRunFileImmutable
	w.history.persist = func(runID, relative string, value any) error {
		if relative == filepath.Join("events", "0002.json") { // selection summary is event 1; attempt start is event 2.
			return errors.New("injected pre-dispatch event failure")
		}
		return write(runID, relative, value)
	}
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("expected pre-dispatch history failure")
	}
	if p.calls.Load() != 0 {
		t.Fatalf("provider was dispatched %d times", p.calls.Load())
	}
	raw, err := os.ReadFile(filepath.Join(w.cfg.StateDir, "budget", "budget.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ledger struct {
		Calls   int64            `json:"calls"`
		Tokens  int64            `json:"tokens"`
		Pending map[string]int64 `json:"pending"`
	}
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	if ledger.Calls != 1 || ledger.Tokens != 0 || len(ledger.Pending) != 0 {
		t.Fatalf("pre-dispatch reservation not reconciled at known zero usage: %+v", ledger)
	}
}

func TestRunOnceHistoryLockFailureLogsFixedWarningAndSkipsProvider(t *testing.T) {
	w, p, _, _ := setupCatalogOnly(t, 1, 1, "local")
	lockPath := filepath.Join(w.history.dir, "writer.lock")
	if err := os.Symlink(filepath.Join(w.history.dir, "missing-target"), lockPath); err != nil {
		t.Fatal(err)
	}
	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("expected lock open failure")
	}
	if p.calls.Load() != 0 {
		t.Fatalf("provider dispatched despite history lock failure: %d", p.calls.Load())
	}
	if !strings.Contains(logs.String(), "maintenance run history lock unavailable; provider dispatch skipped") {
		t.Fatalf("fixed lock failure warning missing: %s", logs.String())
	}
}

func TestHistoryRecordsCreatedThenExplicitlyReusedDraftAcrossRuns(t *testing.T) {
	cfg := historyTestConfig(t)
	store, err := newHistoryStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{cfg: cfg, dir: filepath.Join(cfg.StateDir, "maintenance")}
	artifact := privateProposal{SchemaVersion: 1, ProviderProtocol: "openai-compatible-chat", Model: "local"}
	first, err := store.newRun(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.saveProposalTracked(context.WithValue(context.Background(), recorderKey{}, first), artifact); err != nil {
		t.Fatal(err)
	}
	if err := first.finish("succeeded", ""); err != nil {
		t.Fatal(err)
	}
	second, err := store.newRun(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.saveProposalTracked(context.WithValue(context.Background(), recorderKey{}, second), artifact); err != nil {
		t.Fatal(err)
	}
	if err := second.finish("succeeded", ""); err != nil {
		t.Fatal(err)
	}
	page, err := ReadRunHistory(cfg, 10, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 2 || page.Runs[0].Drafts[0].Status != "present" || page.Runs[1].Drafts[0].Status != "present" || page.Runs[0].Drafts[0].SHA256 != page.Runs[1].Drafts[0].SHA256 {
		t.Fatalf("draft provenance not explicit: %+v", page)
	}
	if page.Runs[0].Completion == nil || page.Runs[0].Completion.Outcome != "succeeded" || page.Runs[0].Completion.ReasonCode != "" || page.Runs[0].Completion.ProviderAttempts != 0 {
		t.Fatalf("reused receipt was mislabeled as no changes: %+v", page.Runs[0].Completion)
	}
}

func TestHistoryDraftIntentReconcilesFileLandedBeforeReferenceFailure(t *testing.T) {
	cfg := historyTestConfig(t)
	store, err := newHistoryStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{cfg: cfg, dir: filepath.Join(cfg.StateDir, "maintenance")}
	r, err := store.newRun(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	write := store.writeRunFileImmutable
	store.persist = func(runID, relative string, value any) error {
		if relative == filepath.Join("events", "0002.json") {
			return errors.New("injected reference write failure")
		}
		return write(runID, relative, value)
	}
	ctx := context.WithValue(context.Background(), recorderKey{}, r)
	if _, err := w.saveProposalTracked(ctx, privateProposal{SchemaVersion: 1, Model: "m"}); err == nil {
		t.Fatal("expected reference write failure")
	}
	if err := r.finish("failed", "state"); err != nil {
		t.Fatal(err)
	}
	store.persist = nil
	page, err := ReadRunHistory(cfg, 1, "", r.started.RunID)
	if err != nil || len(page.Runs[0].Drafts) != 1 || page.Runs[0].Drafts[0].Status != "unconfirmed" {
		t.Fatalf("landed draft intent was not reconciled: %+v, %v", page, err)
	}
}
