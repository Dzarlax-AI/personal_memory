package aimaintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
)

type catalogWorkerState struct {
	SchemaVersion  int               `json:"schema_version"`
	Cursor         string            `json:"cursor,omitempty"`
	Fingerprints   map[string]string `json:"fingerprints"`
	LatestProposal string            `json:"latest_proposal,omitempty"`
	LastDispatchAt string            `json:"last_dispatch_at,omitempty"`
	LastOutcome    string            `json:"last_outcome,omitempty"`
	LastReason     string            `json:"last_reason,omitempty"`
	LastQueued     int               `json:"last_queued"`
	LastCompleted  int               `json:"last_completed"`
	LastSkipped    int               `json:"last_skipped"`
	FailureStreak  int               `json:"failure_streak"`
	NextAllowedAt  string            `json:"next_allowed_at,omitempty"`
}

// CatalogStatus is deliberately content-free and safe for an operator status command.
type CatalogStatus struct {
	Enabled        bool   `json:"enabled"`
	Scope          string `json:"scope"`
	Model          string `json:"model,omitempty"`
	LastDispatchAt string `json:"last_dispatch_at,omitempty"`
	LastOutcome    string `json:"last_outcome,omitempty"`
	ReasonCode     string `json:"reason_code,omitempty"`
	Queued         int    `json:"queued"`
	Completed      int    `json:"completed"`
	Skipped        int    `json:"skipped"`
}

type catalogWorker struct {
	cfg            aipolicy.Config
	provider       Provider
	budget         *aipolicy.Budget
	proposalWorker *Worker
	statePath      string
	projectionPath string
}

var errCatalogSourcesChanged = errors.New("maintenance catalog sources changed during processing")

func newCatalogWorker(cfg aipolicy.Config, provider Provider, budget *aipolicy.Budget, proposalDir string) (*catalogWorker, error) {
	if !cfg.Maintenance.Enabled || cfg.Maintenance.Scope != "catalog_only" || provider == nil || budget == nil {
		return nil, errors.New("catalog-only maintenance dependencies are unavailable")
	}
	stateDir := filepath.Join(cfg.StateDir, "maintenance-catalog")
	if err := aipolicy.SecureDir(stateDir); err != nil {
		return nil, errors.New("cannot prepare private catalog maintenance state")
	}
	proposalWorker := &Worker{cfg: cfg, provider: provider, budget: budget, dir: proposalDir}
	return &catalogWorker{cfg: cfg, provider: provider, budget: budget, proposalWorker: proposalWorker, statePath: filepath.Join(stateDir, "state.json"), projectionPath: filepath.Join(stateDir, "projections.json")}, nil
}

func (w *catalogWorker) RunOnce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := w.load()
	if err != nil {
		return err
	}
	if state.NextAllowedAt != "" {
		if at, e := time.Parse(time.RFC3339Nano, state.NextAllowedAt); e == nil && time.Now().Before(at) {
			state.LastOutcome = "backoff"
			state.LastReason = "backoff_active"
			state.LastQueued, state.LastCompleted = 0, 0
			return w.save(state)
		}
	}
	cat, base, err := contextcatalog.LoadActive(w.cfg.CatalogDir)
	if err != nil {
		return errors.New("active context catalog unavailable")
	}
	cards := admittedCatalogEntries(cat, w.cfg)
	sort.Slice(cards, func(i, j int) bool {
		if cards[i].Namespace == cards[j].Namespace {
			return cards[i].Tag < cards[j].Tag
		}
		return cards[i].Namespace < cards[j].Namespace
	})
	cards, blocked, err := w.restorePublishedOriginals(cat, cards)
	if err != nil {
		return err
	}
	if len(cards) == 0 {
		state.LastOutcome, state.LastReason = "no_eligible_cards", "no_eligible_cards"
		state.LastQueued, state.LastCompleted, state.LastSkipped = 0, 0, 0
		return w.save(state)
	}
	eligibleKeys := make(map[string]bool, len(cards))
	for _, card := range cards {
		eligibleKeys[cardKey(card)] = true
	}
	for key := range state.Fingerprints {
		if !eligibleKeys[key] {
			delete(state.Fingerprints, key)
		}
	}
	start := 0
	if state.Cursor != "" {
		for i, e := range cards {
			if cardKey(e) == state.Cursor {
				start = (i + 1) % len(cards)
				break
			}
		}
	}
	ordered := append(append([]contextcatalog.Entry{}, cards[start:]...), cards[:start]...)
	selected := make([]contextcatalog.Entry, 0, w.cfg.Maintenance.MaxProjectsPerRun)
	skipped := 0
	projectionUnavailable := false
	newFingerprints := cloneFingerprints(state.Fingerprints)
	for _, card := range ordered {
		if len(selected) >= w.cfg.Maintenance.MaxProjectsPerRun {
			break
		}
		key := cardKey(card)
		if blocked[key] {
			skipped++
			projectionUnavailable = true
			state.Cursor = key
			continue
		}
		finger := catalogCardFingerprintWithContext(card, cards, w.cfg)
		if state.Fingerprints[key] == finger {
			continue
		}
		ev, fitsAlone := strictEvidenceForCard(card)
		if len(ev) == 0 {
			skipped++
			newFingerprints[key] = finger
			state.Cursor = key
			continue
		}
		if !fitsAlone {
			skipped++
			newFingerprints[key] = finger
			state.Cursor = key
			continue
		}
		candidate := append(append([]contextcatalog.Entry{}, selected...), card)
		input := catalogOnlyInput(candidate, cat, cards, w.cfg, base)
		if !batchEvidenceFits(input.CatalogEvidence) {
			// This card is individually processable but does not fit the current
			// batch. Leave it pending for the next cursor turn.
			break
		}
		if _, buildErr := BuildRequest(w.cfg.Profiles[w.cfg.Maintenance.Profile], input); buildErr != nil {
			if len(selected) == 0 {
				skipped++
				newFingerprints[key] = finger
				state.Cursor = key
				continue
			}
			break
		}
		selected = append(selected, card)
	}
	state.LastQueued, state.LastCompleted, state.LastSkipped = len(selected), 0, skipped
	if len(selected) == 0 {
		state.Fingerprints = newFingerprints
		if skipped > 0 {
			state.LastOutcome, state.LastReason = "skipped", "evidence_unavailable"
			if projectionUnavailable {
				state.LastReason = "source_projection_unavailable"
			}
		} else {
			state.LastOutcome, state.LastReason = "no_changes", "no_changes"
		}
		return w.save(state)
	}
	input := catalogOnlyInput(selected, cat, cards, w.cfg, base)
	receipt := w.receiptKey(input)
	var proposal Proposal
	var artifact privateProposal
	alreadyPublished := false
	if existing, name, complete, found := w.findReceipt(receipt, input, cat, base); found {
		artifact, proposal = existing, existing.Proposal
		state.LatestProposal = name
		alreadyPublished = complete
	} else {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.proposalWorker.ensureProposalCapacity(); err != nil {
			return err
		}
		reservation, err := w.budget.Begin(time.Now())
		if err != nil {
			return err
		}
		state.LastOutcome, state.LastReason = "dispatching", ""
		state.LastQueued, state.LastCompleted, state.LastSkipped = len(selected), 0, skipped
		state.Fingerprints = newFingerprints
		state.LastDispatchAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := w.save(state); err != nil {
			reservation.Finish(0, true)
			return err
		}
		var usage aijudgment.Usage
		var callErr error
		proposal, usage, callErr = w.provider.Propose(ctx, input)
		reservation.Finish(usage.InputTokens, usage.Known)
		if callErr != nil {
			return callErr
		}
		if err := validateProposal(input, proposal); err != nil {
			return err
		}
		// In registered mode, reject a known source change before persisting a
		// result. An unavailable catalog is different: retain the completed
		// inference receipt, then require the original source to validate on
		// recovery before it can be accepted.
		if w.cfg.Maintenance.ProjectPolicy == "registered" {
			available, current := w.catalogSourcesState(cards)
			if available && !current {
				return errCatalogSourcesChanged
			}
		}
		publishedProjection := []privateCatalogPublished(nil)
		if w.cfg.Maintenance.CatalogPublish == "evidence_bounded" {
			publishedProjection = expectedCatalogPublication(input, proposal, cat)
		}
		artifact = privateProposal{SchemaVersion: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ProviderProtocol: w.cfg.Profiles[w.cfg.Maintenance.Profile].Protocol, Model: w.cfg.Profiles[w.cfg.Maintenance.Profile].Model, MaintenancePolicy: w.cfg.Maintenance.ProjectPolicy, RelationshipInstructions: maintenanceRelationshipInstructionsIdentity(), BaseCatalogHash: base, Proposal: proposal, CatalogDependencies: catalogDependenciesFor(input), CatalogReceiptKey: receipt, CatalogOriginal: snapshotOf(input.Catalog), CatalogOriginalAll: snapshotOf(contextcatalog.Snapshot{SchemaVersion: cat.SchemaVersion, Version: cat.Version, ParentHash: cat.ParentHash, CreatedAt: cat.CreatedAt, Entries: cards}), CatalogPublished: publishedProjection}
		name, err := w.proposalWorker.saveProposal(artifact)
		if err != nil {
			// If the proposal file landed but a later operation failed, findReceipt is the recovery path.
			return err
		}
		state.LatestProposal = name
	}
	if artifact.MaintenancePolicy != w.cfg.Maintenance.ProjectPolicy || artifact.RelationshipInstructions != maintenanceRelationshipInstructionsIdentity() || (w.cfg.Maintenance.ProjectPolicy == "registered" && !w.catalogSourcesCurrent(cards)) {
		return errCatalogSourcesChanged
	}
	// Preserve completed inference before mutable projection storage. A reused
	// receipt must also persist its original sources before publication.
	if catalogPublicationChanges(cat, artifact.CatalogPublished) {
		if err := w.persistOriginalProjections(cat, input, artifact.CatalogPublished); err != nil {
			return errors.New("cannot persist private catalog source projections")
		}
	}
	published := alreadyPublished
	if !alreadyPublished && w.cfg.Maintenance.CatalogPublish == "evidence_bounded" && proposal.Catalog != nil {
		published, err = w.publishSelected(ctx, cat, base, input, proposal, artifact)
		if err != nil {
			return err
		}
	}
	for _, card := range selected {
		key := cardKey(card)
		newFingerprints[key] = catalogCardFingerprintWithContext(card, cards, w.cfg)
		state.Cursor = key
	}
	state.Fingerprints = newFingerprints
	state.LastCompleted = len(selected)
	state.LastReason = ""
	switch {
	case published:
		state.LastOutcome = "published"
	case proposal.Catalog == nil:
		state.LastOutcome = "proposed"
	case w.cfg.Maintenance.CatalogPublish == "review":
		state.LastOutcome = "proposed"
	case len(proposal.MissingEvidence) > 0:
		state.LastOutcome, state.LastReason = "proposed", "insufficient_evidence"
	case catalogHasUnresolved(proposal.Catalog):
		state.LastOutcome, state.LastReason = "proposed", "unresolved_proposal"
	default:
		state.LastOutcome = "conflict_or_unpublished"
		state.LastReason = "base_or_dependency_changed"
	}
	return w.save(state)
}

func catalogHasUnresolved(catalog *contextcatalog.Snapshot) bool {
	if catalog == nil {
		return false
	}
	for _, entry := range catalog.Entries {
		if entry.ReviewStatus == "unresolved" {
			return true
		}
	}
	return false
}

func (w *catalogWorker) publishSelected(ctx context.Context, original contextcatalog.Snapshot, base string, input Input, proposal Proposal, private privateProposal) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if proposal.Catalog == nil || len(proposal.MissingEvidence) != 0 {
		return false, nil
	}
	current, hash, err := contextcatalog.LoadActive(w.cfg.CatalogDir)
	if err != nil {
		return false, err
	}
	if hash != base {
		return false, nil
	}
	if !sameCatalogDependencies(private.CatalogDependencies, input.CatalogEvidence, input.Catalog.Entries) || !sameCatalogEvidence(makeCatalogEvidence(input.Catalog), input.CatalogEvidence) {
		return false, nil
	}
	merged := cloneSnapshot(current)
	changed := false
	for _, selected := range input.Catalog.Entries {
		var candidate *contextcatalog.Entry
		for i := range proposal.Catalog.Entries {
			if proposal.Catalog.Entries[i].Namespace == selected.Namespace && proposal.Catalog.Entries[i].Tag == selected.Tag {
				candidate = &proposal.Catalog.Entries[i]
				break
			}
		}
		if candidate == nil || candidate.Name != selected.Name || candidate.ReviewStatus != "draft" {
			return false, nil
		}
		i := findEntry(merged.Entries, selected.Namespace, selected.Tag)
		if i < 0 {
			return false, nil
		}
		active := &merged.Entries[i]
		if !sameEntryIdentityAndSources(*active, selected) {
			return false, nil
		}
		if !sameIdentityDescriptionExceptEditable(*candidate, selected) {
			return false, nil
		}
		if catalogDescriptionEqual(*active, *candidate) {
			continue
		}
		refs, ok := durableEvidenceRefs(input, selected.Namespace, selected.Tag, candidate.EvidenceRefs)
		if !ok || len(refs) == 0 {
			return false, nil
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
		active.EvidenceRefs = refs
		active.DescriptionSource = "model_derived"
		changed = true
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	merged.ParentHash = base
	merged.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	merged.Version = nextCatalogVersion(current.Version, base, proposal)
	if err := contextcatalog.Validate(merged); err != nil {
		return false, nil
	}
	_, err = contextcatalog.Publish(w.cfg.CatalogDir, merged, base)
	if errors.Is(err, contextcatalog.ErrConflict) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = original
	return true, nil
}

func sameEntryIdentityAndSources(a, b contextcatalog.Entry) bool {
	identity := a.Namespace == b.Namespace && a.Tag == b.Tag && a.Name == b.Name && a.ProjectID == b.ProjectID && a.ProjectKey == b.ProjectKey && a.Owner == b.Owner && a.ReviewStatus == b.ReviewStatus && clientEvidenceEqual(a.ClientEvidence, b.ClientEvidence)
	if !identity {
		return false
	}
	if a.DescriptionSource == b.DescriptionSource {
		return a.DeclaredSummary == b.DeclaredSummary
	}
	// A private receipt must have matched the entire live model-derived entry
	// before this original client projection can be used for a new proposal.
	return a.DescriptionSource == "model_derived" && b.DescriptionSource == "client_declared" && b.Summary != "" && a.DeclaredSummary == b.Summary
}
func sameIdentityDescriptionExceptEditable(a, b contextcatalog.Entry) bool {
	return a.Namespace == b.Namespace && a.Tag == b.Tag && a.Name == b.Name && a.ReviewStatus == "draft" && a.ProjectID == "" && a.ProjectKey == "" && a.Owner == "" && len(a.ClientEvidence) == 0 && a.DescriptionSource == "" && a.DeclaredSummary == ""
}
func catalogDescriptionEqual(a, b contextcatalog.Entry) bool {
	return a.Summary == b.Summary && stringSlicesEqual(a.Aliases, b.Aliases) && stringSlicesEqual(a.OwnedComponents, b.OwnedComponents) && stringSlicesEqual(a.Boundaries, b.Boundaries) && stringSlicesEqual(a.Uses, b.Uses) && stringSlicesEqual(a.SharedWith, b.SharedWith) && stringSlicesEqual(a.PositiveExamples, b.PositiveExamples) && stringSlicesEqual(a.NegativeExamples, b.NegativeExamples)
}

func catalogOnlyInput(selected []contextcatalog.Entry, source contextcatalog.Snapshot, allCards []contextcatalog.Entry, cfg aipolicy.Config, base string) Input {
	s := contextcatalog.Snapshot{SchemaVersion: source.SchemaVersion, Version: source.Version, ParentHash: source.ParentHash, CreatedAt: source.CreatedAt, Entries: append([]contextcatalog.Entry{}, selected...)}
	evidence := make([]CatalogEvidence, 0)
	for _, card := range selected {
		one := contextcatalog.Snapshot{SchemaVersion: source.SchemaVersion, Version: source.Version, ParentHash: source.ParentHash, CreatedAt: source.CreatedAt, Entries: []contextcatalog.Entry{card}}
		for _, item := range makeCatalogEvidence(one) {
			item.Alias = fmt.Sprintf("c%03d", len(evidence))
			evidence = append(evidence, item)
		}
	}
	return Input{Catalog: s, CatalogEvidence: evidence, Facts: []Fact{}, CatalogOnly: true, contextIdentity: catalogContextIdentity(allCards, cfg), baseCatalogHash: base, contextEntries: append([]contextcatalog.Entry{}, allCards...)}
}
func snapshotOf(s contextcatalog.Snapshot) *contextcatalog.Snapshot { return &s }

func expectedCatalogPublication(in Input, proposal Proposal, current contextcatalog.Snapshot) []privateCatalogPublished {
	if proposal.Catalog == nil || len(proposal.MissingEvidence) != 0 {
		return nil
	}
	out := make([]privateCatalogPublished, 0, len(in.Catalog.Entries))
	for _, selected := range in.Catalog.Entries {
		var candidate *contextcatalog.Entry
		for i := range proposal.Catalog.Entries {
			if proposal.Catalog.Entries[i].Namespace == selected.Namespace && proposal.Catalog.Entries[i].Tag == selected.Tag {
				candidate = &proposal.Catalog.Entries[i]
				break
			}
		}
		if candidate == nil || candidate.ReviewStatus != "draft" {
			return nil
		}
		var live *contextcatalog.Entry
		for i := range current.Entries {
			if current.Entries[i].Namespace == selected.Namespace && current.Entries[i].Tag == selected.Tag {
				live = &current.Entries[i]
				break
			}
		}
		if live == nil {
			return nil
		}
		if catalogDescriptionEqual(*live, *candidate) {
			hash, err := catalogEntryHash(*live)
			if err != nil {
				return nil
			}
			out = append(out, privateCatalogPublished{Namespace: live.Namespace, Tag: live.Tag, EntryHash: hash})
			continue
		}
		refs, ok := durableEvidenceRefs(in, selected.Namespace, selected.Tag, candidate.EvidenceRefs)
		if !ok || len(refs) == 0 {
			return nil
		}
		entry := *live
		if entry.DeclaredSummary == "" && entry.DescriptionSource == "client_declared" {
			entry.DeclaredSummary = entry.Summary
		}
		entry.Summary = candidate.Summary
		entry.Aliases = cloneStrings(candidate.Aliases)
		entry.OwnedComponents = cloneStrings(candidate.OwnedComponents)
		entry.Boundaries = cloneStrings(candidate.Boundaries)
		entry.Uses = cloneStrings(candidate.Uses)
		entry.SharedWith = cloneStrings(candidate.SharedWith)
		entry.PositiveExamples = cloneStrings(candidate.PositiveExamples)
		entry.NegativeExamples = cloneStrings(candidate.NegativeExamples)
		entry.EvidenceRefs = refs
		entry.DescriptionSource = "model_derived"
		hash, err := catalogEntryHash(entry)
		if err != nil {
			return nil
		}
		out = append(out, privateCatalogPublished{Namespace: entry.Namespace, Tag: entry.Tag, EntryHash: hash})
	}
	return out
}

func catalogEntryHash(e contextcatalog.Entry) (string, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func strictEvidenceForCard(e contextcatalog.Entry) ([]CatalogEvidence, bool) {
	items := make([]CatalogEvidence, 0, len(e.ClientEvidence)+1)
	add := func(kind, value string) {
		if value == "" {
			return
		}
		sum := sha256.Sum256([]byte(value))
		items = append(items, CatalogEvidence{Alias: fmt.Sprintf("c%03d", len(items)), Namespace: e.Namespace, Tag: e.Tag, Kind: kind, Digest: hex.EncodeToString(sum[:]), Text: value})
	}
	if e.DeclaredSummary != "" {
		add("user_declared", e.DeclaredSummary)
	} else if e.DescriptionSource == "client_declared" {
		add("user_declared", e.Summary)
	}
	for _, evidence := range e.ClientEvidence {
		add(evidence.Kind, evidence.Text)
	}
	return items, batchEvidenceFits(items)
}
func batchEvidenceFits(e []CatalogEvidence) bool {
	if len(e) > maxCatalogEvidenceItems {
		return false
	}
	total := 0
	for _, x := range e {
		total += len(x.Text)
	}
	return total <= maxCatalogEvidenceBytes
}
func cardKey(e contextcatalog.Entry) string { return e.Namespace + "/" + e.Tag }
func catalogCardFingerprint(e contextcatalog.Entry, cfg aipolicy.Config) string {
	return catalogCardFingerprintWithContext(e, []contextcatalog.Entry{e}, cfg)
}
func catalogCardFingerprintWithContext(e contextcatalog.Entry, cards []contextcatalog.Entry, cfg aipolicy.Config) string {
	// Only original declarations and immutable source context participate. Model-authored fields are excluded.
	declared := e.DeclaredSummary
	if declared == "" && e.DescriptionSource == "client_declared" {
		declared = e.Summary
	}
	context := make([]any, 0, len(cards))
	for _, other := range cards {
		if !maintenanceAdmits(other, cfg, registeredTagsForCards(cards, cfg)) {
			continue
		}
		declaredOther := other.DeclaredSummary
		if declaredOther == "" && other.DescriptionSource == "client_declared" {
			declaredOther = other.Summary
		}
		context = append(context, originalCatalogEntry(other, declaredOther))
	}
	identity := struct {
		Profile                                                                           aipolicy.Profile `json:"profile"`
		Publish                                                                           string           `json:"publish"`
		MaintenancePolicy                                                                 string           `json:"maintenance_project_policy"`
		RelationshipInstructions                                                          string           `json:"relationship_instructions"`
		Egress                                                                            aipolicy.Egress  `json:"egress"`
		Namespace, Tag, Name, ProjectID, ProjectKey, Owner, ReviewStatus, DeclaredSummary string
		Evidence                                                                          []contextcatalog.Evidence
		OriginalDescription                                                               originalDescription
		OriginalContext                                                                   any `json:"original_context"`
	}{cfg.Profiles[cfg.Maintenance.Profile], cfg.Maintenance.CatalogPublish, cfg.Maintenance.ProjectPolicy, maintenanceRelationshipInstructionsIdentity(), cfg.Egress, e.Namespace, e.Tag, e.Name, e.ProjectID, e.ProjectKey, e.Owner, e.ReviewStatus, declared, e.ClientEvidence, originalDescriptionFor(e), context}
	raw, _ := json.Marshal(identity)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type originalDescription struct {
	Aliases, OwnedComponents, Boundaries, Uses, SharedWith, PositiveExamples, NegativeExamples []string
}
type originalCatalogCard struct {
	Namespace, Tag, Name, ProjectID, ProjectKey, Owner, ReviewStatus, DeclaredSummary string
	Evidence                                                                          []contextcatalog.Evidence
	Description                                                                       originalDescription
}

func originalDescriptionFor(e contextcatalog.Entry) originalDescription {
	if e.DescriptionSource != "client_declared" {
		return originalDescription{[]string{}, []string{}, []string{}, []string{}, []string{}, []string{}, []string{}}
	}
	return originalDescription{e.Aliases, e.OwnedComponents, e.Boundaries, e.Uses, e.SharedWith, e.PositiveExamples, e.NegativeExamples}
}
func originalCatalogEntry(e contextcatalog.Entry, declared string) originalCatalogCard {
	return originalCatalogCard{e.Namespace, e.Tag, e.Name, e.ProjectID, e.ProjectKey, e.Owner, e.ReviewStatus, declared, e.ClientEvidence, originalDescriptionFor(e)}
}
func catalogContextIdentity(cards []contextcatalog.Entry, cfg aipolicy.Config) string {
	original := make([]originalCatalogCard, 0, len(cards))
	registeredTags := registeredTagsForCards(cards, cfg)
	for _, card := range cards {
		if !maintenanceAdmits(card, cfg, registeredTags) {
			continue
		}
		declared := card.DeclaredSummary
		if declared == "" && card.DescriptionSource == "client_declared" {
			declared = card.Summary
		}
		original = append(original, originalCatalogEntry(card, declared))
	}
	raw, _ := json.Marshal(original)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (w *catalogWorker) catalogSourcesCurrent(original []contextcatalog.Entry) bool {
	available, current := w.catalogSourcesState(original)
	return available && current
}

func (w *catalogWorker) catalogSourcesState(original []contextcatalog.Entry) (available, currentSources bool) {
	// Review acceptance depends on admitted original sources, not unrelated
	// changes to the global catalog generation. Publication keeps its own CAS.
	snapshot, _, err := contextcatalog.LoadActive(w.cfg.CatalogDir)
	if err != nil {
		return false, false
	}
	admitted := admittedCatalogEntries(snapshot, w.cfg)
	sort.Slice(admitted, func(i, j int) bool {
		if admitted[i].Namespace == admitted[j].Namespace {
			return admitted[i].Tag < admitted[j].Tag
		}
		return admitted[i].Namespace < admitted[j].Namespace
	})
	restored, _, err := w.restorePublishedOriginals(snapshot, admitted)
	if err != nil {
		return false, false
	}
	return true, sameEntrySlices(restored, original)
}
func (w *catalogWorker) receiptKey(in Input) string {
	fingerprints := make([]string, 0, len(in.Catalog.Entries))
	for _, e := range in.Catalog.Entries {
		fingerprints = append(fingerprints, catalogCardFingerprint(e, w.cfg))
	}
	raw, _ := json.Marshal(struct {
		Profile                  aipolicy.Profile
		Publish                  string
		MaintenancePolicy        string
		RelationshipInstructions string
		Egress                   aipolicy.Egress
		Fingerprints             []string
		Evidence                 []CatalogEvidence
		Context                  string
	}{w.cfg.Profiles[w.cfg.Maintenance.Profile], w.cfg.Maintenance.CatalogPublish, w.cfg.Maintenance.ProjectPolicy, maintenanceRelationshipInstructionsIdentity(), w.cfg.Egress, fingerprints, in.CatalogEvidence, in.contextIdentity})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func catalogDependenciesFor(in Input) []privateCatalogDependency {
	deps := []privateCatalogDependency{}
	for _, e := range in.CatalogEvidence {
		for _, card := range in.Catalog.Entries {
			if card.Namespace == e.Namespace && card.Tag == e.Tag {
				deps = append(deps, privateCatalogDependency{Alias: e.Alias, ProjectID: card.ProjectID, Owner: card.Owner, ProjectKey: card.ProjectKey, Namespace: e.Namespace, Tag: e.Tag, Kind: e.Kind, Digest: e.Digest})
				break
			}
		}
	}
	return deps
}
func cloneFingerprints(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (w *catalogWorker) findReceipt(key string, input Input, current contextcatalog.Snapshot, base string) (privateProposal, string, bool, bool) {
	entries, err := os.ReadDir(filepath.Join(w.proposalWorker.dir, "proposals"))
	if err != nil || len(entries) > maxProposalFiles {
		return privateProposal{}, "", false, false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := aipolicy.ReadPrivate(filepath.Join(w.proposalWorker.dir, "proposals", e.Name()), MaxResponseBytes*4)
		if err != nil || !proposalFilenameMatches(e.Name(), raw) {
			continue
		}
		var p privateProposal
		if json.Unmarshal(raw, &p) != nil || p.CatalogReceiptKey != key || p.SchemaVersion != 1 || p.Proposal.BaseCatalogHash != p.BaseCatalogHash || p.ProviderProtocol != w.cfg.Profiles[w.cfg.Maintenance.Profile].Protocol || p.Model != w.cfg.Profiles[w.cfg.Maintenance.Profile].Model || p.CatalogOriginal == nil || p.CatalogOriginalAll == nil || len(p.CatalogOriginal.Entries) != len(input.Catalog.Entries) || !sameEntrySlices(p.CatalogOriginal.Entries, input.Catalog.Entries) || !sameEntrySlices(p.CatalogOriginalAll.Entries, inputCatalogContext(input)) {
			continue
		}
		validationInput := input
		validationInput.Catalog = *p.CatalogOriginal
		validationInput.baseCatalogHash = p.BaseCatalogHash
		validationInput.contextIdentity = catalogContextIdentity(p.CatalogOriginalAll.Entries, w.cfg)
		if validateProposal(validationInput, p.Proposal) != nil || !sameCatalogDependencies(p.CatalogDependencies, input.CatalogEvidence, input.Catalog.Entries) || !sameCatalogEvidence(makeCatalogEvidence(validationInput.Catalog), input.CatalogEvidence) {
			continue
		}
		if p.BaseCatalogHash == base {
			return p, e.Name(), false, true
		}
		if catalogPublicationMatches(current, p.CatalogPublished) {
			return p, e.Name(), true, true
		}
	}
	return privateProposal{}, "", false, false
}

func inputCatalogContext(input Input) []contextcatalog.Entry { return input.contextEntries }

func sameEntrySlices(a, b []contextcatalog.Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, _ := json.Marshal(a[i])
		y, _ := json.Marshal(b[i])
		if !bytes.Equal(x, y) {
			return false
		}
	}
	return true
}

func proposalFilenameMatches(name string, raw []byte) bool {
	if len(name) != len("proposal-")+64+len(".json") || !strings.HasPrefix(name, "proposal-") || !strings.HasSuffix(name, ".json") {
		return false
	}
	sum := sha256.Sum256(raw)
	return name == "proposal-"+hex.EncodeToString(sum[:])+".json"
}

func catalogPublicationMatches(current contextcatalog.Snapshot, expected []privateCatalogPublished) bool {
	if len(expected) == 0 {
		return false
	}
	for _, item := range expected {
		found := false
		for _, entry := range current.Entries {
			if entry.Namespace == item.Namespace && entry.Tag == item.Tag {
				hash, err := catalogEntryHash(entry)
				found = err == nil && hash == item.EntryHash
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func catalogPublicationChanges(current contextcatalog.Snapshot, expected []privateCatalogPublished) bool {
	for _, item := range expected {
		for _, live := range current.Entries {
			if live.Namespace != item.Namespace || live.Tag != item.Tag {
				continue
			}
			hash, err := catalogEntryHash(live)
			if err == nil && hash != item.EntryHash {
				return true
			}
		}
	}
	return false
}

func (w *catalogWorker) restorePublishedOriginals(current contextcatalog.Snapshot, cards []contextcatalog.Entry) ([]contextcatalog.Entry, map[string]bool, error) {
	cache, err := w.loadProjectionCache()
	if err != nil {
		return nil, nil, err
	}
	// Import still-live receipts once into the durable cache before operators can
	// archive proposal artifacts. The cache itself is independent of that archive.
	cache, changed, err := w.importReceiptProjections(current, cache)
	if err != nil {
		return nil, nil, err
	}
	if changed {
		if err := w.saveProjectionCache(cache); err != nil {
			return nil, nil, errors.New("cannot migrate private catalog source projections")
		}
	}
	out := make([]contextcatalog.Entry, 0, len(cards))
	blocked := map[string]bool{}
	registeredTags := registeredTagsForCards(cards, w.cfg)
	for _, live := range cards {
		entry := live
		record, ok := cache.find(cardKey(entry))
		if ok && cacheRecordMatchesLive(record, entry) {
			entry = record.Original
		} else if entry.DescriptionSource == "model_derived" {
			blocked[cardKey(entry)] = true
			entry.Summary = entry.DeclaredSummary
			entry.Aliases, entry.OwnedComponents, entry.Boundaries = nil, nil, nil
			entry.Uses, entry.SharedWith, entry.PositiveExamples, entry.NegativeExamples = nil, nil, nil, nil
		}
		if w.cfg.Maintenance.ProjectPolicy == "registered" && !maintenanceAdmits(entry, w.cfg, registeredTags) {
			blocked[cardKey(entry)] = true
			continue
		}
		out = append(out, entry)
	}
	return out, blocked, nil
}
func (w *catalogWorker) load() (catalogWorkerState, error) {
	state := catalogWorkerState{SchemaVersion: 1, Fingerprints: map[string]string{}}
	if _, err := os.Lstat(w.statePath); os.IsNotExist(err) {
		return state, nil
	}
	raw, err := aipolicy.ReadPrivate(w.statePath, maxStateBytes)
	if err != nil {
		return state, errors.New("cannot read catalog maintenance state")
	}
	if decodeCatalogState(raw, &state) != nil || !validCatalogStatusState(state) {
		return catalogWorkerState{}, errors.New("catalog maintenance state is invalid")
	}
	return state, nil
}
func (w *catalogWorker) save(s catalogWorkerState) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return errors.New("cannot encode catalog maintenance state")
	}
	if len(raw) > maxStateBytes {
		return errors.New("catalog maintenance state exceeds limit")
	}
	if err := aipolicy.AtomicWrite(filepath.Dir(w.statePath), filepath.Base(w.statePath), raw); err != nil {
		return errors.New("cannot save catalog maintenance state")
	}
	return nil
}
func (w *catalogWorker) recordOutcome(outcome, reason string) {
	s, err := w.load()
	if err != nil {
		return
	}
	s.LastOutcome = outcome
	s.LastReason = reason
	if outcome == "dispatching" {
		s.LastDispatchAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if outcome == "succeeded" || outcome == "proposed" || outcome == "published" {
		s.FailureStreak = 0
		s.NextAllowedAt = ""
	} else {
		if s.FailureStreak < 10000 {
			s.FailureStreak++
		}
		d := time.Duration(w.cfg.Maintenance.IntervalSeconds) * time.Second
		for i := 1; i < s.FailureStreak && d < time.Hour; i++ {
			d *= 2
		}
		if d > time.Hour {
			d = time.Hour
		}
		s.NextAllowedAt = time.Now().Add(d).UTC().Format(time.RFC3339Nano)
	}
	_ = w.save(s)
}

func (w *catalogWorker) resetFailureAfterSuccess() error {
	s, err := w.load()
	if err != nil {
		return err
	}
	if s.LastOutcome == "backoff" || (s.FailureStreak == 0 && s.NextAllowedAt == "") {
		return nil
	}
	s.FailureStreak = 0
	s.NextAllowedAt = ""
	return w.save(s)
}

func finiteCatalogReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case strings.Contains(err.Error(), "budget"):
		return "budget_exhausted"
	case strings.Contains(err.Error(), "state"):
		return "state_unavailable"
	case strings.Contains(err.Error(), "proposal capacity") || strings.Contains(err.Error(), "proposal retention limit"):
		return "proposal_capacity"
	case strings.Contains(err.Error(), "projection"):
		return "state_unavailable"
	case errors.Is(err, errCatalogSourcesChanged):
		return "base_or_dependency_changed"
	case strings.Contains(err.Error(), "catalog"):
		return "catalog_unavailable"
	default:
		return "provider_or_validation_failure"
	}
}

func ReadCatalogStatus(cfg aipolicy.Config) (CatalogStatus, error) {
	cfg.Defaults()
	if cfg.Maintenance.Scope != "catalog_only" && cfg.Maintenance.Scope != "facts_and_catalog" {
		return CatalogStatus{}, errors.New("catalog maintenance status unavailable")
	}
	status := CatalogStatus{Enabled: cfg.Maintenance.Enabled, Scope: cfg.Maintenance.Scope}
	if cfg.Maintenance.Profile != "" {
		status.Model = cfg.Profiles[cfg.Maintenance.Profile].Model
	}
	if !cfg.Maintenance.Enabled || cfg.Maintenance.Scope != "catalog_only" {
		status.Model = cfg.Profiles[cfg.Maintenance.Profile].Model
		return status, nil
	}
	path := filepath.Join(cfg.StateDir, "maintenance-catalog", "state.json")
	if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
		status.LastOutcome = "not_started"
		return status, nil
	}
	raw, err := aipolicy.ReadPrivate(path, maxStateBytes)
	if err != nil {
		return CatalogStatus{}, errors.New("catalog maintenance status unavailable")
	}
	var state catalogWorkerState
	if decodeCatalogState(raw, &state) != nil || !validCatalogStatusState(state) {
		return CatalogStatus{}, errors.New("catalog maintenance status invalid")
	}
	status.LastDispatchAt = state.LastDispatchAt
	status.LastOutcome = state.LastOutcome
	status.ReasonCode = state.LastReason
	status.Queued = state.LastQueued
	status.Completed = state.LastCompleted
	status.Skipped = state.LastSkipped
	return status, nil
}

func decodeCatalogState(raw []byte, state *catalogWorkerState) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(state); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing state data")
	}
	return nil
}

func validCatalogStatusState(s catalogWorkerState) bool {
	if s.SchemaVersion != 1 || s.Fingerprints == nil || len(s.Fingerprints) > 32 || s.LastQueued < 0 || s.LastQueued > 32 || s.LastCompleted < 0 || s.LastCompleted > s.LastQueued || s.LastSkipped < 0 || s.LastSkipped > 32 || s.FailureStreak < 0 || s.FailureStreak > 10000 {
		return false
	}
	if s.Cursor != "" && (len(s.Cursor) > 256 || !strings.HasPrefix(s.Cursor, "projects/")) {
		return false
	}
	allowedOutcomes := map[string]bool{"": true, "not_started": true, "dispatching": true, "proposed": true, "published": true, "conflict_or_unpublished": true, "no_changes": true, "skipped": true, "empty_catalog": true, "no_eligible_cards": true, "backoff": true, "failed": true}
	allowedReasons := map[string]bool{"": true, "no_changes": true, "evidence_unavailable": true, "source_projection_unavailable": true, "no_eligible_cards": true, "backoff_active": true, "base_or_dependency_changed": true, "insufficient_evidence": true, "unresolved_proposal": true, "canceled": true, "timeout": true, "budget_exhausted": true, "catalog_unavailable": true, "state_unavailable": true, "provider_or_validation_failure": true, "proposal_capacity": true}
	if !allowedOutcomes[s.LastOutcome] || !allowedReasons[s.LastReason] {
		return false
	}
	for k, v := range s.Fingerprints {
		if len(k) > 256 || len(v) != 64 {
			return false
		}
		if _, err := hex.DecodeString(v); err != nil {
			return false
		}
	}
	for _, value := range []string{s.LastDispatchAt, s.NextAllowedAt} {
		if value != "" {
			if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
				return false
			}
		}
	}
	return true
}
