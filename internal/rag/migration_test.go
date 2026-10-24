package rag

import (
	"context"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"testing"
)

func TestMigrationSelectsLatestSealedAndRejectsUnverifiedFiles(t *testing.T) {
	path := "/synthetic/a.md"
	old := generationPoints(t, path, "old", []string{"old"}, true)
	newer := generationPoints(t, path, "new", []string{"new"}, true)
	setSealTime(newer, "2026-10-18T00:00:00Z")
	pending := generationPoints(t, path, "pending", []string{"partial", "absent"}, false)[:1]
	source := controlledGenerationScroller{generations: map[string][]qdrant.ScrollPoint{path + "\x00old": old, path + "\x00new": newer, path + "\x00pending": pending}}
	all := append(append(append([]qdrant.ScrollPoint{}, old...), newer...), pending...)
	selected, err := migrationChunks(context.Background(), source, all)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].ID != newer[0].ID {
		t.Fatalf("selected=%v", selected)
	}
	unverified := qdrant.ScrollPoint{ID: "unverified", Payload: map[string]any{"file_path": "/synthetic/unverified.md", "text": "legacy"}}
	all = append(all, unverified)
	if _, err := migrationChunks(context.Background(), source, all); err == nil {
		t.Fatal("silently dropped unverified document")
	}
}
