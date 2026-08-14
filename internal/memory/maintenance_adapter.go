package memory

import (
	"github.com/Dzarlax-AI/personal-memory/internal/memory/maintenance"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func activeMemoryFilters(base map[string]interface{}) map[string]interface{} {
	return maintenance.ActiveFilter(base)
}

func activeMemoryPayload(payload map[string]interface{}) bool {
	return maintenance.IsActive(payload)
}

func activeSearchPoints(points []qdrant.Point) []qdrant.Point {
	result := make([]qdrant.Point, 0, len(points))
	for _, point := range points {
		if activeMemoryPayload(point.Payload) {
			result = append(result, point)
		}
	}
	return result
}

func activeScrollPoints(points []qdrant.ScrollPoint) []qdrant.ScrollPoint {
	result := make([]qdrant.ScrollPoint, 0, len(points))
	for _, point := range points {
		if activeMemoryPayload(point.Payload) {
			result = append(result, point)
		}
	}
	return result
}
