package rag

import (
	"context"
	"errors"
	"sort"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

// MigrationChunks selects the newest complete sealed generation for every file.
// Legacy or pending-only files fail closed: rebuild those documents from their
// source directory with the new indexer rather than legitimizing partial data.
// The caller retains the complete source fingerprint for freshness checking.
func MigrationChunks(ctx context.Context, source *qdrant.Client, all []qdrant.ScrollPoint) ([]qdrant.ScrollPoint, error) {
	return migrationChunks(ctx, source, all)
}

func migrationChunks(ctx context.Context, source generationScroller, all []qdrant.ScrollPoint) ([]qdrant.ScrollPoint, error) {
	files := map[string]bool{}
	for _, p := range all {
		path, ok := p.Payload["file_path"].(string)
		if !ok || path == "" {
			return nil, errors.New("migration chunk has no file identity")
		}
		files[path] = true
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	original := map[string]qdrant.ScrollPoint{}
	for _, point := range all {
		original[point.ID] = point
	}
	var selected []qdrant.ScrollPoint
	for _, path := range paths {
		discovery, err := discoverSealedGenerations(ctx, source, path)
		if err != nil {
			return nil, errors.New("document generation validation failed")
		}
		if len(discovery.generations) == 0 {
			return nil, errors.New("document has no complete sealed generation; rebuild from source before migration")
		}
		var best string
		for generation, v := range discovery.generations {
			previous := discovery.generations[best]
			if best == "" || v.sealedAt.After(previous.sealedAt) || (v.sealedAt.Equal(previous.sealedAt) && generation > best) {
				best = generation
			}
		}
		for _, point := range discovery.generations[best].points {
			raw, ok := original[point.ID]
			if !ok {
				return nil, errors.New("document generation changed during migration scan")
			}
			selected = append(selected, raw)
		}
	}
	return selected, nil
}
