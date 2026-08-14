package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/maintenance"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

const day = 24 * time.Hour
const maxDays = 36500

type analyzeOptions struct {
	qdrantURL               string
	collection              string
	namespace               string
	output                  string
	referenceTime           time.Time
	supersededRetentionDays int
	staleDays               int
	lowRecallThreshold      int
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, time.Now); err != nil {
		fmt.Fprintln(os.Stderr, "maintenance refused:", err)
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer, now func() time.Time) error {
	if len(args) == 0 || args[0] != "analyze" {
		return fmt.Errorf("usage: maintenance analyze [flags]")
	}
	options, err := parseAnalyzeOptions(args[1:], now())
	if err != nil {
		return err
	}
	manifest, err := maintenance.Analyze(ctx, qdrant.NewClient(options.qdrantURL, options.collection), maintenance.Options{
		Collection: options.collection, Namespace: options.namespace, ReferenceTime: options.referenceTime,
		SupersededRetention: time.Duration(options.supersededRetentionDays) * day,
		StaleAfter:          time.Duration(options.staleDays) * day, LowRecallThreshold: options.lowRecallThreshold,
	})
	if err != nil {
		return err
	}
	if err := maintenance.WriteManifest(options.output, manifest); err != nil {
		return err
	}
	eligible := 0
	for _, finding := range manifest.Findings {
		if finding.EligibleForQuarantine {
			eligible++
		}
	}
	_, err = fmt.Fprintf(stdout, "mode=analyze complete=true scanned=%d findings=%d eligible_for_quarantine=%d batch_id=%s\n", manifest.Scanned, len(manifest.Findings), eligible, manifest.BatchID)
	return err
}

func parseAnalyzeOptions(args []string, now time.Time) (analyzeOptions, error) {
	defaults := analyzeOptions{qdrantURL: os.Getenv("QDRANT_URL"), collection: "memory", supersededRetentionDays: 30, staleDays: 90, lowRecallThreshold: 1, referenceTime: now.UTC()}
	if defaults.qdrantURL == "" {
		defaults.qdrantURL = "http://memory-qdrant:6333"
	}
	set := flag.NewFlagSet("maintenance analyze", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var reference string
	set.StringVar(&defaults.qdrantURL, "qdrant-url", defaults.qdrantURL, "Qdrant base URL")
	set.StringVar(&defaults.collection, "collection", defaults.collection, "collection name")
	set.StringVar(&defaults.namespace, "namespace", "", "optional namespace")
	set.StringVar(&defaults.output, "output", "", "exclusive analysis manifest path")
	set.StringVar(&reference, "reference-time", "", "RFC3339 reference time")
	set.IntVar(&defaults.supersededRetentionDays, "superseded-retention-days", defaults.supersededRetentionDays, "superseded retention")
	set.IntVar(&defaults.staleDays, "stale-days", defaults.staleDays, "stale review threshold")
	set.IntVar(&defaults.lowRecallThreshold, "low-recall-threshold", defaults.lowRecallThreshold, "low recall review threshold")
	if err := set.Parse(args); err != nil {
		return analyzeOptions{}, err
	}
	if set.NArg() != 0 {
		return analyzeOptions{}, fmt.Errorf("unexpected arguments")
	}
	if strings.TrimSpace(defaults.qdrantURL) == "" || strings.TrimSpace(defaults.collection) == "" || strings.TrimSpace(defaults.output) == "" {
		return analyzeOptions{}, fmt.Errorf("qdrant URL, collection, and output are required")
	}
	if defaults.supersededRetentionDays <= 0 || defaults.supersededRetentionDays > maxDays || defaults.staleDays <= 0 || defaults.staleDays > maxDays || defaults.lowRecallThreshold < 0 {
		return analyzeOptions{}, fmt.Errorf("retention days must be positive and low recall threshold non-negative")
	}
	if reference != "" {
		parsed, err := time.Parse(time.RFC3339, reference)
		if err != nil {
			return analyzeOptions{}, fmt.Errorf("reference-time must use RFC3339")
		}
		defaults.referenceTime = parsed
	}
	return defaults, nil
}
