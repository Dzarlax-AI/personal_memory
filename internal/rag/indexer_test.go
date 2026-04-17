package rag

import (
	"regexp"
	"strings"
	"testing"
)

// Qdrant accepts a point id only as an unsigned integer or a UUID
// in the canonical 8-4-4-4-12 hex format. A different layout (e.g. 8-4-4-4-20)
// silently fails every upsert with a 400.
var uuidV5Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestChunkPointID_MatchesQdrantUUIDFormat(t *testing.T) {
	id := chunkPointID("/root/documents/personal/notes/architecture.md", 3)
	if !uuidV5Pattern.MatchString(id) {
		t.Errorf("chunkPointID returned %q, which does not match UUID 8-4-4-4-12 (Qdrant rejects this)", id)
	}
}

func TestFolderPointID_MatchesQdrantUUIDFormat(t *testing.T) {
	id := folderPointID("/root/documents/personal/notes")
	if !uuidV5Pattern.MatchString(id) {
		t.Errorf("folderPointID returned %q, which does not match UUID 8-4-4-4-12 (Qdrant rejects this)", id)
	}
}

func TestChunkPointID_DeterministicPerFilePath(t *testing.T) {
	a := chunkPointID("/a/b/c.md", 0)
	b := chunkPointID("/a/b/c.md", 0)
	if a != b {
		t.Errorf("chunkPointID must be deterministic; got %q then %q", a, b)
	}
	if chunkPointID("/a/b/c.md", 0) == chunkPointID("/a/b/c.md", 1) {
		t.Error("different chunk indices should produce different ids")
	}
}

func TestStaleCleanupDecision_WalkErrorsAlwaysSkip(t *testing.T) {
	skip, reason := staleCleanupDecision(100, 100, true)
	if !skip {
		t.Fatal("must skip cleanup when walk had errors")
	}
	if !strings.Contains(reason, "walk") {
		t.Errorf("reason should mention walk, got %q", reason)
	}
}

func TestStaleCleanupDecision_SuspiciouslyLowWalk(t *testing.T) {
	// Qdrant has 100 files, walk only saw 5. Cleanup would delete 95 — abort.
	skip, reason := staleCleanupDecision(5, 100, false)
	if !skip {
		t.Fatal("must skip cleanup when walked count is suspiciously low")
	}
	if !strings.Contains(reason, "suspicious") && !strings.Contains(reason, "low") {
		t.Errorf("reason should flag the low count, got %q", reason)
	}
}

func TestStaleCleanupDecision_HealthyWalk(t *testing.T) {
	// 90 walked, 100 known — plausible: 10 files genuinely deleted.
	skip, _ := staleCleanupDecision(90, 100, false)
	if skip {
		t.Error("must not skip cleanup on a healthy walk")
	}
}

func TestStaleCleanupDecision_FreshIndex(t *testing.T) {
	// First-ever run: nothing in Qdrant, lots of files on disk.
	skip, _ := staleCleanupDecision(50, 0, false)
	if skip {
		t.Error("must not skip cleanup on a fresh (empty) index")
	}
}

func TestStaleCleanupDecision_EmptyDiskWithFullIndex(t *testing.T) {
	// No files walked but 100 in Qdrant — catastrophic if we didn't guard.
	skip, _ := staleCleanupDecision(0, 100, false)
	if !skip {
		t.Error("must skip cleanup when disk is empty but Qdrant is full")
	}
}

func TestStaleCleanupDecision_ExactThreshold(t *testing.T) {
	// walkedFiles*2 < knownFiles:
	// - 50*2 == 100, not less → do NOT skip (half is the boundary).
	// - 49*2 == 98 < 100 → skip.
	if skip, _ := staleCleanupDecision(50, 100, false); skip {
		t.Error("should not skip at exactly half")
	}
	if skip, _ := staleCleanupDecision(49, 100, false); !skip {
		t.Error("should skip just below half")
	}
}
