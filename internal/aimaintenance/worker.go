package aimaintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/Dzarlax-AI/personal-memory/internal/factgrouping"
	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

const (
	maxTrackedFacts  = 40000
	maxStateBytes    = 16 << 20
	maxProposalFiles = 100
)

type factRecord struct {
	Fingerprint string             `json:"fingerprint"`
	Group       factgrouping.Group `json:"group"`
}
type workerState struct {
	SchemaVersion        int                   `json:"schema_version"`
	BindingHash          string                `json:"binding_hash,omitempty"`
	EvidenceInferenceKey string                `json:"evidence_inference_key,omitempty"`
	InProgress           bool                  `json:"in_progress"`
	Offset               any                   `json:"offset,omitempty"`
	PendingPage          bool                  `json:"pending_page"`
	PageStartOffset      any                   `json:"page_start_offset,omitempty"`
	ProcessedIDs         []string              `json:"processed_ids,omitempty"`
	Seen                 map[string]factRecord `json:"seen"`
	Previous             map[string]factRecord `json:"previous"`
	LastSweepDeleted     int                   `json:"last_sweep_deleted"`
	LastDeletedIDs       []string              `json:"last_deleted_ids"`
	LatestProposal       string                `json:"latest_proposal,omitempty"`
	FailureStreak        int                   `json:"failure_streak"`
	NextAllowedAt        string                `json:"next_allowed_at,omitempty"`
	LastOutcome          string                `json:"last_outcome,omitempty"`
	LastRunAt            string                `json:"last_run_at,omitempty"`
}
type privateProposal struct {
	SchemaVersion        int                        `json:"schema_version"`
	CreatedAt            string                     `json:"created_at"`
	ProviderProtocol     string                     `json:"provider_protocol"`
	Model                string                     `json:"model"`
	BaseCatalogHash      string                     `json:"base_catalog_hash"`
	BindingHash          string                     `json:"binding_hash"`
	Proposal             Proposal                   `json:"proposal"`
	Dependencies         []privateDependency        `json:"dependencies"`
	CatalogDependencies  []privateCatalogDependency `json:"catalog_dependencies,omitempty"`
	Manifest             factgrouping.Manifest      `json:"grouping_manifest"`
	EvidenceInferenceKey string                     `json:"evidence_inference_key,omitempty"`
}

type privateCatalogDependency struct {
	Alias      string `json:"alias"`
	ProjectID  string `json:"project_id,omitempty"`
	Owner      string `json:"owner,omitempty"`
	ProjectKey string `json:"project_key,omitempty"`
	Namespace  string `json:"namespace"`
	Tag        string `json:"tag"`
	Kind       string `json:"kind"`
	Digest     string `json:"digest"`
}

// privateDependency resolves model evidence aliases to the exact local fact
// version reviewed by the operator. It is never included in provider input.
type privateDependency struct {
	Alias       string             `json:"alias"`
	PointID     string             `json:"point_id"`
	Fingerprint string             `json:"fingerprint"`
	Namespace   string             `json:"namespace"`
	Text        string             `json:"text"`
	Before      factgrouping.Group `json:"before_group"`
}

type Worker struct {
	cfg      aipolicy.Config
	store    *qdrant.Client
	provider Provider
	budget   *aipolicy.Budget
	dir      string
	interval time.Duration
	runMu    sync.Mutex
	stateMu  sync.Mutex
	started  bool
	cancel   context.CancelFunc
	done     chan struct{}
}

var errSweepInProgress = errors.New("maintenance sweep already running")

func NewWorker(cfg aipolicy.Config, store *qdrant.Client, provider Provider, budget *aipolicy.Budget) (*Worker, error) {
	cfg.Defaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Maintenance.Enabled {
		return nil, errors.New("maintenance worker is disabled")
	}
	if store == nil || provider == nil || budget == nil {
		return nil, errors.New("maintenance store, provider, and budget are required")
	}
	dir := filepath.Join(cfg.StateDir, "maintenance")
	if err := aipolicy.SecureDir(dir); err != nil {
		return nil, errors.New("cannot prepare private maintenance state")
	}
	return &Worker{cfg: cfg, store: store, provider: provider, budget: budget, dir: dir, interval: time.Duration(cfg.Maintenance.IntervalSeconds) * time.Second, done: make(chan struct{})}, nil
}

// Start schedules the first bounded sweep after one full interval. It never
// performs inference during startup.
func (w *Worker) Start(parent context.Context) error {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.started {
		return errors.New("maintenance worker already started")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	w.started = true
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = w.RunOnce(ctx)
			}
		}
	}()
	return nil
}

func (w *Worker) Stop() {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.cancel != nil {
		w.cancel()
	}
}
func (w *Worker) Wait(ctx context.Context) error {
	w.stateMu.Lock()
	started := w.started
	done := w.done
	w.stateMu.Unlock()
	if !started {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RunOnce processes at most one Qdrant page. Cursor state advances only after
// any generated proposal and the updated private state are atomically saved.
func (w *Worker) RunOnce(ctx context.Context) error {
	if !w.runMu.TryLock() {
		return errSweepInProgress
	}
	defer w.runMu.Unlock()
	state, err := w.loadState()
	if err != nil {
		return err
	}
	if state.NextAllowedAt != "" {
		if at, e := time.Parse(time.RFC3339Nano, state.NextAllowedAt); e == nil && time.Now().Before(at) {
			return nil
		}
	}
	err = w.runOnce(ctx)
	if errors.Is(err, errSweepInProgress) {
		return err
	}
	if err != nil {
		w.recordOutcome("failed")
		return err
	}
	w.recordOutcome("succeeded")
	return nil
}

func (w *Worker) runOnce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cat, catHash, err := contextcatalog.LoadActive(w.cfg.CatalogDir)
	if err != nil {
		return errors.New("active context catalog unavailable")
	}
	state, err := w.loadState()
	if err != nil {
		return err
	}
	bindingHash, err := w.bindingHash(catHash)
	if err != nil {
		return err
	}
	if state.BindingHash != bindingHash {
		state.BindingHash = bindingHash
		state.InProgress = false
		state.Offset = nil
		state.PendingPage = false
		state.PageStartOffset = nil
		state.ProcessedIDs = nil
		state.Seen = map[string]factRecord{}
		state.Previous = map[string]factRecord{}
		state.LastSweepDeleted = 0
		state.LastDeletedIDs = nil
	}
	if !state.InProgress {
		state.InProgress = true
		state.Offset = nil
		state.Seen = map[string]factRecord{}
	}
	pageOffset := state.Offset
	if state.PendingPage {
		pageOffset = state.PageStartOffset
	}
	page, err := w.store.Scroll(ctx, w.cfg.Maintenance.MaxFactsPerRun, pageOffset, nil, false)
	if err != nil {
		return errors.New("maintenance fact scan failed")
	}
	processedBefore := map[string]bool{}
	for _, id := range state.ProcessedIDs {
		processedBefore[id] = true
	}
	input, points, records, order := w.makeInput(page.Points, cat, catHash, state, processedBefore)
	processCount := len(input.Facts)
	for processCount > 0 {
		preview := input
		preview.Facts = input.Facts[:processCount]
		if _, buildErr := BuildRequest(w.cfg.Profiles[w.cfg.Maintenance.Profile], preview); buildErr == nil {
			break
		}
		processCount--
	}
	if len(input.Facts) > 0 && processCount == 0 {
		return errors.New("one maintenance fact exceeds the 128 KiB request bound; cursor retained")
	}
	processedAll := processCount == len(input.Facts)
	throughIndex := len(order) - 1
	if !processedAll && processCount > 0 {
		lastAlias := input.Facts[processCount-1].Alias
		last := points[lastAlias]
		for i, id := range order {
			if id == last.ID {
				throughIndex = i
				break
			}
		}
	} else if !processedAll { // zero eligible candidates means the entire page is safe to acknowledge.
		processCount = 0
	}
	processedNow := map[string]bool{}
	for i, id := range order {
		if i <= throughIndex {
			processedNow[id] = true
		}
	}
	selected := input
	selected.Facts = input.Facts[:processCount]
	evidenceKey := w.evidenceInferenceKey(selected.CatalogEvidence)
	evidenceOnly := len(selected.Facts) == 0 && len(selected.CatalogEvidence) > 0 && evidenceKey != state.EvidenceInferenceKey
	if evidenceOnly && w.hasEvidenceProposal(evidenceKey) {
		state.EvidenceInferenceKey = evidenceKey
		evidenceOnly = false
	}
	if len(selected.Facts) > 0 || evidenceOnly {
		if err := w.ensureProposalCapacity(); err != nil {
			return err
		}
		reservation, err := w.budget.Begin(time.Now())
		if err != nil {
			return err
		}
		proposal, usage, callErr := w.provider.Propose(ctx, selected)
		reservation.Finish(usage.InputTokens, usage.Known)
		if callErr != nil {
			return callErr
		}
		if err := validateProposal(selected, proposal); err != nil {
			return err
		}
		selectedPoints := map[string]qdrant.ScrollPoint{}
		selectedRecords := map[string]factRecord{}
		for _, f := range selected.Facts {
			point := points[f.Alias]
			selectedPoints[f.Alias] = point
			selectedRecords[point.ID] = records[point.ID]
		}
		private, err := w.makePrivateProposal(catHash, proposal, selected, selectedPoints, selectedRecords)
		if err != nil {
			return err
		}
		if evidenceOnly {
			private.EvidenceInferenceKey = evidenceKey
		}
		name, err := w.saveProposal(private)
		if err != nil {
			return err
		}
		state.LatestProposal = name
		if len(selected.CatalogEvidence) > 0 {
			state.EvidenceInferenceKey = evidenceKey
		}
		if w.cfg.Maintenance.CatalogPublish == "evidence_bounded" {
			_, err = w.tryPublishDescription(ctx, cat, catHash, selected, proposal, private)
			if err != nil {
				return err
			}
		}
	}
	for id, record := range records {
		if processedNow[id] {
			state.Seen[id] = record
		}
	}
	if len(state.Seen) > maxTrackedFacts {
		return errors.New("maintenance sweep exceeds bounded fact tracking capacity")
	}
	if throughIndex < len(order)-1 {
		state.PendingPage = true
		state.PageStartOffset = pageOffset
		for _, id := range order {
			if processedNow[id] && !processedBefore[id] {
				state.ProcessedIDs = append(state.ProcessedIDs, id)
			}
		}
		state.Offset = pageOffset
	} else {
		state.PendingPage = false
		state.PageStartOffset = nil
		state.ProcessedIDs = nil
		state.Offset = page.RawOffset
	}
	if !state.PendingPage && page.RawOffset == nil {
		deleted := make([]string, 0)
		for id := range state.Previous {
			if _, exists := state.Seen[id]; !exists {
				deleted = append(deleted, id)
			}
		}
		sort.Strings(deleted)
		state.LastSweepDeleted = len(deleted)
		state.LastDeletedIDs = deleted
		state.Previous = state.Seen
		state.Seen = map[string]factRecord{}
		state.InProgress = false
		state.Offset = nil
	}
	if err := w.saveState(state); err != nil {
		return err
	}
	return nil
}

func (w *Worker) recordOutcome(outcome string) {
	state, err := w.loadState()
	if err != nil {
		return
	}
	state.LastOutcome = outcome
	state.LastRunAt = time.Now().UTC().Format(time.RFC3339Nano)
	if outcome == "succeeded" {
		state.FailureStreak = 0
		state.NextAllowedAt = ""
		_ = w.saveState(state)
		return
	}
	state.FailureStreak++
	delay := w.interval
	for i := 1; i < state.FailureStreak && delay < time.Hour; i++ {
		delay *= 2
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	jitter := time.Duration(rand.Int64N(int64(delay/4) + 1))
	state.NextAllowedAt = time.Now().Add(delay + jitter).UTC().Format(time.RFC3339Nano)
	_ = w.saveState(state)
}

func (w *Worker) makeInput(points []qdrant.ScrollPoint, catalog contextcatalog.Snapshot, catalogHash string, state workerState, processed map[string]bool) (Input, map[string]qdrant.ScrollPoint, map[string]factRecord, []string) {
	filteredCatalog := contextcatalog.Snapshot{SchemaVersion: catalog.SchemaVersion, Version: catalog.Version, ParentHash: catalog.ParentHash, CreatedAt: catalog.CreatedAt, Entries: []contextcatalog.Entry{}}
	for _, e := range catalog.Entries {
		if contextcatalog.Eligible(e) && e.Namespace == "projects" && w.cfg.Egress.Allows(e.Namespace, []string{e.Tag}) {
			filteredCatalog.Entries = append(filteredCatalog.Entries, e)
		}
	}
	input := Input{Catalog: filteredCatalog, CatalogEvidence: makeCatalogEvidence(filteredCatalog), Facts: []Fact{}, baseCatalogHash: catalogHash}
	pointMap := map[string]qdrant.ScrollPoint{}
	records := map[string]factRecord{}
	order := make([]string, 0, len(points))
	for _, point := range points {
		order = append(order, point.ID)
		if processed[point.ID] {
			continue
		}
		p := point.Payload
		if !eligibleCurrent(point.ID, p) {
			continue
		}
		ns, ok := p["namespace"].(string)
		if !ok {
			continue
		}
		tags := payloadTags(p["tags"])
		if tags == nil && p["tags"] != nil {
			continue
		}
		validTags := true
		for _, tag := range tags {
			if !projectTagRE.MatchString(tag) {
				validTags = false
				break
			}
		}
		if !validTags || len(tags) > 64 {
			continue
		}
		if !w.cfg.Egress.Allows(ns, tags) {
			continue
		}
		text, ok := p["text"].(string)
		if !ok || strings.TrimSpace(text) == "" || len(text) > 8192 {
			continue
		}
		finger, err := factgrouping.Fingerprint(p)
		if err != nil {
			continue
		}
		group := factgrouping.GroupFrom(p)
		record := factRecord{Fingerprint: finger, Group: group}
		old, found := state.Previous[point.ID]
		if state.InProgress {
			if current, ok := state.Seen[point.ID]; ok {
				old, found = current, true
			}
		}
		if found && old.Fingerprint == record.Fingerprint && sameGroup(old.Group, record.Group) {
			records[point.ID] = record
			continue
		}
		alias := fmt.Sprintf("f%03d", len(input.Facts))
		primary, _ := p["primary_tag"].(string)
		if primary != "" && !projectTagRE.MatchString(primary) {
			continue
		}
		input.Facts = append(input.Facts, Fact{Alias: alias, Text: text, Namespace: ns, Tags: tags, PrimaryTag: primary, Fingerprint: finger})
		pointMap[alias] = point
		records[point.ID] = record
	}
	return input, pointMap, records, order
}

const (
	maxCatalogEvidenceItems = 32
	maxCatalogEvidenceBytes = 16 * 1024
)

func makeCatalogEvidence(catalog contextcatalog.Snapshot) []CatalogEvidence {
	out := make([]CatalogEvidence, 0)
	total := 0
	appendEvidence := func(ns, tag, kind, text string) {
		if text == "" || len(out) >= maxCatalogEvidenceItems || total+len(text) > maxCatalogEvidenceBytes {
			return
		}
		sum := sha256.Sum256([]byte(text))
		out = append(out, CatalogEvidence{Alias: fmt.Sprintf("c%03d", len(out)), Namespace: ns, Tag: tag, Kind: kind, Digest: hex.EncodeToString(sum[:]), Text: text})
		total += len(text)
	}
	for _, entry := range catalog.Entries {
		if !contextcatalog.Eligible(entry) || entry.Namespace != "projects" {
			continue
		}
		identity := entry.Owner + "\x00" + entry.Namespace + "\x00" + entry.ProjectKey + "\x00" + entry.ProjectID + "\x00" + entry.Tag + "\x00" + entry.Name
		identitySum := sha256.Sum256([]byte(identity))
		identityDigest := hex.EncodeToString(identitySum[:])
		start := len(out)
		if entry.DeclaredSummary != "" {
			appendEvidence(entry.Namespace, entry.Tag, "user_declared", entry.DeclaredSummary)
		} else if entry.DescriptionSource == "client_declared" {
			appendEvidence(entry.Namespace, entry.Tag, "user_declared", entry.Summary)
		}
		for _, evidence := range entry.ClientEvidence {
			appendEvidence(entry.Namespace, entry.Tag, evidence.Kind, evidence.Text)
		}
		for i := start; i < len(out); i++ {
			out[i].sourceID = identityDigest
		}
	}
	return out
}

func (w *Worker) evidenceInferenceKey(evidence []CatalogEvidence) string {
	if len(evidence) == 0 {
		return ""
	}
	profile := w.cfg.Profiles[w.cfg.Maintenance.Profile]
	identity := struct {
		Profile   aipolicy.Profile  `json:"profile"`
		Publish   string            `json:"catalog_publish"`
		Egress    aipolicy.Egress   `json:"egress"`
		Evidence  []CatalogEvidence `json:"evidence"`
		SourceIDs []string          `json:"source_ids"`
	}{profile, w.cfg.Maintenance.CatalogPublish, w.cfg.Egress, evidence, catalogEvidenceSourceIDs(evidence)}
	raw, _ := json.Marshal(identity)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func catalogEvidenceSourceIDs(evidence []CatalogEvidence) []string {
	ids := make([]string, len(evidence))
	for i := range evidence {
		ids[i] = evidence[i].sourceID
	}
	return ids
}

func eligibleCurrent(id string, p map[string]interface{}) bool {
	if p == nil {
		return false
	}
	if status, exists := p["maintenance_status"]; exists {
		if s, ok := status.(string); !ok || s != "active" {
			return false
		}
	}
	view, err := lifecycle.Parse(p, id)
	if err != nil || !view.Valid || view.State != lifecycle.Current {
		return false
	}
	if until, ok := p["valid_until"]; ok {
		value, ok := until.(string)
		if !ok {
			return false
		}
		var expiry time.Time
		var err error
		if len(value) == 10 {
			expiry, err = time.Parse("2006-01-02", value)
		} else {
			expiry, err = time.Parse(time.RFC3339, value)
		}
		if err != nil || expiry.Before(time.Now().UTC().Truncate(24*time.Hour)) {
			return false
		}
	}
	return true
}

func payloadTags(raw any) []string {
	out := []string{}
	switch v := raw.(type) {
	case []string:
		out = append(out, v...)
	case []any:
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				return nil
			}
			out = append(out, s)
		}
	case nil:
		return []string{}
	default:
		return nil
	}
	seen := map[string]bool{}
	for _, tag := range out {
		if seen[tag] {
			return nil
		}
		seen[tag] = true
	}
	return out
}
func sameGroup(a, b factgrouping.Group) bool {
	if a.PrimaryTag != b.PrimaryTag || a.AuditRef != b.AuditRef || len(a.Tags) != len(b.Tags) {
		return false
	}
	for i := range a.Tags {
		if a.Tags[i] != b.Tags[i] {
			return false
		}
	}
	return true
}

func sameGrouping(a, b factgrouping.Group) bool {
	if a.PrimaryTag != b.PrimaryTag || len(a.Tags) != len(b.Tags) {
		return false
	}
	for i := range a.Tags {
		if a.Tags[i] != b.Tags[i] {
			return false
		}
	}
	return true
}

func (w *Worker) makePrivateProposal(base string, p Proposal, in Input, points map[string]qdrant.ScrollPoint, records map[string]factRecord) (privateProposal, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	manifest := factgrouping.Manifest{SchemaVersion: 1, CatalogHash: base, Changes: []factgrouping.Change{}, Approved: false}
	factByAlias := map[string]Fact{}
	dependencies := make([]privateDependency, 0, len(in.Facts))
	approvedProjectTags := map[string]bool{}
	for _, entry := range in.Catalog.Entries {
		if entry.Namespace == "projects" && contextcatalog.Eligible(entry) {
			approvedProjectTags[entry.Tag] = true
		}
	}
	catalogDeps := make([]privateCatalogDependency, 0, len(in.CatalogEvidence))
	for _, evidence := range in.CatalogEvidence {
		var source *contextcatalog.Entry
		for i := range in.Catalog.Entries {
			entry := &in.Catalog.Entries[i]
			if entry.Namespace == evidence.Namespace && entry.Tag == evidence.Tag {
				source = entry
				break
			}
		}
		if source == nil {
			return privateProposal{}, errors.New("maintenance catalog evidence dependency is unavailable")
		}
		catalogDeps = append(catalogDeps, privateCatalogDependency{Alias: evidence.Alias, ProjectID: source.ProjectID, Owner: source.Owner, ProjectKey: source.ProjectKey, Namespace: evidence.Namespace, Tag: evidence.Tag, Kind: evidence.Kind, Digest: evidence.Digest})
	}
	for _, f := range in.Facts {
		factByAlias[f.Alias] = f
		point, ok := points[f.Alias]
		if !ok {
			return privateProposal{}, errors.New("maintenance dependency is unavailable")
		}
		record, ok := records[point.ID]
		if !ok {
			return privateProposal{}, errors.New("maintenance dependency version is unavailable")
		}
		dependencies = append(dependencies, privateDependency{Alias: f.Alias, PointID: point.ID, Fingerprint: record.Fingerprint, Namespace: f.Namespace, Text: f.Text, Before: record.Group})
	}
	for _, g := range p.Grouping {
		f, ok := factByAlias[g.Alias]
		if !ok || f.Namespace != "projects" {
			continue
		}
		point := points[g.Alias]
		record := records[point.ID]
		tags := append([]string(nil), f.Tags...)
		if f.PrimaryTag != "" && g.PrimaryTag != "" && f.PrimaryTag != g.PrimaryTag && approvedProjectTags[f.PrimaryTag] {
			keepOldPrimary := contains(g.RelatedTags, f.PrimaryTag)
			if !keepOldPrimary {
				filtered := tags[:0]
				for _, tag := range tags {
					if tag != f.PrimaryTag {
						filtered = append(filtered, tag)
					}
				}
				tags = filtered
			}
		}
		for _, tag := range g.RelatedTags {
			if !contains(tags, tag) {
				tags = append(tags, tag)
			}
		}
		if g.PrimaryTag != "" && !contains(tags, g.PrimaryTag) {
			tags = append(tags, g.PrimaryTag)
		}
		sort.Strings(tags)
		primary := g.PrimaryTag
		if primary == "" {
			primary = f.PrimaryTag
		}
		after := factgrouping.Group{Tags: tags, PrimaryTag: primary}
		if sameGrouping(record.Group, after) {
			continue
		}
		manifest.Changes = append(manifest.Changes, factgrouping.Change{PointID: point.ID, Fingerprint: record.Fingerprint, Before: record.Group, After: after})
	}
	identity := struct {
		Proposal Proposal              `json:"proposal"`
		Changes  []factgrouping.Change `json:"changes"`
	}{p, manifest.Changes}
	sumBytes, _ := json.Marshal(identity)
	sum := sha256.Sum256(sumBytes)
	id := "maintenance-" + hex.EncodeToString(sum[:8])
	manifest.ID = id
	for i := range manifest.Changes {
		manifest.Changes[i].After.AuditRef = id
	}
	bindingHash, err := w.bindingHash(base)
	if err != nil {
		return privateProposal{}, err
	}
	return privateProposal{SchemaVersion: 1, CreatedAt: now, ProviderProtocol: w.cfg.Profiles[w.cfg.Maintenance.Profile].Protocol, Model: w.cfg.Profiles[w.cfg.Maintenance.Profile].Model, BaseCatalogHash: base, BindingHash: bindingHash, Proposal: p, Dependencies: dependencies, CatalogDependencies: catalogDeps, Manifest: manifest}, nil
}

// tryPublishDescription applies a model description only when every source and
// exact fact dependency still matches the active generation. The proposal was
// durably saved by the caller before this method is entered.
func (w *Worker) tryPublishDescription(ctx context.Context, source contextcatalog.Snapshot, base string, in Input, p Proposal, private privateProposal) (bool, error) {
	if w.cfg.Maintenance.CatalogPublish != "evidence_bounded" || p.Catalog == nil || len(p.MissingEvidence) != 0 || len(p.Catalog.Entries) != len(in.Catalog.Entries) {
		return false, nil
	}
	if ctx == nil || ctx.Err() != nil {
		return false, nil
	}
	current, currentHash, err := contextcatalog.LoadActive(w.cfg.CatalogDir)
	if err != nil || currentHash != base {
		return false, nil
	}
	if sourceHash, hashErr := contextcatalog.Hash(source); hashErr != nil || sourceHash != base {
		return false, nil
	}
	allowed := contextcatalog.Snapshot{SchemaVersion: current.SchemaVersion, Version: current.Version, ParentHash: current.ParentHash, CreatedAt: current.CreatedAt, Entries: []contextcatalog.Entry{}}
	for _, entry := range current.Entries {
		if contextcatalog.Eligible(entry) && entry.Namespace == "projects" && w.cfg.Egress.Allows(entry.Namespace, []string{entry.Tag}) {
			allowed.Entries = append(allowed.Entries, entry)
		}
	}
	if !sameCatalogEvidence(makeCatalogEvidence(allowed), in.CatalogEvidence) || !sameSourceEntries(allowed.Entries, in.Catalog.Entries) {
		return false, nil
	}
	if !sameCatalogDependencies(private.CatalogDependencies, in.CatalogEvidence, allowed.Entries) {
		return false, nil
	}
	if !w.verifyFactDependencies(ctx, in.Facts, private.Dependencies) {
		return false, nil
	}

	merged, changed := mergeDescriptiveProposal(current, allowed, *p.Catalog, in)
	if !changed || ctx.Err() != nil {
		return false, nil
	}
	merged.ParentHash = base
	merged.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	merged.Version = nextCatalogVersion(current.Version, base, p)
	if err := contextcatalog.Validate(merged); err != nil {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, nil
	}
	_, err = contextcatalog.Publish(w.cfg.CatalogDir, merged, base)
	if errors.Is(err, contextcatalog.ErrConflict) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func mergeDescriptiveProposal(current, allowed, proposal contextcatalog.Snapshot, in Input) (contextcatalog.Snapshot, bool) {
	if len(proposal.Entries) != len(allowed.Entries) {
		return contextcatalog.Snapshot{}, false
	}
	proposed := make(map[string]contextcatalog.Entry, len(proposal.Entries))
	for _, entry := range proposal.Entries {
		if entry.ReviewStatus == "unresolved" {
			return contextcatalog.Snapshot{}, false
		}
		key := entry.Namespace + "\x00" + entry.Tag
		if _, exists := proposed[key]; exists {
			return contextcatalog.Snapshot{}, false
		}
		proposed[key] = entry
	}
	merged := cloneSnapshot(current)
	changed := false
	for _, original := range allowed.Entries {
		candidate, ok := proposed[original.Namespace+"\x00"+original.Tag]
		if !ok || candidate.Namespace != original.Namespace || candidate.Tag != original.Tag || candidate.Name != original.Name || candidate.ReviewStatus != "draft" {
			return contextcatalog.Snapshot{}, false
		}
		activeIndex := findEntry(merged.Entries, original.Namespace, original.Tag)
		if activeIndex < 0 {
			return contextcatalog.Snapshot{}, false
		}
		active := &merged.Entries[activeIndex]
		if active.Name != original.Name || active.ProjectID != original.ProjectID || active.ProjectKey != original.ProjectKey || active.Owner != original.Owner || active.ReviewStatus != original.ReviewStatus || active.DescriptionSource != original.DescriptionSource || active.DeclaredSummary != original.DeclaredSummary || !clientEvidenceEqual(active.ClientEvidence, original.ClientEvidence) {
			return contextcatalog.Snapshot{}, false
		}
		contentChanged := active.Summary != candidate.Summary || !stringSlicesEqual(active.Aliases, candidate.Aliases) || !stringSlicesEqual(active.OwnedComponents, candidate.OwnedComponents) || !stringSlicesEqual(active.Boundaries, candidate.Boundaries) || !stringSlicesEqual(active.Uses, candidate.Uses) || !stringSlicesEqual(active.SharedWith, candidate.SharedWith) || !stringSlicesEqual(active.PositiveExamples, candidate.PositiveExamples) || !stringSlicesEqual(active.NegativeExamples, candidate.NegativeExamples)
		if !contentChanged {
			continue
		}
		if len(candidate.EvidenceRefs) == 0 {
			return contextcatalog.Snapshot{}, false
		}
		durableRefs, ok := durableEvidenceRefs(in, candidate.Namespace, candidate.Tag, candidate.EvidenceRefs)
		if !ok {
			return contextcatalog.Snapshot{}, false
		}
		if active.DeclaredSummary == "" && active.DescriptionSource == "client_declared" {
			active.DeclaredSummary = active.Summary
		}
		active.Summary = candidate.Summary
		active.Aliases = cloneStrings(candidate.Aliases)
		active.OwnedComponents = cloneStrings(candidate.OwnedComponents)
		active.Boundaries = cloneStrings(candidate.Boundaries)
		active.Uses = cloneStrings(candidate.Uses)
		active.SharedWith = cloneStrings(candidate.SharedWith)
		active.PositiveExamples = cloneStrings(candidate.PositiveExamples)
		active.NegativeExamples = cloneStrings(candidate.NegativeExamples)
		active.EvidenceRefs = durableRefs
		active.DescriptionSource = "model_derived"
		changed = true
	}
	if len(proposed) != len(allowed.Entries) {
		return contextcatalog.Snapshot{}, false
	}
	return merged, changed
}

func clientEvidenceEqual(a, b []contextcatalog.Evidence) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (w *Worker) verifyFactDependencies(ctx context.Context, facts []Fact, dependencies []privateDependency) bool {
	return verifyFactDependenciesWithGetter(ctx, facts, dependencies, w.store.Get)
}

func verifyFactDependenciesWithGetter(ctx context.Context, facts []Fact, dependencies []privateDependency, get func(context.Context, string) (qdrant.Point, bool, error)) bool {
	if len(facts) != len(dependencies) {
		return false
	}
	byAlias := make(map[string]privateDependency, len(dependencies))
	for _, dependency := range dependencies {
		byAlias[dependency.Alias] = dependency
	}
	for _, fact := range facts {
		if ctx.Err() != nil {
			return false
		}
		dependency, ok := byAlias[fact.Alias]
		if !ok || dependency.Namespace != fact.Namespace || dependency.Fingerprint != fact.Fingerprint {
			return false
		}
		point, found, err := get(ctx, dependency.PointID)
		if err != nil || !found || point.ID != dependency.PointID || !eligibleCurrent(point.ID, point.Payload) {
			return false
		}
		ns, _ := point.Payload["namespace"].(string)
		fingerprint, err := factgrouping.Fingerprint(point.Payload)
		if err != nil || ns != dependency.Namespace || fingerprint != dependency.Fingerprint {
			return false
		}
		group := factgrouping.GroupFrom(point.Payload)
		if !sameGroup(group, dependency.Before) || group.PrimaryTag != fact.PrimaryTag || !stringSlicesEqual(group.Tags, fact.Tags) {
			return false
		}
	}
	return true
}

func sameCatalogDependencies(dependencies []privateCatalogDependency, evidence []CatalogEvidence, entries []contextcatalog.Entry) bool {
	if len(dependencies) != len(evidence) {
		return false
	}
	entriesByTag := map[string]contextcatalog.Entry{}
	for _, entry := range entries {
		entriesByTag[entry.Namespace+"\x00"+entry.Tag] = entry
	}
	for i, item := range evidence {
		dep := dependencies[i]
		entry, ok := entriesByTag[item.Namespace+"\x00"+item.Tag]
		if !ok || dep.Alias != item.Alias || dep.ProjectID != entry.ProjectID || dep.Owner != entry.Owner || dep.ProjectKey != entry.ProjectKey || dep.Namespace != item.Namespace || dep.Tag != item.Tag || dep.Kind != item.Kind || dep.Digest != item.Digest {
			return false
		}
	}
	return true
}

func sameCatalogEvidence(a, b []CatalogEvidence) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameSourceEntries(a, b []contextcatalog.Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.Namespace != y.Namespace || x.Tag != y.Tag || x.Name != y.Name || x.Summary != y.Summary || x.ProjectID != y.ProjectID || x.ProjectKey != y.ProjectKey || x.Owner != y.Owner || x.ReviewStatus != y.ReviewStatus || x.DescriptionSource != y.DescriptionSource || x.DeclaredSummary != y.DeclaredSummary {
			return false
		}
	}
	return true
}

func durableEvidenceRefs(in Input, namespace, tag string, aliases []string) ([]string, bool) {
	refs := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		found := false
		for _, evidence := range in.CatalogEvidence {
			if evidence.Alias == alias {
				if evidence.Namespace != namespace || evidence.Tag != tag {
					return nil, false
				}
				refs = append(refs, "client:"+evidence.Kind+":"+evidence.Digest)
				found = true
				break
			}
		}
		if found {
			continue
		}
		fact, ok := factForAlias(in.Facts, alias)
		if !ok {
			return nil, false
		}
		refs = append(refs, "fact:"+fact.Fingerprint)
	}
	return refs, true
}

func findEntry(entries []contextcatalog.Entry, namespace, tag string) int {
	for i := range entries {
		if entries[i].Namespace == namespace && entries[i].Tag == tag {
			return i
		}
	}
	return -1
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneSnapshot(s contextcatalog.Snapshot) contextcatalog.Snapshot {
	s.Entries = append([]contextcatalog.Entry{}, s.Entries...)
	for i := range s.Entries {
		e := &s.Entries[i]
		e.Aliases = cloneStrings(e.Aliases)
		e.OwnedComponents = cloneStrings(e.OwnedComponents)
		e.Boundaries = cloneStrings(e.Boundaries)
		e.Uses = cloneStrings(e.Uses)
		e.SharedWith = cloneStrings(e.SharedWith)
		e.PositiveExamples = cloneStrings(e.PositiveExamples)
		e.NegativeExamples = cloneStrings(e.NegativeExamples)
		e.EvidenceRefs = cloneStrings(e.EvidenceRefs)
		e.ClientEvidence = append([]contextcatalog.Evidence(nil), e.ClientEvidence...)
	}
	return s
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string{}, in...)
}

func nextCatalogVersion(current, base string, p Proposal) string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(append([]byte(base+"\x00"+current+"\x00"), raw...))
	return "maintenance-" + hex.EncodeToString(sum[:8])
}

func (w *Worker) bindingHash(catalogHash string) (string, error) {
	profile := w.cfg.Profiles[w.cfg.Maintenance.Profile]
	identity := struct {
		CatalogHash    string           `json:"catalog_hash"`
		Profile        aipolicy.Profile `json:"profile"`
		Egress         aipolicy.Egress  `json:"egress"`
		CatalogPublish string           `json:"catalog_publish"`
	}{catalogHash, profile, w.cfg.Egress, w.cfg.Maintenance.CatalogPublish}
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", errors.New("cannot bind maintenance progress")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}

func (w *Worker) saveProposal(p privateProposal) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	if len(raw) > MaxResponseBytes*4 {
		return "", errors.New("maintenance proposal artifact exceeds limit")
	}
	sum := sha256.Sum256(raw)
	name := "proposal-" + hex.EncodeToString(sum[:]) + ".json"
	path := filepath.Join(w.dir, "proposals", name)
	if err := aipolicy.SecureDir(filepath.Dir(path)); err != nil {
		return "", errors.New("cannot prepare private proposal directory")
	}
	if _, err := os.Lstat(path); err == nil {
		existing, e := aipolicy.ReadPrivate(path, MaxResponseBytes*4)
		if e != nil || !bytes.Equal(existing, raw) {
			return "", errors.New("proposal artifact identity conflict")
		}
		return name, nil
	} else if !os.IsNotExist(err) {
		return "", errors.New("cannot inspect proposal artifact")
	}
	if err := w.ensureProposalCapacity(); err != nil {
		return "", err
	}
	if err := aipolicy.AtomicWrite(filepath.Dir(path), name, raw); err != nil {
		return "", errors.New("cannot save private proposal artifact")
	}
	return name, nil
}

func (w *Worker) hasEvidenceProposal(key string) bool {
	if key == "" {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(w.dir, "proposals"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "proposal-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, readErr := aipolicy.ReadPrivate(filepath.Join(w.dir, "proposals", entry.Name()), MaxResponseBytes*4)
		if readErr != nil {
			continue
		}
		var proposal privateProposal
		if json.Unmarshal(raw, &proposal) == nil && proposal.EvidenceInferenceKey == key {
			return true
		}
	}
	return false
}

func (w *Worker) ensureProposalCapacity() error {
	entries, err := os.ReadDir(filepath.Join(w.dir, "proposals"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.New("cannot inspect proposal retention")
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "proposal-") && strings.HasSuffix(e.Name(), ".json") {
			count++
		}
	}
	if count >= maxProposalFiles {
		return errors.New("proposal retention limit reached; archive reviewed proposals before resuming maintenance")
	}
	return nil
}

func (w *Worker) loadState() (workerState, error) {
	path := filepath.Join(w.dir, "state.json")
	if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
		return workerState{SchemaVersion: 1, Seen: map[string]factRecord{}, Previous: map[string]factRecord{}}, nil
	} else if statErr != nil {
		return workerState{}, errors.New("cannot inspect private maintenance state")
	}
	raw, err := aipolicy.ReadPrivate(path, maxStateBytes)
	if err != nil {
		return workerState{}, errors.New("cannot read private maintenance state")
	}
	var s workerState
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || s.SchemaVersion != 1 || len(s.Seen) > maxTrackedFacts || len(s.Previous) > maxTrackedFacts {
		return workerState{}, errors.New("invalid maintenance state")
	}
	if s.Seen == nil {
		s.Seen = map[string]factRecord{}
	}
	if s.Previous == nil {
		s.Previous = map[string]factRecord{}
	}
	return s, nil
}
func (w *Worker) saveState(s workerState) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(raw) > maxStateBytes {
		return errors.New("maintenance state exceeds bounded size")
	}
	return aipolicy.AtomicWrite(w.dir, "state.json", raw)
}
