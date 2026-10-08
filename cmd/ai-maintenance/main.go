package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Dzarlax-AI/personal-memory/internal/factgrouping"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ai-maintenance apply|rollback --manifest FILE --journal FILE --qdrant-url URL --confirm-server-stopped")
		os.Exit(1)
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
