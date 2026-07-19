package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

type Loop struct {
	qdrant        *qdrant.Client
	interval      time.Duration
	keepSnapshots int
}

func NewLoop(qc *qdrant.Client, interval time.Duration, keepSnapshots int) *Loop {
	return &Loop{
		qdrant:        qc,
		interval:      interval,
		keepSnapshots: keepSnapshots,
	}
}

// Run starts the backup loop. Blocks until ctx is cancelled.
func (l *Loop) Run(ctx context.Context) {
	slog.Info("backup loop started", "interval", l.interval, "keep_snapshots", l.keepSnapshots)
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("backup loop stopped")
			return
		case <-ticker.C:
			if err := l.DoBackup(ctx); err != nil {
				slog.Error("backup failed", "error", err)
			}
		}
	}
}

// DoBackup creates one snapshot and prunes snapshots beyond the retention
// limit. It returns all prune failures so callers and tests can observe them.
func (l *Loop) DoBackup(ctx context.Context) error {
	name, err := l.qdrant.CreateSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	slog.Info("backup: snapshot created", "name", name)

	// Prune old snapshots.
	snapshots, err := l.qdrant.ListSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}

	if len(snapshots) <= l.keepSnapshots {
		return nil
	}

	// Sort alphabetically (snapshot names include timestamps).
	sort.Strings(snapshots)
	toDelete := snapshots[:len(snapshots)-l.keepSnapshots]
	var pruneErrors []error
	for _, s := range toDelete {
		if err := l.qdrant.DeleteSnapshot(ctx, s); err != nil {
			slog.Error("backup: delete snapshot failed", "name", s, "error", err)
			pruneErrors = append(pruneErrors, fmt.Errorf("delete snapshot %q: %w", s, err))
		} else {
			slog.Info("backup: pruned snapshot", "name", s)
		}
	}
	return errors.Join(pruneErrors...)
}
