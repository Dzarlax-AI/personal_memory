package memory

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

const lifecycleCacheScope = "lifecycle-current-v1"

func currentLifecycleFilters(base map[string]interface{}) map[string]interface{} {
	lifecycleShould := []map[string]interface{}{
		{"key": "lifecycle_state", "match": map[string]interface{}{"value": string(lifecycle.Current)}},
		{"is_empty": map[string]interface{}{"key": "lifecycle_state"}},
	}
	if _, hasShould := base["should"]; hasShould {
		return map[string]interface{}{"must": []interface{}{
			base,
			map[string]interface{}{"should": lifecycleShould},
		}}
	}
	filters := make(map[string]interface{}, len(base)+1)
	for key, value := range base {
		filters[key] = value
	}
	filters["should"] = lifecycleShould
	return filters
}

func lifecycleCandidateLimit(limit int) int {
	const minimumHeadroom = 20
	candidateLimit := limit * 4
	if candidateLimit < minimumHeadroom {
		candidateLimit = minimumHeadroom
	}
	if candidateLimit > maxSearchLimit {
		candidateLimit = maxSearchLimit
	}
	return candidateLimit
}

func lifecycleView(pointID string, payload map[string]interface{}) lifecycle.View {
	view, _ := lifecycle.Parse(payload, pointID)
	return view
}

func addLifecycleMetadata(target map[string]interface{}, view lifecycle.View) {
	target["lifecycle_state"] = string(view.State)
	target["lifecycle_legacy"] = view.Legacy
	target["canonical"] = view.Canonical
	target["provenance"] = view.Provenance
	target["verified_at"] = view.VerifiedAt
	target["supersedes"] = view.Supersedes
	target["superseded_by"] = view.SupersededBy
	target["lifecycle_valid"] = view.Valid
	if !view.Valid {
		target["lifecycle_error"] = view.InvalidReason
	}
	target["_lifecycle_summary"] = formatLifecycleView(view)
}

func formatLifecycleView(view lifecycle.View) string {
	parts := []string{"state:" + string(view.State)}
	if view.Legacy {
		parts = append(parts, "legacy")
	}
	if view.Canonical {
		parts = append(parts, "canonical")
	}
	if !view.Valid {
		parts = append(parts, "invalid:"+view.InvalidReason)
	}
	if view.Provenance != nil {
		parts = append(parts, "source:"+view.Provenance.Source)
		if view.Provenance.Reference != "" {
			parts = append(parts, "reference:"+view.Provenance.Reference)
		}
	}
	if view.VerifiedAt != "" {
		parts = append(parts, "verified:"+view.VerifiedAt)
	}
	if len(view.Supersedes) > 0 {
		parts = append(parts, "supersedes:"+strings.Join(view.Supersedes, ","))
	}
	if len(view.SupersededBy) > 0 {
		parts = append(parts, "superseded-by:"+strings.Join(view.SupersededBy, ","))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func lifecycleSummaryFromHit(hit map[string]interface{}) string {
	if summary, ok := hit["_lifecycle_summary"].(string); ok && summary != "" {
		return summary
	}
	return ""
}

func currentSearchPoints(points []qdrant.Point) []qdrant.Point {
	byID := make(map[string]qdrant.Point, len(points))
	candidates := make([]lifecycle.Candidate, 0, len(points))
	for _, point := range points {
		view := lifecycleView(point.ID, point.Payload)
		if !lifecycle.IsCurrentTruth(view, isExpired(point.Payload)) {
			continue
		}
		byID[point.ID] = point
		candidates = append(candidates, lifecycle.Candidate{PointID: point.ID, Score: point.Score, View: view})
	}
	lifecycle.SortCandidates(candidates)
	result := make([]qdrant.Point, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, byID[candidate.PointID])
	}
	return result
}

func relatedSearchPoints(points []qdrant.Point) []qdrant.Point {
	byID := make(map[string]qdrant.Point, len(points))
	candidates := make([]lifecycle.Candidate, 0, len(points))
	for _, point := range points {
		view := lifecycleView(point.ID, point.Payload)
		byID[point.ID] = point
		candidates = append(candidates, lifecycle.Candidate{PointID: point.ID, Score: point.Score, View: view})
	}
	lifecycle.SortCandidates(candidates)
	result := make([]qdrant.Point, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, byID[candidate.PointID])
	}
	return result
}

func sortLifecycleCounts(counts map[lifecycle.State]int) []string {
	states := []lifecycle.State{lifecycle.Current, lifecycle.Historical, lifecycle.Superseded, lifecycle.Disputed}
	lines := make([]string, 0, len(states))
	for _, state := range states {
		lines = append(lines, fmt.Sprintf("  %s: %d", state, counts[state]))
	}
	return lines
}

func lifecycleCounts(points []qdrant.ScrollPoint) (map[lifecycle.State]int, int, int) {
	counts := make(map[lifecycle.State]int, 4)
	legacy := 0
	invalid := 0
	for _, point := range points {
		view := lifecycleView(point.ID, point.Payload)
		if !view.Valid {
			invalid++
			continue
		}
		counts[view.State]++
		if view.Legacy {
			legacy++
		}
	}
	return counts, legacy, invalid
}

func sortOperationalPoints(points []qdrant.ScrollPoint) {
	sort.SliceStable(points, func(i, j int) bool {
		left := lifecycleView(points[i].ID, points[i].Payload)
		right := lifecycleView(points[j].ID, points[j].Payload)
		if left.Canonical != right.Canonical {
			return left.Canonical
		}
		ri, _ := points[i].Payload["recall_count"].(float64)
		rj, _ := points[j].Payload["recall_count"].(float64)
		if ri != rj {
			return ri > rj
		}
		return points[i].ID < points[j].ID
	})
}
