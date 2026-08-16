package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/maintenance"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

const day = 24 * time.Hour
const maxDays = 36500
const managedQdrantSnapshotDir = "/qdrant/snapshots"

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

type mutationOptions struct {
	qdrantURL             string
	collection            string
	manifest              string
	journal               string
	snapshotArchive       string
	pointIDs              pointIDs
	eligible              bool
	confirmServerStopped  bool
	confirmPurge          bool
	minimumQuarantineDays int
}

type pointIDs []string

func (ids *pointIDs) String() string { return strings.Join(*ids, ",") }
func (ids *pointIDs) Set(value string) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("point ID is required")
	}
	*ids = append(*ids, value)
	return nil
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
	if len(args) == 0 {
		return fmt.Errorf("usage: maintenance <analyze|quarantine|restore|purge> [flags]")
	}
	switch args[0] {
	case "analyze":
		return runAnalyze(ctx, args[1:], stdout, now)
	case "quarantine", "restore":
		return runMutation(ctx, args[0], args[1:], stdout)
	case "purge":
		return runPurge(ctx, args[1:], stdout)
	default:
		return fmt.Errorf("usage: maintenance <analyze|quarantine|restore|purge> [flags]")
	}
}

func runPurge(ctx context.Context, args []string, stdout io.Writer) error {
	options, err := parsePurgeOptions(args)
	if err != nil {
		return err
	}
	manifest, err := maintenance.ReadManifest(options.manifest)
	if err != nil {
		return err
	}
	service, err := maintenance.NewService(qdrant.NewClient(options.qdrantURL, options.collection), options.collection, nil)
	if err != nil {
		return fmt.Errorf("maintenance purge is not configured")
	}
	result, err := service.Purge(ctx, maintenance.PurgeRequest{
		Manifest:             manifest,
		Selection:            maintenance.Selection{PointIDs: []string(options.pointIDs)},
		JournalPath:          options.journal,
		SnapshotArchivePath:  options.snapshotArchive,
		MinimumQuarantineAge: time.Duration(options.minimumQuarantineDays) * day,
	})
	if err != nil {
		return fmt.Errorf("maintenance purge could not complete")
	}
	return writeMutationSummary(stdout, "purge", result)
}

func runAnalyze(ctx context.Context, args []string, stdout io.Writer, now func() time.Time) error {
	options, err := parseAnalyzeOptions(args, now())
	if err != nil {
		return err
	}
	manifest, err := maintenance.Analyze(ctx, qdrant.NewClient(options.qdrantURL, options.collection), maintenance.Options{
		Collection: options.collection, Namespace: options.namespace, ReferenceTime: options.referenceTime,
		SupersededRetention: time.Duration(options.supersededRetentionDays) * day,
		StaleAfter:          time.Duration(options.staleDays) * day, LowRecallThreshold: options.lowRecallThreshold,
	})
	if err != nil {
		return fmt.Errorf("analysis could not complete")
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

func runMutation(ctx context.Context, operation string, args []string, stdout io.Writer) error {
	options, err := parseMutationOptions(operation, args)
	if err != nil {
		return err
	}
	manifest, err := maintenance.ReadManifest(options.manifest)
	if err != nil {
		return err
	}
	service, err := maintenance.NewService(qdrant.NewClient(options.qdrantURL, options.collection), options.collection, nil)
	if err != nil {
		return fmt.Errorf("maintenance action is not configured")
	}
	request := maintenance.Request{Manifest: manifest, JournalPath: options.journal, Selection: maintenance.Selection{PointIDs: []string(options.pointIDs), IncludeEligibleFindings: options.eligible}}
	var result maintenance.Result
	if operation == "quarantine" {
		result, err = service.Quarantine(ctx, request)
	} else {
		result, err = service.Restore(ctx, request)
	}
	if err != nil {
		return fmt.Errorf("maintenance action could not complete")
	}
	return writeMutationSummary(stdout, operation, result)
}

func writeMutationSummary(stdout io.Writer, operation string, result maintenance.Result) error {
	counts := map[maintenance.OutcomeStatus]int{}
	for _, outcome := range result.Outcomes {
		counts[outcome.Status]++
	}
	if operation == "purge" {
		if _, err := fmt.Fprintf(stdout, "mode=purge batch_id=%s deleted=%d already_applied=%d pending=%d dispatching=%d not_found=%d protected_or_ineligible=%d conflict=%d failed=%d ambiguous=%d\n", result.BatchID, counts[maintenance.OutcomeDeleted], counts[maintenance.OutcomeAlreadyApplied], counts[maintenance.OutcomePending], counts[maintenance.OutcomeDispatching], counts[maintenance.OutcomeNotFound], counts[maintenance.OutcomeProtectedOrIneligible], counts[maintenance.OutcomeConflict], counts[maintenance.OutcomeFailed], counts[maintenance.OutcomeAmbiguous]); err != nil {
			return err
		}
		if len(result.Outcomes) == 0 || counts[maintenance.OutcomeDeleted]+counts[maintenance.OutcomeAlreadyApplied] != len(result.Outcomes) {
			return fmt.Errorf("maintenance purge completed with non-success outcomes")
		}
		return nil
	}
	if _, err := fmt.Fprintf(stdout, "mode=%s batch_id=%s updated=%d already_applied=%d not_found=%d protected_or_ineligible=%d conflict=%d failed=%d ambiguous=%d\n", operation, result.BatchID, counts[maintenance.OutcomeUpdated], counts[maintenance.OutcomeAlreadyApplied], counts[maintenance.OutcomeNotFound], counts[maintenance.OutcomeProtectedOrIneligible], counts[maintenance.OutcomeConflict], counts[maintenance.OutcomeFailed], counts[maintenance.OutcomeAmbiguous]); err != nil {
		return err
	}
	if counts[maintenance.OutcomeFailed] > 0 || counts[maintenance.OutcomeAmbiguous] > 0 {
		return fmt.Errorf("maintenance action completed with unresolved outcomes")
	}
	return nil
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

func parseMutationOptions(operation string, args []string) (mutationOptions, error) {
	options := mutationOptions{}
	set := flag.NewFlagSet("maintenance "+operation, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&options.qdrantURL, "qdrant-url", "", "Qdrant base URL")
	set.StringVar(&options.collection, "collection", "", "collection name")
	set.StringVar(&options.manifest, "manifest", "", "saved analysis manifest")
	set.StringVar(&options.journal, "journal", "", "private result journal path")
	set.Var(&options.pointIDs, "point-id", "manifest point ID (repeatable)")
	set.BoolVar(&options.confirmServerStopped, "confirm-server-stopped", false, "confirm memory-mcp and other collection writers are stopped")
	if operation == "quarantine" {
		set.BoolVar(&options.eligible, "eligible", false, "select all eligible manifest findings")
	}
	if err := set.Parse(args); err != nil {
		return mutationOptions{}, err
	}
	if set.NArg() != 0 {
		return mutationOptions{}, fmt.Errorf("unexpected arguments")
	}
	if strings.TrimSpace(options.qdrantURL) == "" || strings.TrimSpace(options.collection) == "" || strings.TrimSpace(options.manifest) == "" || strings.TrimSpace(options.journal) == "" {
		return mutationOptions{}, fmt.Errorf("qdrant URL, collection, manifest, and journal are required")
	}
	if len(options.pointIDs) == 0 && !options.eligible {
		return mutationOptions{}, fmt.Errorf("at least one point ID or --eligible is required")
	}
	if !options.confirmServerStopped {
		return mutationOptions{}, fmt.Errorf("--confirm-server-stopped is required")
	}
	if operation != "quarantine" && options.eligible {
		return mutationOptions{}, fmt.Errorf("eligible selection is only supported for quarantine")
	}
	return options, nil
}

func parsePurgeOptions(args []string) (mutationOptions, error) {
	options := mutationOptions{}
	set := flag.NewFlagSet("maintenance purge", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&options.qdrantURL, "qdrant-url", "", "Qdrant base URL")
	set.StringVar(&options.collection, "collection", "", "collection name")
	set.StringVar(&options.manifest, "manifest", "", "saved analysis manifest")
	set.StringVar(&options.journal, "journal", "", "private result journal path")
	set.StringVar(&options.snapshotArchive, "snapshot-archive", "", "private recovery snapshot archive path")
	set.Var(&options.pointIDs, "point-id", "manifest point ID (repeatable)")
	set.IntVar(&options.minimumQuarantineDays, "minimum-quarantine-days", 0, "minimum completed quarantine days")
	set.BoolVar(&options.confirmServerStopped, "confirm-server-stopped", false, "confirm memory-mcp and other collection writers are stopped")
	set.BoolVar(&options.confirmPurge, "confirm-purge", false, "confirm permanent deletion of the explicit point IDs")
	if err := set.Parse(args); err != nil {
		return mutationOptions{}, err
	}
	if set.NArg() != 0 {
		return mutationOptions{}, fmt.Errorf("unexpected arguments")
	}
	if strings.TrimSpace(options.qdrantURL) == "" || strings.TrimSpace(options.collection) == "" || strings.TrimSpace(options.manifest) == "" || strings.TrimSpace(options.journal) == "" || strings.TrimSpace(options.snapshotArchive) == "" {
		return mutationOptions{}, fmt.Errorf("qdrant URL, collection, manifest, journal, and snapshot archive are required")
	}
	if pathWithin(managedQdrantSnapshotDir, options.snapshotArchive) {
		return mutationOptions{}, fmt.Errorf("snapshot archive must be outside Qdrant's managed snapshot directory")
	}
	if len(options.pointIDs) == 0 || len(options.pointIDs) > maintenance.MaxSelectionSize {
		return mutationOptions{}, fmt.Errorf("explicit point IDs must be non-empty and bounded")
	}
	seen := make(map[string]struct{}, len(options.pointIDs))
	for _, id := range options.pointIDs {
		if _, duplicate := seen[id]; duplicate {
			return mutationOptions{}, fmt.Errorf("duplicate point ID")
		}
		seen[id] = struct{}{}
	}
	if options.minimumQuarantineDays <= 0 || options.minimumQuarantineDays > maxDays {
		return mutationOptions{}, fmt.Errorf("minimum quarantine days must be positive and bounded")
	}
	if !options.confirmServerStopped {
		return mutationOptions{}, fmt.Errorf("--confirm-server-stopped is required")
	}
	if !options.confirmPurge {
		return mutationOptions{}, fmt.Errorf("--confirm-purge is required")
	}
	return options, nil
}

func pathWithin(root, candidate string) bool {
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return false
	}
	candidate, err = filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
