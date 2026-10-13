package aimaintenance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"golang.org/x/sys/unix"
)

const (
	historySchema    = 1
	maxHistoryFile   = 64 << 10
	maxHistoryRuns   = 1_000_000
	maxHistoryEvents = 256
)

var runIDRE = regexp.MustCompile(`^[0-9]{8}T[0-9]{15}Z-[a-f0-9]{32}-[0-9]{8}$`)
var historyTempRE = regexp.MustCompile(`^\.history-[0-9]{1,10}$`)

type runStarted struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	SessionID     string `json:"session_id"`
	Sequence      uint64 `json:"sequence"`
	StartedAt     string `json:"started_at"`
	Scope         string `json:"scope"`
	ProjectPolicy string `json:"project_policy"`
	Publication   string `json:"publication"`
	Model         string `json:"model,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	CatalogHash   string `json:"catalog_hash,omitempty"`
}

type runEvent struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	SessionID     string `json:"session_id"`
	Sequence      int    `json:"sequence"`
	At            string `json:"at"`
	Type          string `json:"type"`
	ReasonCode    string `json:"reason_code,omitempty"`
	Count         int    `json:"count,omitempty"`
	CountSkipped  int    `json:"count_skipped,omitempty"`
	CountFacts    int    `json:"count_facts,omitempty"`
	InputTokens   int64  `json:"input_tokens,omitempty"`
	OutputTokens  int64  `json:"output_tokens,omitempty"`
	UsageKnown    bool   `json:"usage_known,omitempty"`
	Filename      string `json:"filename,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	DraftStatus   string `json:"draft_status,omitempty"`
}

type runCompleted struct {
	SchemaVersion     int    `json:"schema_version"`
	CompletedAt       string `json:"completed_at"`
	DurationMillis    *int64 `json:"duration_ms,omitempty"`
	Outcome           string `json:"outcome"`
	ReasonCode        string `json:"reason_code,omitempty"`
	RunID             string `json:"run_id"`
	SessionID         string `json:"session_id"`
	RecoveredAt       string `json:"recovered_at,omitempty"`
	FactsConsidered   int    `json:"facts_considered,omitempty"`
	ProjectsQueued    int    `json:"projects_queued,omitempty"`
	ProjectsCompleted int    `json:"projects_completed,omitempty"`
	ProjectsSkipped   int    `json:"projects_skipped,omitempty"`
	CountsKnown       bool   `json:"counts_known,omitempty"`
	ProviderAttempts  int    `json:"provider_attempts"`
	EventCount        int    `json:"event_count"`
	EventCountKnown   bool   `json:"event_count_known,omitempty"`
	InputTokens       int64  `json:"input_tokens,omitempty"`
	OutputTokens      int64  `json:"output_tokens,omitempty"`
	UsageKnown        bool   `json:"usage_known,omitempty"`
}

type runRecorder struct {
	store             *historyStore
	liveLock          *os.File
	started           runStarted
	startedAt         time.Time
	events            atomic.Int32
	eventMu           sync.Mutex
	attempts          atomic.Int32
	inputTok          atomic.Int64
	outputTok         atomic.Int64
	usageKnown        atomic.Bool
	countsKnown       atomic.Bool
	failed            atomic.Bool
	inputFacts        atomic.Int32
	projectsQueued    atomic.Int32
	projectsCompleted atomic.Int32
	projectsSkipped   atomic.Int32
	hintOutcome       string
	hintReason        string
}

type recorderKey struct{}

func recorderFrom(ctx context.Context) *runRecorder {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(recorderKey{}).(*runRecorder)
	return r
}

type historyStore struct {
	dir                 string
	session             string
	seq                 atomic.Uint64
	persist             func(runID, relative string, value any) error
	now                 func() time.Time
	beforeRecoveryLock  func(runID string)
	afterCompletionRead func(runID string)
}

func newHistoryStore(stateDir string) (*historyStore, error) {
	dir := filepath.Join(stateDir, "maintenance-runs")
	if err := aipolicy.SecureDir(dir); err != nil {
		return nil, errors.New("cannot prepare maintenance run history")
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, errors.New("cannot initialize maintenance run history")
	}
	return &historyStore{dir: dir, session: hex.EncodeToString(b[:])}, nil
}

func (s *historyStore) newRun(cfg aipolicy.Config, catalogHash string) (*runRecorder, error) {
	seq := s.seq.Add(1)
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	startedAt := now().UTC()
	sortableTimestamp := strings.ReplaceAll(startedAt.Format("20060102T150405.000000000Z"), ".", "")
	start := runStarted{SchemaVersion: historySchema, SessionID: s.session, Sequence: seq, RunID: fmt.Sprintf("%s-%s-%08d", sortableTimestamp, s.session, seq), StartedAt: startedAt.Format(time.RFC3339Nano), Scope: cfg.Maintenance.Scope, ProjectPolicy: cfg.Maintenance.ProjectPolicy, Publication: cfg.Maintenance.CatalogPublish, CatalogHash: catalogHash}
	if p, ok := cfg.Profiles[cfg.Maintenance.Profile]; ok {
		start.Model, start.Protocol = p.Model, p.Protocol
	}
	r := &runRecorder{store: s, started: start, startedAt: startedAt}
	runDir := filepath.Join(s.dir, start.RunID)
	if err := aipolicy.SecureDir(runDir); err != nil {
		return nil, errors.New("cannot prepare maintenance run record")
	}
	liveLock, err := acquireRunLivenessLock(runDir, true)
	if err != nil {
		return nil, errors.New("cannot protect maintenance run record")
	}
	r.liveLock = liveLock
	if err := s.writeRunFile(start.RunID, "started.json", start); err != nil {
		releaseHistoryLock(r.liveLock)
		return nil, errors.New("cannot persist maintenance run start")
	}
	return r, nil
}

func (r *runRecorder) event(ev runEvent) error {
	if r == nil {
		return nil
	}
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	n := int(r.events.Load()) + 1
	if n > maxHistoryEvents {
		r.failed.Store(true)
		return errors.New("maintenance history event limit reached")
	}
	ev.SchemaVersion, ev.RunID, ev.SessionID, ev.Sequence, ev.At = historySchema, r.started.RunID, r.started.SessionID, n, time.Now().UTC().Format(time.RFC3339Nano)
	if err := r.store.writeRunFile(r.started.RunID, filepath.Join("events", fmt.Sprintf("%04d.json", n)), ev); err != nil {
		r.failed.Store(true)
		return errors.New("cannot persist maintenance run event")
	}
	r.events.Add(1)
	return nil
}

func (r *runRecorder) finish(outcome, reason string) error {
	if r == nil {
		return nil
	}
	end := time.Now().UTC()
	duration := end.Sub(r.startedAt).Milliseconds()
	c := runCompleted{SchemaVersion: historySchema, RunID: r.started.RunID, SessionID: r.started.SessionID, CompletedAt: end.Format(time.RFC3339Nano), DurationMillis: &duration, Outcome: outcome, ReasonCode: reason, ProviderAttempts: int(r.attempts.Load()), EventCount: int(r.events.Load()), EventCountKnown: !r.failed.Load(), FactsConsidered: int(r.inputFacts.Load()), ProjectsQueued: int(r.projectsQueued.Load()), ProjectsCompleted: int(r.projectsCompleted.Load()), ProjectsSkipped: int(r.projectsSkipped.Load()), CountsKnown: r.countsKnown.Load(), InputTokens: r.inputTok.Load(), OutputTokens: r.outputTok.Load(), UsageKnown: r.usageKnown.Load()}
	if err := r.store.writeRunFile(r.started.RunID, "completion.json", c); err != nil {
		releaseHistoryLock(r.liveLock)
		r.liveLock = nil
		return errors.New("cannot persist maintenance run completion")
	}
	releaseHistoryLock(r.liveLock)
	r.liveLock = nil
	return nil
}

// acquireRunLivenessLock is held from before started.json publication through
// terminal persistence. Recovery only interrupts a started run after proving
// that this lock is no longer held by a live invocation.
func acquireRunLivenessLock(runDir string, create bool) (*os.File, error) {
	flags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if create {
		flags |= unix.O_CREAT | unix.O_EXCL
	}
	fd, err := unix.Open(filepath.Join(runDir, "writer.lock"), flags, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(runDir, "writer.lock"))
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func (s *historyStore) runDir(runID string) (string, error) {
	if !runIDRE.MatchString(runID) {
		return "", errors.New("invalid run id")
	}
	dir := filepath.Join(s.dir, runID)
	if err := aipolicy.SecureDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func (s *historyStore) writeRunFile(runID, relative string, value any) error {
	if s.persist != nil {
		return s.persist(runID, relative, value)
	}
	return s.writeRunFileImmutable(runID, relative, value)
}

func (s *historyStore) writeRunFileImmutable(runID, relative string, value any) error {
	if !runIDRE.MatchString(runID) || filepath.IsAbs(relative) || strings.Contains(relative, "..") {
		return errors.New("invalid history path")
	}
	dir, err := s.runDir(runID)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, relative)
	parent := filepath.Dir(path)
	if err := aipolicy.SecureDir(parent); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maxHistoryFile {
		return errors.New("invalid history record")
	}
	tmp, err := os.CreateTemp(parent, ".history-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err := os.Remove(tmpName); err == nil {
			_ = syncDir(parent)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Link(tmpName, path); err != nil {
		return err
	} // link is an atomic create-once publish.
	if err := syncDir(parent); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	return syncDir(s.dir)
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func acquireHistoryLock(dir string) (*os.File, bool, error) {
	path := filepath.Join(dir, "writer.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, false, errors.New("cannot open maintenance history lock")
	}
	f := os.NewFile(uintptr(fd), path)
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, false, errors.New("cannot protect maintenance history lock")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, errors.New("cannot lock maintenance run history")
	}
	return f, true, nil
}

func releaseHistoryLock(f *os.File) {
	if f != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}
}

func (s *historyStore) recoverUnfinished(exceptRunID string) error {
	names, err := boundedDirNames(s.dir, maxHistoryRuns)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == exceptRunID {
			continue
		}
		info, statErr := os.Lstat(filepath.Join(s.dir, name))
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !runIDRE.MatchString(name) {
			continue
		}
		run, err := readHistoryRun(s.dir, name)
		if err != nil {
			continue
		}
		if run.HistoryStatus == "corrupt_completion" {
			continue
		}
		if run.HistoryStatus == "unfinished" && run.Completion == nil && run.Started != nil {
			if s.beforeRecoveryLock != nil {
				s.beforeRecoveryLock(name)
			}
			runDir := filepath.Join(s.dir, name)
			liveLock, lockErr := acquireRunLivenessLock(runDir, false)
			if os.IsNotExist(lockErr) { // Older revision records predate the per-run lock.
				liveLock, lockErr = acquireRunLivenessLock(runDir, true)
			}
			if lockErr != nil {
				if errors.Is(lockErr, unix.EWOULDBLOCK) || errors.Is(lockErr, unix.EAGAIN) {
					continue
				}
				return errors.New("cannot inspect unfinished maintenance run")
			}
			// The invocation may have completed after the first read but before
			// this lock was acquired. Re-read while holding liveness ownership so
			// immutable terminal records are never rewritten or treated as fatal.
			run, err = readHistoryRun(s.dir, name)
			if err != nil || run.HistoryStatus != "unfinished" || run.Completion != nil || run.Started == nil {
				releaseHistoryLock(liveLock)
				continue
			}
			recoveredAt := time.Now().UTC().Format(time.RFC3339Nano)
			completion := recoveredCompletion(run, recoveredAt)
			writeErr := s.writeRunFile(name, "completion.json", completion)
			releaseHistoryLock(liveLock)
			if writeErr != nil {
				return errors.New("cannot recover unfinished maintenance run")
			}
		}
	}
	return nil
}

type DraftReference struct {
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Status   string `json:"status"`
}
type HistoryRun struct {
	RunID         string           `json:"run_id"`
	Started       *runStarted      `json:"started,omitempty"`
	Events        []runEvent       `json:"events"`
	Completion    *runCompleted    `json:"completion,omitempty"`
	Drafts        []DraftReference `json:"drafts"`
	HistoryStatus string           `json:"history_status,omitempty"`
}
type HistoryPage struct {
	Runs         []HistoryRun `json:"runs"`
	NextCursor   string       `json:"next_cursor,omitempty"`
	Availability string       `json:"availability"`
}

// ReadRunHistory is a read-only bounded history query. It never creates the history directory.
func ReadRunHistory(cfg aipolicy.Config, limit int, cursor, runID string) (HistoryPage, error) {
	if strings.TrimSpace(cfg.StateDir) == "" {
		return HistoryPage{Runs: []HistoryRun{}, Availability: "unavailable"}, nil
	}
	return (&historyStore{dir: filepath.Join(cfg.StateDir, "maintenance-runs")}).Read(limit, cursor, runID)
}

func readHistoryRun(root, id string) (HistoryRun, error) {
	return readHistoryRunWithHook(root, id, nil)
}

func (s *historyStore) readRun(id string) (HistoryRun, error) {
	return readHistoryRunWithHook(s.dir, id, s.afterCompletionRead)
}

func readHistoryRunWithHook(root, id string, afterCompletionRead func(runID string)) (HistoryRun, error) {
	out := HistoryRun{RunID: id, Events: []runEvent{}, Drafts: []DraftReference{}}
	if !runIDRE.MatchString(id) {
		return out, errors.New("invalid run id")
	}
	dir := filepath.Join(root, id)
	startedRaw, err := readRegularBounded(filepath.Join(dir, "started.json"), maxHistoryFile)
	if err != nil {
		out.HistoryStatus = "missing_started"
		return out, err
	}
	var start runStarted
	parts := strings.Split(id, "-")
	if strictJSON(startedRaw, &start) != nil || start.SchemaVersion != historySchema || start.RunID != id || len(parts) != 3 || start.SessionID != parts[1] || start.Sequence == 0 || fmt.Sprintf("%08d", start.Sequence) != parts[2] || start.Scope == "" || start.ProjectPolicy == "" || start.Publication == "" {
		out.HistoryStatus = "corrupt_started"
		return out, errors.New("corrupt started record")
	}
	out.Started = &start
	// Terminal publication follows all event publication. Read its existence
	// first so a concurrent final event cannot race an older event listing and
	// make a complete immutable run look corrupt.
	completionRaw, completionErr := readRegularBounded(filepath.Join(dir, "completion.json"), maxHistoryFile)
	if afterCompletionRead != nil {
		afterCompletionRead(id)
	}
	ents, err := boundedDirNames(filepath.Join(dir, "events"), maxHistoryEvents+1)
	if err != nil && !os.IsNotExist(err) {
		out.HistoryStatus = "corrupt_events"
		return out, err
	}
	if len(ents) > maxHistoryEvents {
		out.HistoryStatus = "corrupt_events"
		ents = ents[:maxHistoryEvents]
	}
	seenSeq := map[int]bool{}
	for _, name := range ents {
		if historyTempRE.MatchString(name) { // interrupted temp writes are not records.
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			out.HistoryStatus = "corrupt_events"
			continue
		}
		raw, er := readRegularBounded(filepath.Join(dir, "events", name), maxHistoryFile)
		var ev runEvent
		if er != nil || strictJSON(raw, &ev) != nil || ev.SchemaVersion != historySchema || ev.RunID != id || ev.SessionID != start.SessionID || ev.Sequence < 1 || ev.Sequence > maxHistoryEvents || name != fmt.Sprintf("%04d.json", ev.Sequence) || seenSeq[ev.Sequence] || !validHistoryEvent(ev) {
			out.HistoryStatus = "corrupt_events"
			continue
		}
		seenSeq[ev.Sequence] = true
		out.Events = append(out.Events, ev)
	}
	sort.Slice(out.Events, func(i, j int) bool { return out.Events[i].Sequence < out.Events[j].Sequence })
	if completionErr == nil {
		var c runCompleted
		if strictJSON(completionRaw, &c) != nil || c.SchemaVersion != historySchema || c.RunID != id || c.SessionID != start.SessionID || c.CompletedAt == "" || c.ProviderAttempts < 0 || (c.EventCountKnown && c.EventCount != len(out.Events)) || c.FactsConsidered < 0 || c.ProjectsQueued < 0 || c.ProjectsCompleted < 0 || c.ProjectsSkipped < 0 || c.InputTokens < 0 || c.OutputTokens < 0 || (c.DurationMillis != nil && *c.DurationMillis < 0) || !validHistoryOutcome(c.Outcome, c.ReasonCode) {
			out.HistoryStatus = "corrupt_completion"
		} else {
			out.Completion = &c
		}
	} else if !os.IsNotExist(completionErr) {
		out.HistoryStatus = "corrupt_completion"
	} else if out.HistoryStatus == "" {
		out.HistoryStatus = "unfinished"
	}
	for i, ev := range out.Events {
		if ev.Sequence != i+1 {
			out.HistoryStatus = "corrupt_events"
		}
	}
	for _, ev := range out.Events {
		if ev.Type == "draft_intent" || ev.Type == "draft_saved" || ev.Type == "draft_reused" {
			status := draftRefStatus(root, ev)
			if ev.Type == "draft_intent" && status == "present" {
				status = "unconfirmed"
			}
			ref := DraftReference{Filename: ev.Filename, SHA256: ev.SHA256, Status: status}
			found := false
			for i := range out.Drafts {
				if out.Drafts[i].Filename == ref.Filename && out.Drafts[i].SHA256 == ref.SHA256 {
					out.Drafts[i] = ref
					found = true
					break
				}
			}
			if !found {
				out.Drafts = append(out.Drafts, ref)
			}
		}
	}
	return out, nil
}

func (s *historyStore) Read(limit int, cursor, runID string) (HistoryPage, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		return HistoryPage{}, errors.New("history limit exceeds 100")
	}
	page := HistoryPage{Runs: []HistoryRun{}, Availability: "available"}
	if runID != "" && !runIDRE.MatchString(runID) {
		return HistoryPage{}, errors.New("invalid run id")
	}
	if err := validateHistoryDir(s.dir); err != nil {
		if os.IsNotExist(err) {
			if runID != "" {
				page.Runs = append(page.Runs, HistoryRun{RunID: runID, Events: []runEvent{}, Drafts: []DraftReference{}, HistoryStatus: "missing_run"})
				return page, nil
			}
			page.Availability = "missing"
			return page, nil
		}
		return HistoryPage{}, errors.New("maintenance run history unavailable")
	}
	if runID != "" {
		run, err := s.readRun(runID)
		if err != nil {
			if run.HistoryStatus == "" {
				run.HistoryStatus = "missing_run"
			}
			page.Runs = append(page.Runs, run)
			return page, nil
		}
		page.Runs = append(page.Runs, run)
		return page, nil
	}
	names, err := boundedDirNames(s.dir, maxHistoryRuns+1)
	if os.IsNotExist(err) {
		page.Availability = "missing"
		return page, nil
	}
	if err != nil {
		return HistoryPage{}, errors.New("maintenance run history unavailable")
	}
	if len(names) > maxHistoryRuns {
		return HistoryPage{}, errors.New("maintenance run history exceeds read bound")
	}
	ids := make([]string, 0, len(names))
	for _, name := range names {
		i, er := os.Lstat(filepath.Join(s.dir, name))
		if er == nil && i.IsDir() && i.Mode()&os.ModeSymlink == 0 && runIDRE.MatchString(name) {
			ids = append(ids, name)
		}
	}
	if len(ids) > maxHistoryRuns {
		return HistoryPage{}, errors.New("maintenance run history exceeds read bound")
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	if cursor != "" {
		if !runIDRE.MatchString(cursor) {
			return HistoryPage{}, errors.New("invalid history cursor")
		}
		filtered := ids[:0]
		for _, id := range ids {
			if id < cursor {
				filtered = append(filtered, id)
			}
		}
		ids = filtered
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	for _, id := range ids {
		r, er := s.readRun(id)
		if er != nil { // Keep the row visible so corruption is not silently omitted.
			if r.HistoryStatus == "" {
				r.HistoryStatus = "corrupt"
			}
			page.Runs = append(page.Runs, r)
			continue
		}
		page.Runs = append(page.Runs, r)
	}
	if more && len(ids) > 0 {
		page.NextCursor = ids[len(ids)-1]
	}
	return page, nil
}

func readRegularBounded(path string, max int64) ([]byte, error) {
	if err := validateHistoryDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || info.Size() > max {
		return nil, errors.New("unsafe or oversized history file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(raw)) > max {
		return nil, errors.New("invalid history file")
	}
	return raw, nil
}

func strictJSON(raw []byte, dst any) error {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var x any
	if d.Decode(&x) != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

func validHistoryOutcome(outcome, reason string) bool {
	return map[string]bool{"succeeded": true, "failed": true, "skipped": true, "interrupted": true}[outcome] && (reason == "" || map[string]bool{"auth": true, "rate_limit": true, "http": true, "transport": true, "timeout": true, "cancel": true, "invalid_request": true, "invalid_response": true, "catalog": true, "budget": true, "state": true, "source_change": true, "capacity": true, "overlap": true, "unknown_failure": true, "interrupted": true, "no_changes": true, "no_eligible": true, "evidence_skip": true, "backoff": true}[reason])
}
func validHistoryEvent(e runEvent) bool {
	if !map[string]bool{"provider_attempt_started": true, "provider_attempt_finished": true, "selection_summary": true, "projects_completed": true, "draft_intent": true, "draft_saved": true, "draft_reused": true, "phase": true}[e.Type] {
		return false
	}
	if e.ReasonCode != "" && !validHistoryOutcome("failed", e.ReasonCode) {
		return false
	}
	if e.Count < 0 || e.CountSkipped < 0 || e.CountFacts < 0 || e.InputTokens < 0 || e.OutputTokens < 0 {
		return false
	}
	if e.Type == "selection_summary" {
		return e.Count >= 0 && e.CountSkipped >= 0 && e.CountFacts >= 0 && e.Filename == "" && e.SHA256 == ""
	}
	if e.Type == "projects_completed" {
		return e.Count >= 0 && e.CountSkipped == 0 && e.CountFacts == 0 && e.Filename == "" && e.SHA256 == ""
	}
	if e.Type == "draft_intent" || e.Type == "draft_saved" || e.Type == "draft_reused" {
		if !(filepath.Base(e.Filename) == e.Filename && strings.HasPrefix(e.Filename, "proposal-") && strings.HasSuffix(e.Filename, ".json") && regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(e.SHA256)) {
			return false
		}
		if e.Type == "draft_intent" {
			return e.DraftStatus == ""
		}
		return (e.Type == "draft_saved" && e.DraftStatus == "created") || (e.Type == "draft_reused" && e.DraftStatus == "reused")
	}
	return e.Filename == "" && e.SHA256 == ""
}
func draftRefStatus(root string, e runEvent) string {
	if !validHistoryEvent(e) {
		return "corrupt_reference"
	}
	path := filepath.Join(filepath.Dir(root), "maintenance", "proposals", e.Filename)
	raw, err := readRegularBounded(path, MaxResponseBytes*4)
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil {
		return "corrupt"
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != e.SHA256 {
		return "corrupt"
	}
	return "present"
}

func boundedDirNames(path string, max int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	names := make([]string, 0)
	for {
		batch, e := f.Readdirnames(256)
		names = append(names, batch...)
		if len(names) > max {
			return names, errors.New("directory exceeds read bound")
		}
		if e == io.EOF {
			return names, nil
		}
		if e != nil {
			return nil, e
		}
	}
}

func validateHistoryDir(path string) error {
	full, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(full, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, e := os.Lstat(current)
		if e != nil {
			return e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe history directory")
		}
	}
	info, err := os.Lstat(full)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("maintenance history directory is not private")
	}
	return nil
}

func countProviderAttempts(events []runEvent) int {
	n := 0
	for _, e := range events {
		if e.Type == "provider_attempt_started" {
			n++
		}
	}
	return n
}

func recoveredCompletion(run HistoryRun, recoveredAt string) runCompleted {
	c := runCompleted{SchemaVersion: historySchema, RunID: run.Started.RunID, SessionID: run.Started.SessionID, CompletedAt: recoveredAt, RecoveredAt: recoveredAt, Outcome: "interrupted", ReasonCode: "interrupted", ProviderAttempts: countProviderAttempts(run.Events), EventCount: len(run.Events), EventCountKnown: true}
	for _, ev := range run.Events {
		switch ev.Type {
		case "selection_summary":
			c.CountsKnown = true
			c.FactsConsidered = ev.CountFacts
			c.ProjectsQueued = ev.Count
			c.ProjectsSkipped = ev.CountSkipped
		case "projects_completed":
			c.ProjectsCompleted = ev.Count
		case "provider_attempt_finished":
			if ev.UsageKnown {
				c.UsageKnown = true
				c.InputTokens += ev.InputTokens
				c.OutputTokens += ev.OutputTokens
			}
		}
	}
	return c
}

func recordDraftRef(ctx context.Context, filename string, raw []byte, reused bool) error {
	r := recorderFrom(ctx)
	if r == nil {
		return nil
	}
	sum := sha256.Sum256(raw)
	typ, status := "draft_saved", "created"
	if reused {
		typ, status = "draft_reused", "reused"
	}
	return r.event(runEvent{Type: typ, Filename: filepath.Base(filename), SHA256: hex.EncodeToString(sum[:]), DraftStatus: status})
}

func recordDraftIntent(ctx context.Context, filename string, raw []byte) error {
	r := recorderFrom(ctx)
	if r == nil {
		return nil
	}
	sum := sha256.Sum256(raw)
	return r.event(runEvent{Type: "draft_intent", Filename: filepath.Base(filename), SHA256: hex.EncodeToString(sum[:])})
}

func proposalBytes(p privateProposal) []byte { raw, _ := json.Marshal(p); return raw }

func finiteRunReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancel"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var pe *ProviderFailure
	if errors.As(err, &pe) {
		switch pe.Kind {
		case "auth", "rate_limit", "http", "transport", "timeout", "cancel", "invalid_request", "invalid_response":
			return pe.Kind
		}
	}
	switch {
	case errors.Is(err, errCatalogSourcesChanged):
		return "source_change"
	case strings.Contains(err.Error(), "catalog sources changed"):
		return "source_change"
	case strings.Contains(err.Error(), "proposal capacity") || strings.Contains(err.Error(), "proposal retention limit"):
		return "capacity"
	case strings.Contains(err.Error(), "inference queue full"):
		return "capacity"
	case strings.Contains(err.Error(), "budget"):
		return "budget"
	case strings.Contains(err.Error(), "capacity"):
		return "capacity"
	case strings.Contains(err.Error(), "catalog"):
		return "catalog"
	case strings.Contains(err.Error(), "state") || strings.Contains(err.Error(), "history"):
		return "state"
	default:
		return "unknown_failure"
	}
}

func finiteOutcome(err error, recorder *runRecorder) (string, string) {
	if recorder != nil && recorder.failed.Load() {
		return "failed", "state"
	}
	if err == nil {
		if recorder != nil && recorder.hintOutcome != "" {
			return recorder.hintOutcome, recorder.hintReason
		}
		return "succeeded", ""
	}
	if errors.Is(err, errSweepInProgress) {
		return "skipped", "overlap"
	}
	return "failed", finiteRunReason(err)
}

func (r *runRecorder) hint(outcome, reason string) {
	if r != nil {
		r.hintOutcome = outcome
		r.hintReason = reason
	}
}
