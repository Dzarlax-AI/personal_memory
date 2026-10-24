// migrate-embeddings prepares or applies an isolated collection backfill.
// Run on the data host. No source content or paths are printed.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Dzarlax-AI/personal-memory/internal/config"
	"github.com/Dzarlax-AI/personal-memory/internal/embeddingmigration"
	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func run() error {
	profilesFile := flag.String("profiles-file", "", "target embedding profiles JSON")
	profileName := flag.String("profile", "", "target profile name")
	sourceName := flag.String("source", "", "source collection")
	targetName := flag.String("target", "", "new target collection")
	role := flag.String("role", "", "memory, chunks or folders")
	manifestFile := flag.String("manifest", "", "private manifest path (new on prepare)")
	mode := flag.String("mode", "prepare", "prepare, apply or verify")
	stopped := flag.Bool("confirm-writers-stopped", false, "all source and target writers including recall workers are stopped")
	flag.Parse()
	if flag.NArg() != 0 || *profilesFile == "" || *profileName == "" || *sourceName == "" || *targetName == "" || *manifestFile == "" {
		return fmt.Errorf("required migration argument missing")
	}
	profiles, err := config.LoadEmbeddingProfiles(*profilesFile)
	if err != nil {
		return err
	}
	backendConfig, ok := profiles.Profiles[*profileName]
	if !ok {
		return fmt.Errorf("unknown migration profile")
	}
	backend, err := embeddings.NewBackend(backendConfig)
	if err != nil {
		return err
	}
	address := os.Getenv("QDRANT_URL")
	if address == "" {
		return fmt.Errorf("QDRANT_URL is required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	job := embeddingmigration.Job{Source: qdrant.NewClient(address, *sourceName), Target: qdrant.NewClient(address, *targetName), Backend: backend, Config: backendConfig, Role: *role}
	switch *mode {
	case "prepare":
		manifest, err := job.Prepare(ctx)
		if err != nil {
			return err
		}
		if err := embeddingmigration.WriteManifest(*manifestFile, manifest); err != nil {
			return err
		}
		fmt.Println("prepared", manifest.SafeSummary())
	case "apply", "verify":
		manifest, err := embeddingmigration.ReadManifest(*manifestFile)
		if err != nil {
			return err
		}
		if *mode == "apply" {
			err = job.Apply(ctx, manifest, *stopped)
		} else {
			err = job.Verify(ctx, manifest)
		}
		if err != nil {
			return err
		}
		fmt.Println(*mode, "confirmed", manifest.SafeSummary())
	default:
		return fmt.Errorf("unsupported migration mode")
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		// Underlying storage errors can contain private payloads. Keep diagnostics on
		// the data host; command output carries a finite outcome only.
		fmt.Fprintln(os.Stderr, "embedding_migration_failed: inspect configuration, manifest freshness, ownership and stopped writers on the data host")
		os.Exit(1)
	}
}
