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
		return fmt.Errorf("usage: eval-memory <run|compare> [flags]")
	}
	switch args[0] {
	case "run":
		return runCommand(args[1:], stdout, stderr)
	case "compare":
		return compareCommand(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown subcommand %q; want run or compare", args[0])
	}
}

func runCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("source", "fixture", "fixture or live")
	datasetPath := flags.String("dataset", "", "dataset JSON path")
	qdrantURL := flags.String("qdrant-url", "http://127.0.0.1:6333", "Qdrant base URL")
	embedURL := flags.String("embed-url", "", "optional TEI base URL for live queries without vectors")
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
	if *timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	dataset, err := loadDatasetFile(*datasetPath)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	options := memoryeval.RunOptions{Source: *source, QdrantURL: strings.TrimRight(*qdrantURL, "/")}
	if *embedURL != "" {
		if *source != "live" {
			return fmt.Errorf("--embed-url is supported only with --source live")
		}
		client := embeddings.NewClient(strings.TrimRight(*embedURL, "/"))
		info, err := client.Info(ctx)
		if err != nil {
			return fmt.Errorf("read TEI identity: %w", err)
		}
		if info.ModelID != dataset.Embedding.ModelID || info.ModelSHA != dataset.Embedding.ModelRevision ||
			info.ModelDType != dataset.Embedding.DType || info.ModelType.Embedding.Pooling != dataset.Embedding.Pooling {
			return fmt.Errorf("TEI identity does not match dataset embedding identity")
		}
		options.Embed = client.Embed
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
	defer file.Close()
	dataset, err := memoryeval.Load(file)
	if err != nil {
		return nil, fmt.Errorf("load dataset: %w", err)
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
