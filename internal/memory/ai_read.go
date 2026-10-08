package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func aiAuthority(view lifecycle.View) (string, int) {
	switch view.State {
	case lifecycle.Current:
		if view.Canonical {
			return "canonical_current", 0
		}
		return "other_current", 1
	case lifecycle.Disputed:
		return "disputed", 2
	case lifecycle.Historical:
		return "historical", 3
	case lifecycle.Superseded:
		return "superseded", 4
	default:
		return "invalid", 5
	}
}

// Semantic dependencies include all payload metadata except best-effort recall
// counters. Neither ranking nor a concurrent recall owns those counters.
func aiFingerprint(payload map[string]interface{}) string {
	copied := make(map[string]interface{}, len(payload))
	for k, v := range payload {
		if k != "recall_count" && k != "last_recalled_at" {
			copied[k] = v
		}
	}
	raw, err := json.Marshal(copied)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (a *aiRuntime) rank(ctx context.Context, query string, options LifecycleRecallOptions, candidates []lifecycleRecallCandidate) ([]lifecycleRecallCandidate, *AIOutcome, bool) {
	outcome := &AIOutcome{Mode: a.cfg.Read.Mode, Status: "unavailable"}
	if ctx.Err() != nil || a.ctx.Err() != nil || len(candidates) == 0 || len(candidates) > 20 {
		return candidates, outcome, false
	}
	catalog, hash, ok := a.catalog()
	if !ok {
		return candidates, outcome, false
	}
	outcome.CatalogHash = hash
	if options.ProjectContext != nil && (options.CatalogHash != hash || !a.cfg.Egress.Allows(options.ProjectContext.Namespace, []string{options.ProjectContext.Tag})) {
		return candidates, outcome, false
	}
	in := aijudgment.RankInput{ProjectContext: options.ProjectContext, Query: query, LifecycleMode: string(options.normalizedMode()), Catalog: &catalog}
	for i, c := range candidates {
		ns := stringFromPayload(c.point.Payload["namespace"])
		tags := relatedCandidateTags(c.point.Payload["tags"])
		if !a.cfg.Egress.Allows(ns, tags) || math.IsNaN(c.point.Score) || math.IsInf(c.point.Score, 0) || aiFingerprint(c.point.Payload) == "" {
			return candidates, outcome, false
		}
		bucket, _ := aiAuthority(c.view)
		in.Candidates = append(in.Candidates, aijudgment.Candidate{Alias: fmt.Sprintf("c%d", i+1), Text: stringFromPayload(c.point.Payload["text"]), Namespace: ns, Tags: tags, AuthorityBucket: bucket})
	}
	p := a.cfg.Profiles[a.cfg.Read.Profile]
	if _, err := aijudgment.BuildRankRequest(a.cfg.Read.Provider, p.Model, in); err != nil || ctx.Err() != nil {
		return candidates, outcome, false
	}
	reservation, err := a.budget.Begin(time.Now())
	if err != nil {
		return candidates, outcome, false
	}
	result, usage, err := a.read.Rank(ctx, in)
	reservation.Finish(usage.InputTokens, usage.Known)
	if err != nil || ctx.Err() != nil {
		return candidates, outcome, true
	}
	if result.Status != "decided" {
		switch result.Status {
		case "abstained", "unavailable", "invalid":
			outcome.Status = result.Status
		default:
			outcome.Status = "invalid"
		}
		return candidates, outcome, true
	}
	if result.NoneRelevant {
		outcome.Status = "none_relevant"
		return candidates, outcome, true
	}
	if len(result.Scores) != len(in.Candidates) {
		outcome.Status = "invalid"
		return candidates, outcome, true
	}
	scores := make(map[string]float64, len(candidates))
	for i, c := range candidates {
		score, ok := result.Scores[in.Candidates[i].Alias]
		if !ok || math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
			outcome.Status = "invalid"
			return candidates, outcome, true
		}
		scores[c.point.ID] = score
	}
	reordered := append([]lifecycleRecallCandidate{}, candidates...)
	sort.SliceStable(reordered, func(i, j int) bool {
		_, left := aiAuthority(reordered[i].view)
		_, right := aiAuthority(reordered[j].view)
		if left != right {
			return left < right
		}
		return scores[reordered[i].point.ID] > scores[reordered[j].point.ID]
	})
	for i := range reordered {
		reordered[i].FinalRank = i + 1
		value := scores[reordered[i].point.ID]
		reordered[i].AIRelevance = &value
	}
	outcome.Status = "decided"
	outcome.Applied = true
	return reordered, outcome, true
}

func matchesAIRecallScope(payload map[string]interface{}, namespace string, tags []string) bool {
	if namespace != "" && stringFromPayload(payload["namespace"]) != namespace {
		return false
	}
	existing := relatedCandidateTags(payload["tags"])
	for _, tag := range tags {
		found := false
		for _, e := range existing {
			if e == tag {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return activeMemoryPayload(payload)
}

func (s *Server) aiRefreshCandidates(ctx context.Context, baseline []lifecycleRecallCandidate, options LifecycleRecallOptions, namespace string, tags []string) ([]lifecycleRecallCandidate, bool, error) {
	latest := make([]qdrant.Point, 0, len(baseline))
	changed := false
	originalRanks := map[string]int{}
	for _, candidate := range baseline {
		originalRanks[candidate.point.ID] = candidate.SemanticRank
		point, found, err := s.qdrant.Get(ctx, candidate.point.ID)
		if err != nil {
			return nil, true, errors.New("candidate freshness could not be verified")
		}
		if !found || point.ID != candidate.point.ID {
			changed = true
			continue
		}
		if aiFingerprint(point.Payload) != aiFingerprint(candidate.point.Payload) {
			changed = true
		}
		if !matchesAIRecallScope(point.Payload, namespace, tags) {
			changed = true
			continue
		}
		point.Score = candidate.point.Score
		latest = append(latest, point)
	}
	refreshed := presentLifecycleRecallCandidates(latest, options, time.Now())
	if len(refreshed) != len(baseline) {
		changed = true
	}
	for i := range refreshed {
		refreshed[i].SemanticRank = originalRanks[refreshed[i].point.ID]
	}
	return refreshed, changed, nil
}

func (s *Server) aiRerank(ctx context.Context, query, namespace string, tags []string, options LifecycleRecallOptions, baseline []lifecycleRecallCandidate) ([]lifecycleRecallCandidate, *AIOutcome, error) {
	a := s.aiState()
	if a == nil || a.cfg.Read.Mode != "on" {
		return baseline, nil, nil
	}
	inferenceCtx, cancel := context.WithTimeout(ctx, aiOperationTimeout)
	stop := context.AfterFunc(a.ctx, cancel)
	ranked, outcome, dispatched := a.rank(inferenceCtx, query, options, baseline)
	stop()
	cancel()
	if !dispatched {
		return baseline, outcome, nil
	}
	// Verification belongs to the safe baseline read. It uses the caller context
	// even when inference exhausted its optional one-second budget.
	refreshed, changed, err := s.aiRefreshCandidates(ctx, baseline, options, namespace, tags)
	if err != nil {
		return nil, outcome, err
	}
	// Every rank request depends on its catalog generation, including calls
	// from older clients that omit explicit project context.
	_, currentHash, catalogOK := a.catalog()
	if !catalogOK || currentHash != outcome.CatalogHash {
		outcome.Status = "catalog_changed"
		outcome.Applied = false
		return refreshed, outcome, nil
	}
	if changed {
		outcome.Status = "candidate_changed"
		outcome.Applied = false
		return refreshed, outcome, nil
	}
	// Fresh counter values are used without changing semantic ranking metadata.
	byID := map[string]qdrant.Point{}
	for _, c := range refreshed {
		byID[c.point.ID] = c.point
	}
	for i := range ranked {
		ranked[i].point = byID[ranked[i].point.ID]
	}
	return ranked, outcome, nil
}

func (s *Server) aiShadowRead(query string, options LifecycleRecallOptions, baseline []lifecycleRecallCandidate) {
	a := s.aiState()
	if a == nil || a.cfg.Read.Mode != "shadow" {
		return
	}
	candidates := append([]lifecycleRecallCandidate{}, baseline...)
	a.enqueue(func(ctx context.Context) {
		ranked, outcome, dispatched := a.rank(ctx, query, options, candidates)
		if !dispatched {
			return
		}
		ref, err := newAIRef()
		if err != nil {
			return
		}
		// The comparison record contains local IDs and hashes, never query/fact text.
		type dependency struct {
			ID          string `json:"point_id"`
			Fingerprint string `json:"fingerprint"`
		}
		record := struct {
			SchemaVersion    int          `json:"schema_version"`
			OperationRef     string       `json:"operation_ref"`
			Mode             string       `json:"mode"`
			Status           string       `json:"status"`
			CatalogHash      string       `json:"catalog_hash,omitempty"`
			ProjectContextID string       `json:"project_context_id,omitempty"`
			Profile          string       `json:"profile"`
			Model            string       `json:"model"`
			RubricVersion    string       `json:"rubric_version"`
			CreatedAt        string       `json:"created_at"`
			Dependencies     []dependency `json:"dependencies"`
			Baseline         []string     `json:"baseline_order"`
			Proposed         []string     `json:"proposed_order"`
			Retention        string       `json:"retention"`
		}{SchemaVersion: 1, OperationRef: ref, Mode: "shadow", Status: outcome.Status, CatalogHash: outcome.CatalogHash, ProjectContextID: options.ProjectContextID, Profile: a.cfg.Read.Profile, Model: a.cfg.Profiles[a.cfg.Read.Profile].Model, RubricVersion: aijudgment.RubricVersion, CreatedAt: nowISO(), Retention: "manual"}
		for _, c := range candidates {
			record.Baseline = append(record.Baseline, c.point.ID)
			record.Dependencies = append(record.Dependencies, dependency{c.point.ID, aiFingerprint(c.point.Payload)})
		}
		for _, c := range ranked {
			record.Proposed = append(record.Proposed, c.point.ID)
		}
		raw, err := json.Marshal(record)
		if err == nil {
			_ = aipolicy.AtomicWrite(filepath.Join(a.cfg.StateDir, "read-audits"), ref+".json", raw)
		}
	})
}
