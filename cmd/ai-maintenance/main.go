package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/Dzarlax-AI/personal-memory/internal/aimaintenance"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/factgrouping"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ai-maintenance status|history --config FILE | apply|rollback --manifest FILE --journal FILE --qdrant-url URL --confirm-server-stopped")
		os.Exit(1)
	}
	if os.Args[1] == "status" {
		if err := runStatus(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if os.Args[1] == "history" {
		if err := runHistory(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	fs := flag.NewFlagSet("ai-maintenance", flag.ExitOnError)
	manifest := fs.String("manifest", "", "private approved manifest")
	journal := fs.String("journal", "", "private outcome journal")
	url := fs.String("qdrant-url", "", "explicit Qdrant endpoint")
	stopped := fs.Bool("confirm-server-stopped", false, "all writers stopped")
	fs.Parse(os.Args[2:])
	if (os.Args[1] != "apply" && os.Args[1] != "rollback") || *url == "" {
		fmt.Fprintln(os.Stderr, "invalid grouping command")
		os.Exit(1)
	}
	m, err := factgrouping.ReadManifest(*manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	j, err := factgrouping.Run(ctx, qdrant.NewClient(*url, "memory"), m, *journal, *stopped, os.Args[1] == "rollback")
	json.NewEncoder(os.Stdout).Encode(j)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runStatus(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ai-maintenance status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "optional AI configuration file")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("invalid status arguments")
	}
	if *configPath == "" || fs.NArg() != 0 {
		return fmt.Errorf("status requires --config FILE")
	}
	cfg, err := aipolicy.Load(*configPath)
	if err != nil {
		return fmt.Errorf("cannot load optional AI configuration")
	}
	status, err := aimaintenance.ReadCatalogStatus(cfg)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(status); err != nil {
		return fmt.Errorf("cannot write maintenance status")
	}
	return nil
}

func runHistory(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ai-maintenance history", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "optional AI configuration file")
	limit := fs.Int("limit", 20, "maximum records")
	runID := fs.String("run-id", "", "exact run identifier")
	cursor := fs.String("cursor", "", "continue before this run identifier")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("invalid history arguments")
	}
	if *configPath == "" || fs.NArg() != 0 {
		return fmt.Errorf("history requires --config FILE")
	}
	cfg, err := aipolicy.Load(*configPath)
	if err != nil {
		return fmt.Errorf("cannot load optional AI configuration")
	}
	if *limit < 1 || *limit > 100 {
		return fmt.Errorf("history limit must be between 1 and 100")
	}
	if *runID != "" && *cursor != "" {
		return fmt.Errorf("history accepts either --run-id or --cursor")
	}
	page, err := aimaintenance.ReadRunHistory(cfg, *limit, *cursor, *runID)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(page); err != nil {
		return fmt.Errorf("cannot write maintenance history")
	}
	return nil
}
