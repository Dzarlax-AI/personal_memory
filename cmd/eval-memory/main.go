package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	memoryeval "github.com/Dzarlax-AI/personal-memory/internal/eval"
)

const defaultTimeout = 2 * time.Minute

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var gateErr *gateFailureError
		if errors.As(err, &gateErr) {
			os.Exit(3)
		}
		os.Exit(1)
	}
}

func runCLI(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: eval-memory <run|compare|materialize> [flags]")
	}
	switch args[0] {
	case "run":
		return runCommand(args[1:], stdout, stderr)
	case "compare":
		return compareCommand(args[1:], stdout, stderr)
	case "materialize":
		return materializeCommand(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown subcommand %q; want run, compare, or materialize", args[0])
	}
}

func runCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("source", "fixture", "fixture, live, or tei-fixture")
	datasetPath := flags.String("dataset", "", "dataset JSON path")
	qdrantURL := flags.String("qdrant-url", "http://127.0.0.1:6333", "Qdrant base URL")
	embedURL := flags.String("embed-url", os.Getenv("EMBED_URL"), "TEI base URL for live/tei-fixture embedding")
	embedModel := flags.String("embed-model", os.Getenv("EMBED_MODEL"), "TEI model ID (defaults to dataset identity)")
	documentsRoot := flags.String("documents-root", "", "document root used to make live lexical paths relative")
	configurationName := flags.String("configuration-name", "", "override experiment configuration name")
	inputProfile := flags.String("input-profile", "", "override experiment input profile")
	retrievalStrategy := flags.String("retrieval-strategy", "", "override retrieval strategy")
	denseCandidateLimit := flags.Int("dense-candidate-limit", -1, "override hybrid dense candidate limit")
	rrfConstant := flags.Int("rrf-constant", -1, "override hybrid RRF constant")
	jsonPath := flags.String("json", "", "JSON report output path")
	markdownPath := flags.String("markdown", "", "Markdown report output path")
	timeout := flags.Duration("timeout", defaultTimeout, "overall evaluation timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("run accepts no positional arguments")
	}
	if *datasetPath == "" || *jsonPath == "" || *markdownPath == "" {
		return fmt.Errorf("--dataset, --json, and --markdown are required")
	}
	samePath, err := sameOutputPath(*jsonPath, *markdownPath)
	if err != nil {
		return fmt.Errorf("compare report output paths: %w", err)
	}
	if samePath {
		return fmt.Errorf("--json and --markdown must refer to different files")
	}
	if *timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	dataset, err := loadDatasetFile(*datasetPath)
	if err != nil {
		return err
	}

	var overrides memoryeval.ExperimentOverrides
	if *configurationName != "" {
		overrides.ConfigurationName = configurationName
	}
	if *inputProfile != "" {
		if *source == "fixture" {
			return fmt.Errorf("--input-profile cannot relabel precomputed fixture vectors")
		}
		value := memoryeval.InputProfile(*inputProfile)
		overrides.InputProfile = &value
	}
	if *retrievalStrategy != "" {
		value := memoryeval.RetrievalStrategy(*retrievalStrategy)
		overrides.RetrievalStrategy = &value
	}
	if *denseCandidateLimit >= 0 {
		overrides.DenseCandidateLimit = denseCandidateLimit
	}
	if *rrfConstant >= 0 {
		overrides.RRFConstant = rrfConstant
	}
	dataset, err = memoryeval.WithExperimentOverrides(dataset, overrides, *source)
	if err != nil {
		return fmt.Errorf("apply experiment overrides: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if *source == "live" && strings.TrimSpace(*documentsRoot) == "" {
		*documentsRoot = os.Getenv("RAG_DOCUMENTS_DIR")
		if strings.TrimSpace(*documentsRoot) == "" {
			*documentsRoot = "/root/documents/personal"
		}
	}
	options := memoryeval.RunOptions{
		Source: *source, QdrantURL: strings.TrimRight(*qdrantURL, "/"),
		DocumentsRoot: *documentsRoot,
	}
	needsEmbedding := *source == "tei-fixture"
	if *source == "live" {
		for _, query := range dataset.Queries {
			if len(query.Vector) == 0 {
				needsEmbedding = true
				break
			}
		}
	}
	if needsEmbedding && *embedURL != "" {
		client := embeddings.NewClient(strings.TrimRight(*embedURL, "/"))
		info, err := client.Info(ctx)
		if err != nil {
			return fmt.Errorf("read TEI identity: %w", err)
		}
		if info.ModelID != dataset.Embedding.ModelID || info.ModelSHA != dataset.Embedding.ModelRevision ||
			info.ModelDType != dataset.Embedding.DType || info.ModelType.Embedding.Pooling != dataset.Embedding.Pooling {
			return fmt.Errorf("TEI identity does not match dataset embedding identity")
		}
		if *embedModel != "" && *embedModel != info.ModelID {
			return fmt.Errorf("TEI model does not match --embed-model")
		}
		options.Embed = client.Embed
		options.Embedder = client
	} else if needsEmbedding {
		return fmt.Errorf("--embed-url or EMBED_URL is required for %s query embedding", *source)
	}
	report, err := memoryeval.Run(ctx, dataset, options)
	if err != nil {
		return err
	}
	jsonReport, err := memoryeval.RenderJSON(report)
	if err != nil {
		return err
	}
	if err := writeAtomic(*jsonPath, jsonReport, 0o644); err != nil {
		return fmt.Errorf("write JSON report: %w", err)
	}
	if err := writeAtomic(*markdownPath, []byte(memoryeval.RenderMarkdown(report)), 0o644); err != nil {
		return fmt.Errorf("write Markdown report: %w", err)
	}
	fmt.Fprintf(stdout, "evaluated %d queries; gates_passed=%t\n", len(report.Queries), report.GatesPassed)
	if !report.GatesPassed {
		return &gateFailureError{failures: report.GateFailures}
	}
	return nil
}

func materializeCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("materialize", flag.ContinueOnError)
	flags.SetOutput(stderr)
	datasetPath := flags.String("dataset", "", "strict schema-v3 materialization input")
	outputPath := flags.String("output", "", "materialized dataset JSON path")
	embedURL := flags.String("embed-url", "", "TEI base URL")
	embedModel := flags.String("embed-model", "", "optional TEI model ID assertion")
	inputProfile := flags.String("input-profile", "", "optional materialized input profile")
	timeout := flags.Duration("timeout", defaultTimeout, "overall materialization timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("materialize accepts no positional arguments")
	}
	if *datasetPath == "" || *outputPath == "" || strings.TrimSpace(*embedURL) == "" {
		return fmt.Errorf("--dataset, --output, and --embed-url are required")
	}
	samePath, err := sameOutputPath(*datasetPath, *outputPath)
	if err != nil {
		return fmt.Errorf("compare materialization paths: %w", err)
	}
	if samePath {
		return fmt.Errorf("--dataset and --output must refer to different files")
	}
	if *timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	dataset, err := loadMaterializationDatasetFile(*datasetPath)
	if err != nil {
		return err
	}
	if dataset.Embedding.Provider != "tei" {
		return fmt.Errorf("materialization requires a TEI dataset embedding identity")
	}
	if *inputProfile != "" {
		if err := embeddings.ValidateInputProfile(
			embeddings.InputProfile(*inputProfile), dataset.Embedding.ModelID,
		); err != nil {
			return fmt.Errorf("invalid --input-profile")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client := embeddings.NewClient(strings.TrimRight(*embedURL, "/"))
	info, err := client.Info(ctx)
	if err != nil {
		return fmt.Errorf("read TEI identity failed")
	}
	if info.ModelID != dataset.Embedding.ModelID ||
		info.ModelSHA != dataset.Embedding.ModelRevision ||
		info.ModelDType != dataset.Embedding.DType ||
		info.ModelType.Embedding.Pooling != dataset.Embedding.Pooling {
		return fmt.Errorf("TEI identity does not match dataset embedding identity")
	}
	if *embedModel != "" && *embedModel != info.ModelID {
		return fmt.Errorf("TEI model does not match --embed-model")
	}

	var options memoryeval.MaterializeOptions
	if *inputProfile != "" {
		profile := memoryeval.InputProfile(*inputProfile)
		options.InputProfile = &profile
	}
	materialized, diagnostics, err := memoryeval.Materialize(ctx, dataset, client, options)
	if err != nil {
		var embedErr *memoryeval.MaterializationEmbeddingError
		if errors.As(err, &embedErr) {
			return fmt.Errorf("materialize dataset: embed %s failed", embedErr.Batch)
		}
		return fmt.Errorf("materialize dataset: %w", err)
	}
	data, err := memoryeval.RenderDatasetJSON(materialized)
	if err != nil {
		return fmt.Errorf("render materialized dataset: %w", err)
	}
	if err := writeAtomic(*outputPath, data, 0o600); err != nil {
		return fmt.Errorf("write materialized dataset: %w", err)
	}
	fmt.Fprintf(
		stdout,
		"materialized facts=%d chunks=%d folders=%d queries=%d profile=%s\n",
		diagnostics.Facts, diagnostics.Chunks, diagnostics.Folders,
		diagnostics.Queries, diagnostics.InputProfile,
	)
	return nil
}

func sameOutputPath(left, right string) (bool, error) {
	leftPath, err := filepath.Abs(filepath.Clean(left))
	if err != nil {
		return false, err
	}
	rightPath, err := filepath.Abs(filepath.Clean(right))
	if err != nil {
		return false, err
	}
	if leftPath == rightPath {
		return true, nil
	}
	leftInfo, leftErr := os.Stat(leftPath)
	rightInfo, rightErr := os.Stat(rightPath)
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo), nil
	}
	for _, statErr := range []error{leftErr, rightErr} {
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return false, statErr
		}
	}
	return false, nil
}

func compareCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("compare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	baselinePath := flags.String("baseline", "", "baseline JSON report")
	candidatePath := flags.String("candidate", "", "candidate JSON report")
	outputPath := flags.String("json", "", "optional comparison JSON output path")
	enforceGates := flags.Bool("enforce-gates", false, "return a failing exit when candidate gates fail")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *baselinePath == "" || *candidatePath == "" {
		return fmt.Errorf("--baseline and --candidate are required")
	}
	baseline, err := loadReportFile(*baselinePath)
	if err != nil {
		return fmt.Errorf("load baseline: %w", err)
	}
	candidate, err := loadReportFile(*candidatePath)
	if err != nil {
		return fmt.Errorf("load candidate: %w", err)
	}
	comparison, err := memoryeval.Compare(baseline, candidate, *enforceGates)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(comparison, "", "  ")
	if err != nil {
		return fmt.Errorf("encode comparison: %w", err)
	}
	data = append(data, '\n')
	if *outputPath != "" {
		if err := writeAtomic(*outputPath, data, 0o644); err != nil {
			return fmt.Errorf("write comparison: %w", err)
		}
	} else if _, err := stdout.Write(data); err != nil {
		return fmt.Errorf("write comparison output: %w", err)
	}
	if !comparison.GatesPassed {
		return &gateFailureError{failures: comparison.GateFailures}
	}
	return nil
}

func loadDatasetFile(path string) (*memoryeval.Dataset, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open dataset: %w", err)
	}
	dataset, loadErr := memoryeval.Load(file)
	closeErr := file.Close()
	if loadErr != nil {
		return nil, fmt.Errorf("load dataset: %w", loadErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close dataset: %w", closeErr)
	}
	return dataset, nil
}

func loadMaterializationDatasetFile(path string) (*memoryeval.Dataset, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open materialization dataset: %w", err)
	}
	dataset, loadErr := memoryeval.LoadForMaterialization(file)
	closeErr := file.Close()
	if loadErr != nil {
		return nil, fmt.Errorf("load materialization dataset: %w", loadErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close materialization dataset: %w", closeErr)
	}
	return dataset, nil
}

func loadReportFile(path string) (memoryeval.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return memoryeval.Report{}, err
	}
	return memoryeval.DecodeReport(data)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".eval-memory-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}()
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

type gateFailureError struct {
	failures []string
}

func (e *gateFailureError) Error() string {
	return "evaluation gates failed: " + strings.Join(e.failures, "; ")
}
