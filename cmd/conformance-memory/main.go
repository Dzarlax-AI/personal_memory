package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

const defaultTimeout = 2 * time.Minute

var safeEnvironmentName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

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
		return fmt.Errorf("usage: conformance-memory run [flags]")
	}
	if args[0] != "run" {
		return fmt.Errorf("unknown subcommand %q; want run", args[0])
	}
	return runCommand(args[1:], stdout, stderr)
}

func runCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("source", "fixture", "fixture or live")
	suitePath := flags.String("suite", "", "scenario suite JSON path")
	contractPath := flags.String("contract", "", "normative contract Markdown path")
	tracesPath := flags.String("traces", "", "fixture trace bundle JSON path")
	clientFamily := flags.String("client-family", "", "live client: codex, claude, chatgpt, or generic_mcp")
	adapterExec := flags.String("adapter-exec", "", "absolute live adapter executable path")
	adapterOutputLimit := flags.Int("adapter-output-limit", 1<<20, "maximum adapter stdout bytes")
	jsonPath := flags.String("json", "", "JSON report output path")
	markdownPath := flags.String("markdown", "", "Markdown report output path")
	timeout := flags.Duration("timeout", defaultTimeout, "overall conformance timeout")
	var adapterArgs stringListFlag
	var adapterEnv stringListFlag
	flags.Var(&adapterArgs, "adapter-arg", "adapter argument; repeatable")
	flags.Var(&adapterEnv, "adapter-env", "environment variable name passed to adapter; repeatable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("run accepts no positional arguments")
	}
	if *suitePath == "" || *contractPath == "" || *jsonPath == "" || *markdownPath == "" {
		return fmt.Errorf("--suite, --contract, --json, and --markdown are required")
	}
	if *timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	inputPaths := []string{*suitePath, *contractPath}
	if *source == "fixture" {
		if *tracesPath == "" {
			return fmt.Errorf("--traces is required with --source fixture")
		}
		inputPaths = append(inputPaths, *tracesPath)
		if *clientFamily != "" || *adapterExec != "" || len(adapterArgs) != 0 || len(adapterEnv) != 0 {
			return fmt.Errorf("live adapter flags require --source live")
		}
	} else if *source == "live" {
		if *tracesPath != "" {
			return fmt.Errorf("--traces is supported only with --source fixture")
		}
		if *clientFamily == "" || *adapterExec == "" {
			return fmt.Errorf("--client-family and --adapter-exec are required with --source live")
		}
	} else {
		return fmt.Errorf("--source must be fixture or live")
	}
	if err := ensureDistinctPaths(append(inputPaths, *jsonPath, *markdownPath)); err != nil {
		return err
	}
	suite, err := loadSuiteFile(*suitePath)
	if err != nil {
		return err
	}
	catalog, err := loadContractFile(*contractPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var report conformance.Report
	if *source == "fixture" {
		bundle, err := loadTraceFile(*tracesPath)
		if err != nil {
			return err
		}
		report, err = conformance.Run(suite, bundle, catalog, "fixture")
		if err != nil {
			return err
		}
	} else {
		environment, err := selectedEnvironment(adapterEnv)
		if err != nil {
			return err
		}
		adapter, err := conformance.NewCommandAdapter(conformance.CommandAdapterOptions{
			ClientFamily: conformance.ClientFamily(*clientFamily),
			Executable:   *adapterExec, Args: adapterArgs,
			Environment: environment, OutputLimit: *adapterOutputLimit,
		})
		if err != nil {
			return err
		}
		report, err = conformance.RunAdapter(ctx, suite, catalog, adapter)
		if err != nil {
			return err
		}
	}
	jsonReport, err := conformance.RenderJSON(report)
	if err != nil {
		return err
	}
	if err := writeAtomic(*jsonPath, jsonReport, 0o644); err != nil {
		return fmt.Errorf("write JSON report: %w", err)
	}
	if err := writeAtomic(*markdownPath, []byte(conformance.RenderMarkdown(report)), 0o644); err != nil {
		return fmt.Errorf("write Markdown report: %w", err)
	}
	fmt.Fprintf(stdout, "evaluated %d client-scenarios; gates_passed=%t\n",
		len(report.Results), report.GatesPassed)
	if !report.GatesPassed {
		return &gateFailureError{aggregate: report.Aggregate}
	}
	return nil
}

func loadSuiteFile(path string) (*conformance.Suite, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open suite: %w", err)
	}
	defer file.Close()
	suite, err := conformance.LoadSuite(file)
	if err != nil {
		return nil, err
	}
	return suite, nil
}

func loadTraceFile(path string) (*conformance.TraceBundle, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open traces: %w", err)
	}
	defer file.Close()
	bundle, err := conformance.LoadTraceBundle(file)
	if err != nil {
		return nil, err
	}
	return bundle, nil
}

func loadContractFile(path string) (conformance.ContractCatalog, error) {
	file, err := os.Open(path)
	if err != nil {
		return conformance.ContractCatalog{}, fmt.Errorf("open contract: %w", err)
	}
	defer file.Close()
	catalog, err := conformance.LoadContractCatalog(file)
	if err != nil {
		return conformance.ContractCatalog{}, err
	}
	return catalog, nil
}

func selectedEnvironment(names []string) ([]string, error) {
	environment := make([]string, 0, len(names))
	seen := map[string]struct{}{}
	for _, name := range names {
		if !safeEnvironmentName.MatchString(name) {
			return nil, fmt.Errorf("adapter environment name %q is invalid", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("adapter environment name %q is duplicated", name)
		}
		seen[name] = struct{}{}
		value, exists := os.LookupEnv(name)
		if !exists {
			return nil, fmt.Errorf("adapter environment variable %q is not set", name)
		}
		environment = append(environment, name+"="+value)
	}
	return environment, nil
}

func ensureDistinctPaths(paths []string) error {
	for i := range paths {
		for j := 0; j < i; j++ {
			same, err := samePath(paths[i], paths[j])
			if err != nil {
				return fmt.Errorf("compare paths %q and %q: %w", paths[j], paths[i], err)
			}
			if same {
				return fmt.Errorf("paths %q and %q must be different", paths[j], paths[i])
			}
		}
	}
	return nil
}

func samePath(left, right string) (bool, error) {
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

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".conformance-memory-*")
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

type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ",") }

func (values *stringListFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type gateFailureError struct {
	aggregate conformance.Aggregate
}

func (errorValue *gateFailureError) Error() string {
	return fmt.Sprintf(
		"conformance gates failed: fail=%d inconclusive=%d error=%d",
		errorValue.aggregate.Fail, errorValue.aggregate.Inconclusive, errorValue.aggregate.Error,
	)
}
