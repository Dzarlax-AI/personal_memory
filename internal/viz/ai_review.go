package viz

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aimaintenance"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/go-chi/chi/v5"
)

const reviewScanLimit = 10000

var reviewSHA = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (h *Handler) withReviewNoStore(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Cache-Control", "no-store"); next(w, r) }
}

// WithAIReview adds read-only review APIs and a separately protected local assessment store.
// publicOrigin is the operator-configured browser origin; proxy headers are never trusted.
func (h *Handler) WithAIReview(cfg aipolicy.Config, publicOrigin string) *Handler {
	c := cfg
	if !validConfiguredReviewOrigin(publicOrigin) {
		publicOrigin = ""
	}
	h.aiReviewConfig, h.aiReviewOrigin = &c, strings.TrimSuffix(publicOrigin, "/")
	return h
}

type aiReviewStatus struct {
	Availability       string `json:"availability"`
	Model              string `json:"model,omitempty"`
	Mode               string `json:"mode"`
	Publication        string `json:"publication"`
	SchedulerEnabled   bool   `json:"scheduler_enabled"`
	AssessmentWritable bool   `json:"assessment_writable"`
	History            string `json:"history"`
}

func (h *Handler) apiAIReviewStatus(w http.ResponseWriter, r *http.Request) {
	cfg := *h.aiReviewConfig
	mode, model := "disabled", ""
	if cfg.Maintenance.Scope != "" {
		mode = cfg.Maintenance.Scope
	}
	if p, ok := cfg.Profiles[cfg.Maintenance.Profile]; ok {
		model = p.Model
	}
	if cfg.Maintenance.Enabled {
		if mode == "disabled" {
			mode = "catalog_only"
		}
	}
	page, err := aimaintenance.ReadRunHistory(cfg, 1, "", "")
	if err != nil {
		writeInternalError(w, "unable to load review status")
		return
	}
	writeNoStoreJSON(w, aiReviewStatus{Availability: aiReviewAvailability(cfg), Model: model, Mode: mode, Publication: cfg.Maintenance.CatalogPublish, SchedulerEnabled: cfg.Maintenance.Enabled, AssessmentWritable: h.reviewWritable(), History: page.Availability})
}

func aiReviewAvailability(c aipolicy.Config) string {
	if strings.TrimSpace(c.StateDir) == "" {
		return "unavailable"
	}
	if c.Maintenance.Scope != "catalog_only" {
		return "unsupported_scope"
	}
	return "available"
}

func (h *Handler) reviewWritable() bool {
	return h.aiReviewConfig != nil && h.aiReviewOrigin != "" && strings.TrimSpace(h.aiReviewConfig.StateDir) != "" && h.aiReviewConfig.Maintenance.Scope == "catalog_only"
}

type aiReviewRunsResponse struct {
	Runs         []aimaintenance.HistoryRun `json:"runs"`
	NextCursor   string                     `json:"next_cursor,omitempty"`
	Availability string                     `json:"availability"`
	Complete     bool                       `json:"complete"`
	Scanned      int                        `json:"scanned"`
}

func (h *Handler) apiAIReviewRuns(w http.ResponseWriter, r *http.Request) {
	if !oneValue(r.URL.Query(), "limit", "cursor", "from", "to") {
		http.Error(w, "duplicate query parameter", 400)
		return
	}
	limit, err := reviewLimit(r.URL.Query().Get("limit"))
	if err != nil {
		http.Error(w, "invalid limit", http.StatusBadRequest)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	from, err := parseReviewTime(r.URL.Query().Get("from"))
	if err != nil {
		http.Error(w, "invalid from", 400)
		return
	}
	to, err := parseReviewTime(r.URL.Query().Get("to"))
	if err != nil {
		http.Error(w, "invalid to", 400)
		return
	}
	if from != nil && to != nil && from.After(*to) {
		http.Error(w, "from must not be after to", 400)
		return
	}
	cfg := *h.aiReviewConfig
	response := aiReviewRunsResponse{Runs: []aimaintenance.HistoryRun{}, Availability: "unavailable", Complete: true}
	if strings.TrimSpace(cfg.StateDir) == "" {
		writeNoStoreJSON(w, response)
		return
	}
	seen := 0
	for seen < reviewScanLimit {
		page, e := aimaintenance.ReadRunHistory(cfg, 100, cursor, "")
		if e != nil {
			writeInternalError(w, "unable to load review history")
			return
		}
		response.Availability = page.Availability
		if page.Availability != "available" {
			break
		}
		for index, run := range page.Runs {
			seen++
			response.Scanned++
			lastScanned := run.RunID
			startedText := ""
			if run.Started != nil {
				startedText = run.Started.StartedAt
			}
			started, parseErr := time.Parse(time.RFC3339Nano, startedText)
			if parseErr != nil && len(run.RunID) >= 25 {
				started, parseErr = time.Parse("20060102T150405000000000Z", run.RunID[:25])
			}
			if parseErr == nil && ((from != nil && started.Before(*from)) || (to != nil && started.After(*to))) {
				continue
			}
			response.Runs = append(response.Runs, run)
			if len(response.Runs) == limit {
				if index < len(page.Runs)-1 || page.NextCursor != "" {
					response.NextCursor = lastScanned
				} else {
					response.NextCursor = ""
				}
				break
			}
		}
		if len(response.Runs) == limit {
			break
		}
		if page.NextCursor == "" {
			response.NextCursor = ""
			response.Complete = true
			break
		}
		cursor = page.NextCursor
		response.NextCursor = cursor
	}
	if seen >= reviewScanLimit && response.NextCursor != "" {
		response.Complete = false
	}
	if response.NextCursor != "" {
		response.Complete = false
	}
	writeNoStoreJSON(w, response)
}

func reviewLimit(s string) (int, error) {
	if s == "" {
		return 20, nil
	}
	n, e := strconv.Atoi(s)
	if e != nil || n < 1 || n > 100 {
		return 0, errors.New("invalid limit")
	}
	return n, nil
}

func parseReviewTime(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, e := time.Parse(time.RFC3339, s)
	if e != nil {
		return nil, e
	}
	u := t.UTC()
	return &u, nil
}

func oneValue(q url.Values, keys ...string) bool {
	for _, k := range keys {
		if v, ok := q[k]; ok && len(v) != 1 {
			return false
		}
	}
	return true
}

func (h *Handler) apiAIReviewRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "run_id")
	if !aimaintenance.ValidRunID(runID) {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}
	page, err := aimaintenance.ReadRunHistory(*h.aiReviewConfig, 1, "", runID)
	if err != nil {
		writeInternalError(w, "unable to load review run")
		return
	}
	if len(page.Runs) == 0 {
		http.NotFound(w, r)
		return
	}
	writeNoStoreJSON(w, map[string]any{"availability": page.Availability, "run": page.Runs[0]})
}

type reviewEntry struct {
	Namespace        string            `json:"namespace"`
	Tag              string            `json:"tag"`
	Name             string            `json:"name"`
	Summary          string            `json:"summary"`
	Aliases          []string          `json:"aliases"`
	OwnedComponents  []string          `json:"owned_components"`
	Boundaries       []string          `json:"boundaries"`
	Uses             []string          `json:"uses"`
	SharedWith       []string          `json:"shared_with"`
	PositiveExamples []string          `json:"positive_examples"`
	NegativeExamples []string          `json:"negative_examples"`
	ReviewStatus     string            `json:"review_status"`
	ClientEvidence   []reviewEvidence  `json:"client_evidence"`
	EvidenceRefs     []reviewReference `json:"evidence_refs"`
}
type reviewReference struct {
	Ref       string `json:"ref"`
	Kind      string `json:"kind,omitempty"`
	Digest    string `json:"digest,omitempty"`
	Available bool   `json:"available"`
}
type reviewProject struct {
	ProjectID         string       `json:"project_id"`
	Namespace         string       `json:"namespace"`
	Tag               string       `json:"tag"`
	Original          *reviewEntry `json:"original"`
	Base              *reviewEntry `json:"base"`
	Proposed          reviewEntry  `json:"proposed"`
	Current           *reviewEntry `json:"current,omitempty"`
	BaseEntryHash     string       `json:"base_entry_hash"`
	ProposedEntryHash string       `json:"proposed_entry_hash"`
	CurrentEntryHash  string       `json:"current_entry_hash"`
	Drift             bool         `json:"drift"`
	IdentityStatus    string       `json:"identity_status"`
}
type reviewEvidence struct {
	Alias     string `json:"alias,omitempty"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Digest    string `json:"digest,omitempty"`
	Available bool   `json:"available"`
}
type reviewCatalogDependency struct {
	Alias      string `json:"alias"`
	ProjectID  string `json:"project_id"`
	Owner      string `json:"owner"`
	ProjectKey string `json:"project_key"`
	Namespace  string `json:"namespace"`
	Tag        string `json:"tag"`
	Kind       string `json:"kind"`
	Digest     string `json:"digest"`
}
type aiReviewDraft struct {
	Availability       string           `json:"availability"`
	DraftStatus        string           `json:"draft_status"`
	AssessmentWritable bool             `json:"assessment_writable"`
	RunID              string           `json:"run_id"`
	SHA256             string           `json:"sha256"`
	Scope              string           `json:"scope"`
	BaseCatalogHash    string           `json:"base_catalog_hash"`
	CurrentCatalogHash string           `json:"current_catalog_hash,omitempty"`
	CurrentStatus      string           `json:"current_status"`
	Projects           []reviewProject  `json:"projects"`
	Evidence           []reviewEvidence `json:"evidence"`
	Missing            []string         `json:"missing"`
}

func (h *Handler) apiAIReviewDraft(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "run_id")
	if !aimaintenance.ValidRunID(runID) {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}
	draft, _, err := h.loadAIDraft(runID, chi.URLParam(r, "sha256"))
	if err != nil {
		http.Error(w, "draft unavailable", http.StatusNotFound)
		return
	}
	writeNoStoreJSON(w, draft)
}

func (h *Handler) loadAIDraft(runID, digest string) (aiReviewDraft, *aimaintenance.HistoryRun, error) {
	out := aiReviewDraft{Availability: "unavailable", RunID: runID, SHA256: digest, Projects: []reviewProject{}, Evidence: []reviewEvidence{}, Missing: []string{}}
	if !reviewSHA.MatchString(digest) {
		return out, nil, errors.New("invalid digest")
	}
	page, err := aimaintenance.ReadRunHistory(*h.aiReviewConfig, 1, "", runID)
	if err != nil || len(page.Runs) != 1 {
		return out, nil, errors.New("run unavailable")
	}
	run := page.Runs[0]
	linked := false
	filename := "proposal-" + digest + ".json"
	for _, ref := range run.Drafts {
		if ref.Filename == filename && ref.SHA256 == digest {
			linked = true
			break
		}
	}
	if !linked {
		return out, &run, errors.New("draft is not linked")
	}
	if run.Started == nil || run.Started.Scope != "catalog_only" {
		out.Availability = "unsupported_scope"
		if run.Started != nil {
			out.Scope = run.Started.Scope
		}
		out.Missing = append(out.Missing, "scope_not_supported")
		return out, &run, nil
	}
	path := filepath.Join(h.aiReviewConfig.StateDir, "maintenance", "proposals", filename)
	if !existingSafeDir(filepath.Join(h.aiReviewConfig.StateDir, "maintenance")) || !existingSafeDir(filepath.Join(h.aiReviewConfig.StateDir, "maintenance", "proposals")) {
		return out, &run, errors.New("proposal directory unavailable")
	}
	raw, err := aipolicy.ReadPrivate(path, aimaintenance.MaxResponseBytes*4)
	if err != nil {
		return out, &run, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		return out, &run, errors.New("digest mismatch")
	}
	var p reviewProposal
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil {
		return out, &run, errors.New("invalid proposal")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return out, &run, errors.New("invalid proposal ending")
	}
	if p.SchemaVersion != 1 || p.BaseCatalogHash == "" || p.Proposal.SchemaVersion != 1 || p.Proposal.BaseCatalogHash != p.BaseCatalogHash || p.CatalogOriginalAll == nil || p.CatalogOriginal == nil {
		return out, &run, errors.New("invalid proposal binding")
	}
	if run.Started.CatalogHash != p.BaseCatalogHash {
		return out, &run, errors.New("run binding mismatch")
	}
	if len(p.Proposal.Grouping) != 0 {
		return out, &run, errors.New("catalog-only proposal contains grouping changes")
	}
	for _, code := range p.Proposal.MissingEvidence {
		if !aimaintenance.ValidMissingEvidenceCode(code) {
			return out, &run, errors.New("proposal contains an unknown missing-evidence code")
		}
	}
	if p.Proposal.Catalog != nil {
		for _, entry := range p.Proposal.Catalog.Entries {
			if entry.ReviewStatus != "draft" && entry.ReviewStatus != "unresolved" {
				return out, &run, errors.New("proposal catalog contains a reviewed entry")
			}
		}
	}
	if err := contextcatalog.Validate(*p.CatalogOriginalAll); err != nil {
		return out, &run, err
	}
	if err := contextcatalog.Validate(*p.CatalogOriginal); err != nil {
		return out, &run, err
	}
	deps := []reviewCatalogDependency{}
	if len(p.CatalogDependencies) > 0 && json.Unmarshal(p.CatalogDependencies, &deps) != nil {
		return out, &run, errors.New("catalog evidence references unavailable")
	}
	evidenceByAlias := map[string]reviewEvidence{}
	for _, entry := range p.CatalogOriginal.Entries {
		if !contextcatalog.RegisteredIdentity(entry) || !contextcatalog.Eligible(entry) || entry.Namespace != "projects" {
			continue
		}
		texts := []struct{ kind, text string }{}
		if entry.DeclaredSummary != "" {
			texts = append(texts, struct{ kind, text string }{"user_declared", entry.DeclaredSummary})
		} else if entry.DescriptionSource == "client_declared" {
			texts = append(texts, struct{ kind, text string }{"user_declared", entry.Summary})
		}
		for _, ev := range entry.ClientEvidence {
			texts = append(texts, struct{ kind, text string }{ev.Kind, ev.Text})
		}
		for _, item := range texts {
			alias := "c" + fmt.Sprintf("%03d", len(evidenceByAlias))
			if len(evidenceByAlias) >= 32 {
				break
			}
			sum := sha256.Sum256([]byte(item.text))
			digest := hex.EncodeToString(sum[:])
			for _, dep := range deps {
				if dep.Alias == alias && dep.ProjectID == entry.ProjectID && dep.Namespace == entry.Namespace && dep.Tag == entry.Tag && dep.Kind == item.kind && dep.Digest == digest && dep.Owner == entry.Owner && dep.ProjectKey == entry.ProjectKey {
					evidenceByAlias[alias] = reviewEvidence{Alias: alias, Kind: item.kind, Text: item.text, Digest: digest, Available: true}
					break
				}
			}
			if _, exists := evidenceByAlias[alias]; !exists {
				// Preserve the worker's global cNNN sequence even when a dependency
				// was omitted or cannot be matched to this registered source.
				evidenceByAlias[alias] = reviewEvidence{Alias: alias, Kind: item.kind, Digest: digest, Available: false}
			}
		}
	}
	baseSnapshot, err := contextcatalog.LoadSnapshot(h.aiReviewConfig.CatalogDir, p.BaseCatalogHash)
	baseAvailable := err == nil
	if baseAvailable {
		if err := contextcatalog.Validate(baseSnapshot); err != nil {
			return out, &run, errors.New("base snapshot invalid")
		}
	} else {
		out.Missing = append(out.Missing, "base_snapshot_unavailable")
	}
	out.Availability = "available"
	out.DraftStatus = "proposal"
	out.Scope = "catalog_only"
	out.BaseCatalogHash = p.BaseCatalogHash
	out.AssessmentWritable = h.reviewWritable() && baseAvailable && p.Proposal.Catalog != nil
	if p.Proposal.Catalog == nil {
		out.DraftStatus = "no_changes"
		if current, currentHash, currentErr := contextcatalog.LoadActive(h.aiReviewConfig.CatalogDir); currentErr == nil {
			_ = current
			out.CurrentCatalogHash = currentHash
			out.CurrentStatus = "available"
		} else {
			out.CurrentStatus = "missing"
			out.Missing = append(out.Missing, "current_catalog_unavailable")
		}
		out.Missing = append(out.Missing, p.Proposal.MissingEvidence...)
		return out, &run, nil
	}
	if err := contextcatalog.Validate(*p.Proposal.Catalog); err != nil {
		return out, &run, err
	}
	if p.Proposal.Catalog.ParentHash != p.BaseCatalogHash || len(p.Proposal.Catalog.Entries) != len(p.CatalogOriginal.Entries) {
		return out, &run, errors.New("invalid proposal source binding")
	}
	for _, proposed := range p.Proposal.Catalog.Entries {
		if proposed.ProjectID != "" || proposed.ProjectKey != "" || proposed.Owner != "" || len(proposed.ClientEvidence) != 0 || proposed.DescriptionSource != "" || proposed.DeclaredSummary != "" {
			return out, &run, errors.New("proposal contains source identity metadata")
		}
	}
	if _, err := contextcatalog.Hash(*p.Proposal.Catalog); err != nil {
		return out, &run, err
	}
	baseByID := map[string]contextcatalog.Entry{}
	sourceByID := map[string]contextcatalog.Entry{}
	proposedByTag := map[string]contextcatalog.Entry{}
	for _, e := range baseSnapshot.Entries {
		if e.ProjectID != "" {
			baseByID[e.ProjectID] = e
		}
	}
	for _, e := range p.CatalogOriginal.Entries {
		if e.ProjectID != "" {
			if _, exists := sourceByID[e.ProjectID]; exists {
				return out, &run, errors.New("duplicate source identity")
			}
			sourceByID[e.ProjectID] = e
		}
	}
	for _, e := range p.Proposal.Catalog.Entries {
		key := e.Namespace + "\x00" + e.Tag
		if _, exists := proposedByTag[key]; exists {
			return out, &run, errors.New("duplicate proposal source")
		}
		proposedByTag[key] = e
	}
	current, currentHash, e := contextcatalog.LoadActive(h.aiReviewConfig.CatalogDir)
	if e == nil {
		out.CurrentCatalogHash = currentHash
		out.CurrentStatus = "available"
	} else {
		out.CurrentStatus = "unavailable"
		if errors.Is(e, contextcatalog.ErrNotFound) {
			out.CurrentStatus = "missing"
		}
		out.Missing = append(out.Missing, "current_catalog_unavailable")
	}
	currentByID := map[string]contextcatalog.Entry{}
	for _, e := range current.Entries {
		if e.ProjectID != "" {
			currentByID[e.ProjectID] = e
		}
	}
	ids := make([]string, 0, len(sourceByID))
	for id := range sourceByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		src := sourceByID[id]
		q, qok := proposedByTag[src.Namespace+"\x00"+src.Tag]
		b, bok := baseByID[id]
		identityStatus := "verified"
		if !contextcatalog.RegisteredIdentity(src) || !bok || !contextcatalog.SameReviewSourceIdentity(b, src) {
			identityStatus = "mismatch"
			out.Missing = append(out.Missing, "source_identity_mismatch")
		}
		if !qok || q.Namespace != src.Namespace || q.Tag != src.Tag || q.Name != src.Name {
			identityStatus = "proposal_unavailable"
			out.Missing = append(out.Missing, "proposal_source_unavailable")
		}
		bh := ""
		var baseSafe *reviewEntry
		if bok && identityStatus == "verified" {
			bh = entryHash(b)
			safe := safeReviewEntry(b)
			baseSafe = &safe
		}
		qh := entryHash(q)
		ch := ""
		pr := reviewProject{ProjectID: id, Namespace: src.Namespace, Tag: src.Tag, Base: baseSafe, BaseEntryHash: bh, CurrentEntryHash: ch, Drift: !bok || identityStatus != "verified", IdentityStatus: identityStatus}
		if identityStatus == "verified" {
			originalSafe := safeReviewEntry(src)
			pr.Original = &originalSafe
		}
		if qok && q.Namespace == src.Namespace && q.Tag == src.Tag && q.Name == src.Name {
			pr.Proposed = safeReviewEntry(q)
			pr.Proposed.EvidenceRefs = safeReviewReferences(q.EvidenceRefs, evidenceByAlias)
			pr.ProposedEntryHash = qh
		}
		if cur, ok := currentByID[id]; ok && identityStatus == "verified" {
			if contextcatalog.SameStableProjectIdentity(cur, src) {
				safe := safeReviewEntry(cur)
				pr.Current = &safe
				ch = entryHash(cur)
			} else {
				pr.Drift = true
			}
		}
		pr.CurrentEntryHash = ch
		if bok && identityStatus == "verified" {
			pr.Drift = pr.Drift || ch != bh
		}
		out.Projects = append(out.Projects, pr)
		if identityStatus == "verified" {
			for _, ev := range src.ClientEvidence {
				out.Evidence = append(out.Evidence, reviewEvidence{Kind: ev.Kind, Text: ev.Text, Available: true})
			}
			for _, ref := range pr.Proposed.EvidenceRefs {
				if ev, ok := evidenceByAlias[ref.Ref]; ok && ev.Available {
					out.Evidence = append(out.Evidence, ev)
				}
			}
		}
	}
	out.Missing = append(out.Missing, p.Proposal.MissingEvidence...)
	if len(out.Evidence) == 0 {
		out.Missing = append(out.Missing, "source_evidence_unavailable")
	}
	return out, &run, nil
}

type reviewProposal struct {
	SchemaVersion   int    `json:"schema_version"`
	BaseCatalogHash string `json:"base_catalog_hash"`
	Proposal        struct {
		SchemaVersion   int                              `json:"schema_version"`
		BaseCatalogHash string                           `json:"base_catalog_hash"`
		Catalog         *contextcatalog.Snapshot         `json:"catalog"`
		MissingEvidence []string                         `json:"missing_evidence"`
		Grouping        []aimaintenance.GroupingProposal `json:"grouping"`
	} `json:"proposal"`
	CatalogOriginal          *contextcatalog.Snapshot `json:"catalog_original"`
	CatalogOriginalAll       *contextcatalog.Snapshot `json:"catalog_original_context"`
	ProviderProtocol         string                   `json:"provider_protocol"`
	Model                    string                   `json:"model"`
	MaintenancePolicy        string                   `json:"maintenance_project_policy"`
	RelationshipInstructions string                   `json:"relationship_instructions"`
	BindingHash              string                   `json:"binding_hash"`
	Dependencies             json.RawMessage          `json:"dependencies"`
	CatalogDependencies      json.RawMessage          `json:"catalog_dependencies"`
	Manifest                 json.RawMessage          `json:"grouping_manifest"`
	EvidenceInferenceKey     string                   `json:"evidence_inference_key"`
	CatalogReceiptKey        string                   `json:"catalog_receipt_key"`
	CatalogPublished         json.RawMessage          `json:"catalog_published"`
	CreatedAt                string                   `json:"created_at"`
}

func entryHash(e contextcatalog.Entry) string {
	b, _ := json.Marshal(e)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func safeReviewReferences(refs []string, evidence map[string]reviewEvidence) []reviewReference {
	out := make([]reviewReference, 0, len(refs))
	for _, ref := range refs {
		if strings.HasPrefix(ref, "c") && len(ref) == 4 && ref[1] >= '0' && ref[1] <= '9' && ref[2] >= '0' && ref[2] <= '9' && ref[3] >= '0' && ref[3] <= '9' {
			if ev, ok := evidence[ref]; ok {
				out = append(out, reviewReference{Ref: ref, Kind: ev.Kind, Digest: ev.Digest, Available: ev.Available})
				continue
			}
			out = append(out, reviewReference{Ref: ref})
			continue
		}
		parts := strings.SplitN(ref, ":", 2)
		allowedKind := len(parts) == 2 && map[string]bool{"user_declared": true, "client_readme": true, "client_agents": true, "declared_summary": true}[parts[0]]
		if allowedKind && reviewSHA.MatchString(parts[1]) {
			found := reviewReference{Ref: ref, Kind: parts[0], Digest: parts[1]}
			for _, ev := range evidence {
				if ev.Kind == parts[0] && ev.Digest == parts[1] && ev.Available {
					found.Available = true
					break
				}
			}
			out = append(out, found)
			continue
		}
		out = append(out, reviewReference{Ref: "unavailable", Available: false})
	}
	return out
}
func safeReviewEntry(e contextcatalog.Entry) reviewEntry {
	out := reviewEntry{Namespace: e.Namespace, Tag: e.Tag, Name: e.Name, Summary: e.Summary, Aliases: e.Aliases, OwnedComponents: e.OwnedComponents, Boundaries: e.Boundaries, Uses: e.Uses, SharedWith: e.SharedWith, PositiveExamples: e.PositiveExamples, NegativeExamples: e.NegativeExamples, ReviewStatus: e.ReviewStatus, ClientEvidence: []reviewEvidence{}, EvidenceRefs: []reviewReference{}}
	for _, x := range e.ClientEvidence {
		out.ClientEvidence = append(out.ClientEvidence, reviewEvidence{Kind: x.Kind, Text: x.Text, Available: true})
	}
	return out
}

type assessmentRequest = aimaintenance.ReviewAssessmentRequest
type assessmentView struct {
	aimaintenance.ReviewAssessment
	Drift bool `json:"drift"`
}
type assessmentList struct {
	Assessments []assessmentView `json:"assessments"`
}

func (h *Handler) apiAIReviewAssessments(w http.ResponseWriter, r *http.Request) {
	runID, digest := chi.URLParam(r, "run_id"), chi.URLParam(r, "sha256")
	if !aimaintenance.ValidRunID(runID) {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}
	draft, run, err := h.loadAIDraft(runID, digest)
	if err != nil {
		http.Error(w, "draft unavailable", 404)
		return
	}
	_ = run
	records, err := aimaintenance.ReadReviewAssessments(*h.aiReviewConfig, runID, digest)
	if err != nil {
		writeInternalError(w, "unable to load assessments")
		return
	}
	items := make([]assessmentView, 0, len(records))
	for _, record := range records {
		view := assessmentView{ReviewAssessment: record}
		view.Drift = assessmentDrift(view, draft)
		items = append(items, view)
	}
	writeNoStoreJSON(w, assessmentList{Assessments: items})
}

func (h *Handler) apiAIReviewSaveAssessment(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "run_id")
	if !aimaintenance.ValidRunID(runID) {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !h.reviewWritable() || !validReviewOrigin(r, h.aiReviewOrigin) || mediaErr != nil || mediaType != "application/json" {
		http.Error(w, "assessment write unavailable", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req assessmentRequest
	if dec.Decode(&req) != nil {
		http.Error(w, "invalid assessment", 400)
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		http.Error(w, "invalid assessment", 400)
		return
	}
	if req.ExpectedRevision < 0 || !map[string]bool{"acceptable": true, "needs_correction": true, "rejected": true}[req.Verdict] || !map[string]bool{"none": true, "source_mismatch": true, "proposal_error": true, "insufficient_evidence": true, "identity_mismatch": true, "other": true}[req.Reason] || len([]byte(req.Note)) > 2048 || !utf8Valid(req.Note) || !reviewSHA.MatchString(req.BaseEntryHash) || !reviewSHA.MatchString(req.ProposedEntryHash) || (req.CurrentEntryHash != "" && !reviewSHA.MatchString(req.CurrentEntryHash)) {
		http.Error(w, "invalid assessment", 400)
		return
	}
	runID, digest, id := chi.URLParam(r, "run_id"), chi.URLParam(r, "sha256"), chi.URLParam(r, "project_id")
	if !reviewSHA.MatchString(id) {
		http.Error(w, "invalid project id", 400)
		return
	}
	draft, _, err := h.loadAIDraft(runID, digest)
	if err != nil {
		http.Error(w, "draft unavailable", 404)
		return
	}
	var p *reviewProject
	for i := range draft.Projects {
		if draft.Projects[i].ProjectID == id {
			p = &draft.Projects[i]
			break
		}
	}
	if p == nil {
		http.NotFound(w, r)
		return
	}
	if p.BaseEntryHash == "" {
		http.Error(w, "base snapshot unavailable", http.StatusConflict)
		return
	}
	if req.ProposedEntryHash != p.ProposedEntryHash || req.BaseEntryHash != p.BaseEntryHash || req.CurrentEntryHash != p.CurrentEntryHash {
		http.Error(w, "assessment binding mismatch or catalog drift", 409)
		return
	}
	result, conflict, err := aimaintenance.SaveReviewAssessment(*h.aiReviewConfig, runID, digest, id, req)
	if err != nil {
		writeInternalError(w, "unable to save assessment")
		return
	}
	if conflict {
		writeNoStoreJSONStatus(w, 409, map[string]any{"error": "assessment revision conflict"})
		return
	}
	writeNoStoreJSON(w, result)
}

func validReviewOrigin(r *http.Request, configured string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || origin != configured {
		return false
	}
	u, e := url.Parse(origin)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return false
	}
	if u.Scheme == "https" && u.Host == r.Host {
		return true
	}
	if u.Scheme != "http" || u.Host != r.Host {
		return false
	}
	host, _, e := net.SplitHostPort(u.Host)
	if e != nil {
		host = u.Host
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func validConfiguredReviewOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Host
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func assessmentDrift(a assessmentView, d aiReviewDraft) bool {
	for _, p := range d.Projects {
		if p.ProjectID == a.ProjectID {
			return p.ProposedEntryHash != a.Request.ProposedEntryHash || p.BaseEntryHash != a.Request.BaseEntryHash || p.CurrentEntryHash != a.Request.CurrentEntryHash
		}
	}
	return true
}
func existingSafeDir(path string) bool {
	i, e := os.Lstat(path)
	return e == nil && i.IsDir() && i.Mode()&os.ModeSymlink == 0
}
func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "\uFFFD") == s }
func safeExistingDir(path string) bool {
	i, e := os.Lstat(path)
	return e == nil && i.IsDir() && i.Mode()&os.ModeSymlink == 0
}
func writeNoStoreJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, v)
}
func writeNoStoreJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
