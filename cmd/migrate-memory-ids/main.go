package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Dzarlax-AI/personal-memory/internal/memorymigration"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func main() {
	defaultURL := os.Getenv("QDRANT_URL")
	if defaultURL == "" {
		defaultURL = "http://memory-qdrant:6333"
	}
	qdrantURL := flag.String("qdrant-url", defaultURL, "Qdrant base URL")
	collection := flag.String("collection", "memory", "memory collection name")
	apply := flag.Bool("apply", false, "apply the migration (default is dry run)")
	writesStopped := flag.Bool("confirm-writes-stopped", false, "confirm that all memory writers are stopped for apply mode")
	flag.Parse()
	if err := validateApplyPreconditions(*apply, *writesStopped); err != nil {
		slog.Error("memory ID migration refused", "error", err)
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	report, err := memorymigration.Run(ctx, qdrant.NewClient(*qdrantURL, *collection), *apply)
	if err != nil {
		slog.Error("memory ID migration failed", "error", err)
		os.Exit(1)
	}
	mode := "dry-run"
	if *apply {
		mode = "apply"
	}
	fmt.Printf("mode=%s scanned=%d planned=%d migrated=%d already_current=%d collisions=%d invalid=%d\n",
		mode, report.Scanned, report.Planned, report.Migrated, report.AlreadyCurrent, report.Collisions, report.Invalid)
	if report.Collisions > 0 || report.Invalid > 0 {
		os.Exit(2)
	}
}

func validateApplyPreconditions(apply, writesStopped bool) error {
	if apply && !writesStopped {
		return fmt.Errorf("-apply requires -confirm-writes-stopped; stop every memory writer before migration and keep them stopped until it finishes")
	}
	return nil
}
