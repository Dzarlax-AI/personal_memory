package memorymigration

import (
	"context"
	"fmt"
	"reflect"

	"github.com/Dzarlax-AI/personal-memory/internal/memory"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

type Store interface {
	ScrollAll(ctx context.Context, filters map[string]interface{}, withVector bool) ([]qdrant.ScrollPoint, error)
	Get(ctx context.Context, id string) (qdrant.Point, bool, error)
	Upsert(ctx context.Context, point qdrant.Point) error
	Delete(ctx context.Context, ids []string) error
}

type Report struct {
	Scanned        int
	AlreadyCurrent int
	Planned        int
	Migrated       int
	Collisions     int
	Invalid        int
}

// Run migrates legacy memory point IDs to namespace-aware IDs. It is dry-run
// by default through the apply argument. Each point is written at its new ID
// before the old ID is deleted, so an interrupted run is safe to resume.
func Run(ctx context.Context, store Store, apply bool) (Report, error) {
	var report Report
	points, err := store.ScrollAll(ctx, nil, true)
	if err != nil {
		return report, fmt.Errorf("scan memory points: %w", err)
	}

	type candidate struct {
		point     qdrant.ScrollPoint
		namespace string
		targetID  string
	}
	type candidateGroup struct {
		targetID   string
		candidates []candidate
	}
	groups := make([]candidateGroup, 0, len(points))
	groupIndexes := make(map[string]int, len(points))
	byID := make(map[string]qdrant.ScrollPoint, len(points))
	for _, point := range points {
		byID[point.ID] = point
	}
	for _, point := range points {
		report.Scanned++
		text, ok := point.Payload["text"].(string)
		if !ok || text == "" || len(point.Vector) == 0 {
			report.Invalid++
			continue
		}
		namespace := memory.NormalizeNamespace(payloadString(point.Payload["namespace"]))
		targetID := memory.PointID(namespace, text)
		if point.ID == targetID {
			report.AlreadyCurrent++
			continue
		}
		entry := candidate{point: point, namespace: namespace, targetID: targetID}
		groupIndex, found := groupIndexes[targetID]
		if !found {
			groupIndex = len(groups)
			groupIndexes[targetID] = groupIndex
			groups = append(groups, candidateGroup{targetID: targetID})
		}
		groups[groupIndex].candidates = append(groups[groupIndex].candidates, entry)
	}

	// Preflight every source that resolves to the same target before mutating
	// anything. If a conflicting point is itself a migration candidate, keep its
	// whole group in place as well so scan order cannot decide which data wins.
	blockedSources := make(map[string]struct{})
	for _, group := range groups {
		canonical := group.candidates[0]
		collision := false
		for _, candidate := range group.candidates[1:] {
			if !equivalentPoint(candidate.point.Vector, candidate.point.Payload, canonical.point.Vector, canonical.point.Payload) {
				collision = true
				break
			}
		}
		occupied, occupiedFound := byID[group.targetID]
		if occupiedFound && !equivalentPoint(canonical.point.Vector, canonical.point.Payload, occupied.Vector, occupied.Payload) {
			collision = true
		}
		if !collision {
			continue
		}
		report.Collisions++
		for _, candidate := range group.candidates {
			blockedSources[candidate.point.ID] = struct{}{}
		}
		if occupiedFound {
			blockedSources[occupied.ID] = struct{}{}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, group := range groups {
			blocked := false
			for _, candidate := range group.candidates {
				if _, found := blockedSources[candidate.point.ID]; found {
					blocked = true
					break
				}
			}
			if !blocked {
				continue
			}
			for _, candidate := range group.candidates {
				if _, found := blockedSources[candidate.point.ID]; !found {
					blockedSources[candidate.point.ID] = struct{}{}
					changed = true
				}
			}
			if occupied, found := byID[group.targetID]; found {
				if _, alreadyBlocked := blockedSources[occupied.ID]; !alreadyBlocked {
					blockedSources[occupied.ID] = struct{}{}
					changed = true
				}
			}
		}
	}

	for _, group := range groups {
		canonical := group.candidates[0]
		if _, blocked := blockedSources[canonical.point.ID]; blocked {
			continue
		}

		target, found, err := store.Get(ctx, group.targetID)
		if err != nil {
			return report, fmt.Errorf("check target %s: %w", group.targetID, err)
		}
		if found && !equivalentPoint(canonical.point.Vector, canonical.point.Payload, target.Vector, target.Payload) {
			report.Collisions++
			continue
		}

		report.Planned += len(group.candidates)
		if !apply {
			continue
		}
		if !found {
			payload := clonePayload(canonical.point.Payload)
			payload["namespace"] = canonical.namespace
			if err := store.Upsert(ctx, qdrant.Point{ID: group.targetID, Vector: canonical.point.Vector, Payload: payload}); err != nil {
				return report, fmt.Errorf("write target %s for source %s: %w", group.targetID, canonical.point.ID, err)
			}
		}
		sourceIDs := make([]string, len(group.candidates))
		for i, candidate := range group.candidates {
			sourceIDs[i] = candidate.point.ID
		}
		if err := store.Delete(ctx, sourceIDs); err != nil {
			return report, fmt.Errorf("delete migrated sources for target %s: %w", group.targetID, err)
		}
		report.Migrated += len(group.candidates)
	}
	return report, nil
}

func equivalentPoint(leftVector []float32, leftPayload map[string]interface{}, rightVector []float32, rightPayload map[string]interface{}) bool {
	return reflect.DeepEqual(leftVector, rightVector) &&
		reflect.DeepEqual(normalizedPayload(leftPayload), normalizedPayload(rightPayload))
}

func payloadString(value interface{}) string {
	valueString, _ := value.(string)
	return valueString
}

func clonePayload(payload map[string]interface{}) map[string]interface{} {
	clone := make(map[string]interface{}, len(payload)+1)
	for key, value := range payload {
		clone[key] = value
	}
	return clone
}

func normalizedPayload(payload map[string]interface{}) map[string]interface{} {
	clone := clonePayload(payload)
	clone["namespace"] = memory.NormalizeNamespace(payloadString(payload["namespace"]))
	return clone
}
