package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Dzarlax-AI/personal-memory/internal/lifecyclemigration"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

type cliOptions struct {
	apply          bool
	writesStopped  bool
	manifestPath   string
	rollbackPath   string
	qdrantURL      string
	collectionName string
}

func main() {
	defaultURL := os.Getenv("QDRANT_URL")
	if defaultURL == "" {
		defaultURL = "http://memory-qdrant:6333"
	}
	var options cliOptions
	flag.StringVar(&options.qdrantURL, "qdrant-url", defaultURL, "Qdrant base URL")
	flag.StringVar(&options.collectionName, "collection", "memory", "memory collection name")
	flag.BoolVar(&options.apply, "apply", false, "apply the migration (default is dry run)")
	flag.BoolVar(&options.writesStopped, "confirm-writes-stopped", false, "confirm that all memory writers are stopped for apply or rollback")
	flag.StringVar(&options.manifestPath, "rollback-manifest", "", "exclusive path for the immutable apply rollback manifest")
	flag.StringVar(&options.rollbackPath, "rollback", "", "rollback from an existing migration manifest")
	flag.Parse()
	if err := validateCLIOptions(options); err != nil {
		slog.Error("lifecycle migration refused", "error", err)
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	report, err := lifecyclemigration.Run(ctx, qdrant.NewClient(options.qdrantURL, options.collectionName), lifecyclemigration.Options{
		Collection:    options.collectionName,
		Apply:         options.apply,
		WritesStopped: options.writesStopped,
		ManifestPath:  options.manifestPath,
		RollbackPath:  options.rollbackPath,
	})
	fmt.Printf(
		"mode=%s scanned=%d planned=%d applied=%d already_applied=%d rolled_back=%d conflicts=%d invalid=%d point_ids=%s\n",
		report.Mode,
		report.Scanned,
		report.Planned,
		report.Applied,
		report.AlreadyApplied,
		report.RolledBack,
		report.Conflicts,
		report.Invalid,
		strings.Join(report.PointIDs, ","),
	)
	if err != nil {
		slog.Error("lifecycle migration failed", "error", err)
		os.Exit(1)
	}
	if report.Invalid > 0 {
		os.Exit(2)
	}
}

func validateCLIOptions(options cliOptions) error {
	if strings.TrimSpace(options.qdrantURL) == "" {
		return fmt.Errorf("qdrant URL is required")
	}
	if strings.TrimSpace(options.collectionName) == "" {
		return fmt.Errorf("collection is required")
	}
	if options.rollbackPath != "" {
		if options.apply || options.manifestPath != "" {
			return fmt.Errorf("-rollback cannot be combined with -apply or -rollback-manifest")
		}
		if !options.writesStopped {
			return fmt.Errorf("-rollback requires -confirm-writes-stopped")
		}
		return nil
	}
	if options.apply {
		if !options.writesStopped {
			return fmt.Errorf("-apply requires -confirm-writes-stopped")
		}
		if strings.TrimSpace(options.manifestPath) == "" {
			return fmt.Errorf("-apply requires -rollback-manifest")
		}
		return nil
	}
	if options.manifestPath != "" || options.writesStopped {
		return fmt.Errorf("dry run does not accept -rollback-manifest or -confirm-writes-stopped")
	}
	return nil
}
