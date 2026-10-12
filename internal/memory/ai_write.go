package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
)

type aiGrouping struct {
	Tags    []string `json:"tags"`
	Primary string   `json:"primary_tag"`
}
type aiWriteDecision struct {
	subject                *FactSubject
	runtime                *aiRuntime
	outcome                *AIOutcome
	before, after          aiGrouping
	missingContextCodes    []string
	recommendedRelatedTags []string
	journal                *aiWriteJournal
	reason                 string
	durationMS             int64
}
type aiWriteJournal struct {
	Owner                  string     `json:"owner,omitempty"`
	InputFingerprint       string     `json:"input_fingerprint,omitempty"`
	RuntimeInstance        string     `json:"runtime_instance,omitempty"`
	PendingUntil           string     `json:"pending_until,omitempty"`
	MissingContextCodes    []string   `json:"missing_context_codes,omitempty"`
	SubjectDecision        string     `json:"subject_decision,omitempty"`
	ReasonCode             string     `json:"reason_code,omitempty"`
	DurationMS             int64      `json:"duration_ms"`
	SchemaVersion          int        `json:"schema_version"`
	OperationRef           string     `json:"operation_ref"`
	PointID                string     `json:"point_id"`
	CatalogHash            string     `json:"catalog_hash,omitempty"`
	Profile                string     `json:"profile"`
	Model                  string     `json:"model"`
	RubricVersion          string     `json:"rubric_version"`
	Mode                   string     `json:"mode"`
	Status                 string     `json:"status"`
	CreatedAt              string     `json:"created_at"`
	Before                 aiGrouping `json:"before"`
	After                  aiGrouping `json:"after"`
	RecommendedRelatedTags []string   `json:"recommended_related_tags,omitempty"`
	Retention              string     `json:"retention"`
}

func (s *Server) aiClassify(ctx context.Context, fact, namespace string, tags []string, primary string, origin *FactOrigin, subject *FactSubject, allowAI bool) *aiWriteDecision {
	a := s.aiState()
	if a == nil || !a.cfg.Write.Active() || namespace != "projects" || primary != "" || subjectDecision(namespace, subject) != "" {
		return nil
	}
	if a.cfg.Write.Admission == "client_opt_in" && !allowAI {
		return nil
	}
	d := &aiWriteDecision{runtime: a, outcome: &AIOutcome{Mode: a.cfg.Write.Mode, Status: "unavailable"}, before: aiGrouping{append([]string{}, tags...), primary}}
	d.after = d.before
	if subject != nil {
		copy := *subject
		d.subject = &copy
	}
	if a.cfg.Write.Mode == "shadow" {
		d.outcome.Status = "shadow"
		return d
	}
	timeout, cancel := context.WithTimeout(ctx, aiOperationTimeout)
	defer cancel()
	stop := context.AfterFunc(a.ctx, cancel)
	defer stop()
	d.classify(timeout, fact, namespace, origin)
	return d
}

func (d *aiWriteDecision) classify(ctx context.Context, fact, namespace string, origin *FactOrigin) {
	a := d.runtime
	if ctx.Err() != nil {
		d.reason = aijudgment.FailureReason(ctx.Err())
		return
	}
	if a.ctx.Err() != nil {
		d.reason = "canceled"
		return
	}
	catalog, hash, reason := a.catalogWithReason()
	if reason != "" {
		d.reason = reason
		return
	}
	if !a.allowsCatalogScope(catalog, namespace, d.before.Tags) || (origin != nil && !a.allowsCatalogScope(catalog, "projects", []string{origin.SourceProject})) {
		d.reason = "egress_denied"
		return
	}
	if d.journal != nil && d.journal.SchemaVersion == 3 && d.journal.CatalogHash != hash {
		d.reason = "catalog_changed"
		return
	}
	d.outcome.CatalogHash = hash
	in := aijudgment.ClassifyInput{FactText: fact, Namespace: namespace, Catalog: catalog}
	if d.subject != nil {
		in.SubjectScope = d.subject.Scope
		in.SubjectContext = d.subject.Context
	}
	// Recording origin is stored provenance, not evidence of the assertion's subject.
	// Both synchronous and shadow classification omit it from provider input.
	p := a.cfg.Profiles[a.cfg.Write.Profile]
	if _, err := aijudgment.BuildClassifyRequest(a.cfg.Write.Provider, p.Model, in); err != nil {
		d.reason = "invalid_request"
		return
	}
	if ctx.Err() != nil {
		d.reason = aijudgment.FailureReason(ctx.Err())
		return
	}
	reservation, err := a.budget.Begin(time.Now())
	if err != nil {
		d.reason = "budget_unavailable"
		return
	}
	result, usage, err := a.write.Classify(ctx, in)
	reservation.Finish(usage.InputTokens, usage.Known)
	if err != nil || ctx.Err() != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		d.reason = aijudgment.FailureReason(err)
		return
	}
	if result.Status == "abstained" {
		if d.journal != nil && (!recommendationSubjectAllowed(result.SubjectDecision) || result.SubjectDecision == "known_project" || !recommendationMissingAllowed(result.MissingContextCodes)) {
			d.reason = "invalid_response"
			return
		}
		d.outcome.Status = "abstained"
		d.outcome.SubjectDecision = result.SubjectDecision
		d.missingContextCodes = append([]string(nil), result.MissingContextCodes...)
		return
	}
	if result.Status != "decided" || result.PrimaryTag == "" {
		if result.Status != "unavailable" {
			d.reason = "invalid_response"
			d.outcome.Status = "invalid"
		}
		return
	}
	allowed := map[string]bool{}
	for _, entry := range catalog.Entries {
		if entry.Namespace == "projects" && contextcatalog.Eligible(entry) {
			allowed[entry.Tag] = true
		}
	}
	if !allowed[result.PrimaryTag] || len(result.RelatedTags) > 32 {
		d.reason = "invalid_response"
		d.outcome.Status = "invalid"
		return
	}
	seen := map[string]bool{}
	for _, tag := range result.RelatedTags {
		if !allowed[tag] || seen[tag] {
			d.reason = "invalid_response"
			d.outcome.Status = "invalid"
			return
		}
		seen[tag] = true
	}
	inferred := append(append([]string{}, d.before.Tags...), result.PrimaryTag)
	inferred, primary := normalizeFactTags(inferred, result.PrimaryTag)
	if len(inferred) > maxTags {
		d.reason = "invalid_response"
		d.outcome.Status = "invalid"
		return
	}
	d.after = aiGrouping{inferred, primary}
	d.recommendedRelatedTags = append([]string(nil), result.RelatedTags...)
	filtered := d.recommendedRelatedTags[:0]
	for _, tag := range d.recommendedRelatedTags {
		if tag != result.PrimaryTag {
			filtered = append(filtered, tag)
		}
	}
	d.recommendedRelatedTags = filtered
	d.outcome.Status = "decided"
	d.outcome.SubjectDecision = "known_project"
}

func newAIRef() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}
func (d *aiWriteDecision) prepare(pointID string) bool {
	if d == nil || d.outcome.Status != "decided" {
		return false
	}
	_, hash, ok := d.runtime.catalog()
	if !ok || hash != d.outcome.CatalogHash || d.runtime.ctx.Err() != nil {
		d.outcome.Status = "catalog_changed"
		d.after = d.before
		return false
	}
	ref, err := newAIRef()
	if err != nil {
		d.outcome.Status = "unavailable"
		d.after = d.before
		return false
	}
	p := d.runtime.cfg.Profiles[d.runtime.cfg.Write.Profile]
	d.journal = &aiWriteJournal{SchemaVersion: 2, OperationRef: ref, PointID: pointID, CatalogHash: d.outcome.CatalogHash, Profile: d.runtime.cfg.Write.Profile, Model: p.Model, RubricVersion: writeRubricVersion(d.subject), Mode: d.outcome.Mode, Status: "prepared", CreatedAt: nowISO(), Before: d.before, After: d.after, RecommendedRelatedTags: append([]string(nil), d.recommendedRelatedTags...), Retention: "manual"}
	d.journal.SubjectDecision = d.outcome.SubjectDecision
	if !d.saveJournal() {
		d.journal = nil
		d.after = d.before
		d.outcome.Status = "unavailable"
		return false
	}
	d.outcome.OperationRef = ref
	return true
}
func (d *aiWriteDecision) saveJournal() bool {
	if d.journal == nil {
		return false
	}
	raw, err := json.Marshal(d.journal)
	return err == nil && aipolicy.AtomicWrite(filepath.Join(d.runtime.cfg.StateDir, "write-audits"), d.journal.OperationRef+".json", raw) == nil
}
func (d *aiWriteDecision) complete() {
	if d == nil || d.journal == nil {
		return
	}
	d.outcome.Applied = true
	d.journal.Status = "stored"
	d.saveJournal()
}
func (d *aiWriteDecision) shadow(pointID, fact, namespace string, origin *FactOrigin, payload map[string]interface{}) {
	if d == nil || d.runtime.cfg.Write.Mode != "shadow" || d.outcome.Status != "shadow" {
		return
	}
	ref, err := newAIRef()
	fingerprint, fingerprintErr := recommendationFingerprint(payload)
	if err != nil || fingerprintErr != nil {
		d.outcome.Status = "unavailable"
		return
	}
	_, hash, _ := contextcatalog.LoadActive(d.runtime.cfg.CatalogDir)
	p := d.runtime.cfg.Profiles[d.runtime.cfg.Write.Profile]
	d.journal = &aiWriteJournal{SchemaVersion: 3, OperationRef: ref, PointID: pointID,
		Owner: d.runtime.owner, InputFingerprint: fingerprint, RuntimeInstance: d.runtime.instanceID,
		PendingUntil: time.Now().UTC().Add(time.Duration(aiShadowQueueSize+1)*max(d.runtime.cfg.Write.ShadowTimeout(), aiOperationTimeout) + time.Second).Format(time.RFC3339Nano),
		CatalogHash:  hash, Profile: d.runtime.cfg.Write.Profile, Model: p.Model,
		Mode: "shadow", Status: "pending", CreatedAt: nowISO(), Before: d.before, After: d.before,
		Retention: "manual", RubricVersion: writeRubricVersion(d.subject)}
	if !d.saveJournal() {
		d.journal = nil
		d.outcome.Status = "unavailable"
		return
	}
	d.outcome.OperationRef = ref
	// The bounded queue captures only immutable request values, never the caller
	// context. Its lifetime and cancellation belong to the server.
	job := *d
	journal := *d.journal
	job.journal = &journal
	job.outcome = &AIOutcome{Mode: "shadow", Status: "unavailable"}
	if origin != nil {
		copy := *origin
		origin = &copy
	}
	job.reason = ""
	queued := d.runtime.enqueueShadow(func(ctx context.Context) {
		started := time.Now()
		job.classify(ctx, fact, namespace, origin)
		job.durationMS = time.Since(started).Milliseconds()
		job.recordShadow()
	}, d.runtime.cfg.Write.ShadowTimeout(), func() { job.reason = "canceled"; job.recordShadow() })
	if !queued {
		job.reason = "queue_full"
		if d.runtime.ctx.Err() != nil {
			job.reason = "canceled"
		}
		job.recordShadow()
	}
}

func (d *aiWriteDecision) recordShadow() {
	if d.outcome.Status != "decided" && d.outcome.Status != "abstained" {
		d.outcome.Status = "failed"
		if d.reason == "" {
			d.reason = "provider_unavailable"
		}
	}
	if d.journal == nil {
		return
	}
	d.journal.Status = d.outcome.Status
	d.journal.After = d.after
	d.journal.RecommendedRelatedTags = append([]string(nil), d.recommendedRelatedTags...)
	d.journal.MissingContextCodes = append([]string(nil), d.missingContextCodes...)
	d.journal.SubjectDecision = d.outcome.SubjectDecision
	d.journal.ReasonCode = d.reason
	d.journal.DurationMS = d.durationMS
	if !d.saveJournal() {
		slog.Warn("optional AI shadow journal unavailable")
	}
}

func writeRubricVersion(subject *FactSubject) string {
	if subject != nil && (subject.Scope != "" || subject.Context != "") {
		return aijudgment.ClassifySubjectRubricVersion
	}
	return aijudgment.ClassifyRubricVersion
}

// guardAIWrite pins both policy identity and catalog publication through Upsert.
// The provider call happens before this interval and never holds these locks.
func (s *Server) guardAIWrite(ctx context.Context, d *aiWriteDecision) (func(), bool) {
	s.aiMu.RLock()
	if s.ai != d.runtime || d.runtime.ctx.Err() != nil {
		s.aiMu.RUnlock()
		return nil, false
	}
	release, err := contextcatalog.GuardGeneration(ctx, d.runtime.cfg.CatalogDir, d.outcome.CatalogHash)
	if err != nil {
		s.aiMu.RUnlock()
		return nil, false
	}
	return func() { release(); s.aiMu.RUnlock() }, true
}
