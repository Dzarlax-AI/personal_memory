package contextcatalog

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerationGuardCancellationWaitingForPublication(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release, err := acquireLock(filepath.Join(dir, lockName))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	unlock, err := GuardGeneration(ctx, dir, "unused")
	if unlock != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("guard ignored cancellation: %v", err)
	}
}
