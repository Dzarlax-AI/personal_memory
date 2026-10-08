package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	subject       *FactSubject
	runtime       *aiRuntime
	outcome       *AIOutcome
	before, after aiGrouping
	journal       *aiWriteJournal
}
type aiWriteJournal struct {
	SubjectDecision string     `json:"subject_decision,omitempty"`
	SchemaVersion   int        `json:"schema_version"`
	OperationRef    string     `json:"operation_ref"`
	PointID         string     `json:"point_id"`
	CatalogHash     string     `json:"catalog_hash,omitempty"`
	Profile         string     `json:"profile"`
	Model           string     `json:"model"`
	RubricVersion   string     `json:"rubric_version"`
	Mode            string     `json:"mode"`
	Status          string     `json:"status"`
	CreatedAt       string     `json:"created_at"`
	Before          aiGrouping `json:"before"`
	After           aiGrouping `json:"after"`
	Retention       string     `json:"retention"`
}

func (s *Server) aiClassify(ctx context.Context, fact, namespace string, tags []string, primary string, origin *FactOrigin, subject *FactSubject) *aiWriteDecision {
	a := s.aiState()
	if a == nil || !a.cfg.Write.Active() || namespace != "projects" || primary != "" || subjectDecision(namespace, subject) != "" {
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
	if ctx.Err() != nil || a.ctx.Err() != nil || !a.cfg.Egress.Allows(namespace, d.before.Tags) {
		return
	}
	if origin != nil && !a.cfg.Egress.Allows("projects", []string{origin.SourceProject}) {
		return
	}
	catalog, hash, ok := a.catalog()
	if !ok {
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
	if _, err := aijudgment.BuildClassifyRequest(a.cfg.Write.Provider, p.Model, in); err != nil || ctx.Err() != nil {
		return
	}
	reservation, err := a.budget.Begin(time.Now())
	if err != nil {
		return
	}
	result, usage, err := a.write.Classify(ctx, in)
	reservation.Finish(usage.InputTokens, usage.Known)
	if err != nil || ctx.Err() != nil {
		return
	}
	if result.Status == "abstained" {
		d.outcome.Status = "abstained"
		d.outcome.SubjectDecision = result.SubjectDecision
		return
	}
	if result.Status != "decided" || result.PrimaryTag == "" {
		if result.Status != "unavailable" {
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
		d.outcome.Status = "invalid"
		return
	}
	seen := map[string]bool{}
	for _, tag := range result.RelatedTags {
		if !allowed[tag] || seen[tag] {
			d.outcome.Status = "invalid"
			return
		}
		seen[tag] = true
	}
	inferred := append(append([]string{}, d.before.Tags...), result.RelatedTags...)
	inferred, primary := normalizeFactTags(inferred, result.PrimaryTag)
	if len(inferred) > maxTags {
		d.outcome.Status = "invalid"
		return
	}
	d.after = aiGrouping{inferred, primary}
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
	ref, err := newAIRef()
	if err != nil {
		d.outcome.Status = "unavailable"
		d.after = d.before
		return false
	}
	p := d.runtime.cfg.Profiles[d.runtime.cfg.Write.Profile]
	d.journal = &aiWriteJournal{SchemaVersion: 1, OperationRef: ref, PointID: pointID, CatalogHash: d.outcome.CatalogHash, Profile: d.runtime.cfg.Write.Profile, Model: p.Model, RubricVersion: aijudgment.RubricVersion, Mode: d.outcome.Mode, Status: "prepared", CreatedAt: nowISO(), Before: d.before, After: d.after, Retention: "manual"}
	if d.subject != nil {
		d.journal.RubricVersion = "optional-ai-subject-v2"
	}
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
func (d *aiWriteDecision) shadow(pointID, fact, namespace string, origin *FactOrigin) {
	if d == nil || d.runtime.cfg.Write.Mode != "shadow" {
		return
	}
	// The bounded queue captures only immutable request values, never the caller
	// context. Its lifetime and cancellation belong to the server.
	job := *d
	job.outcome = &AIOutcome{Mode: "shadow", Status: "unavailable"}
	if origin != nil {
		copy := *origin
		origin = &copy
	}
	d.runtime.enqueue(func(ctx context.Context) {
		job.classify(ctx, fact, namespace, origin)
		if job.outcome.Status != "decided" && job.outcome.Status != "abstained" {
			return
		}
		ref, err := newAIRef()
		if err != nil {
			return
		}
		p := job.runtime.cfg.Profiles[job.runtime.cfg.Write.Profile]
		job.journal = &aiWriteJournal{SchemaVersion: 1, OperationRef: ref, PointID: pointID, CatalogHash: job.outcome.CatalogHash, Profile: job.runtime.cfg.Write.Profile, Model: p.Model, Mode: "shadow", Status: job.outcome.Status, CreatedAt: nowISO(), Before: job.before, After: job.after, Retention: "manual"}
		job.journal.RubricVersion = aijudgment.RubricVersion
		if job.subject != nil {
			job.journal.RubricVersion = "optional-ai-subject-v2"
		}
		job.journal.SubjectDecision = job.outcome.SubjectDecision
		job.saveJournal()
	})
}
