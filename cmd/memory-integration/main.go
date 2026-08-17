package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/integrationbundle"
	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "command is required: install, update, verify, rollback, render, or conformance-adapter")
		return 2
	}
	command := args[0]
	if strings.HasPrefix(command, "quick-") {
		return runQuick(command, args[1:], stdout, stderr)
	}
	if command == "conformance-adapter" {
		return runConformanceAdapter(args[1:], stdin, stdout, stderr)
	}
	if command != "install" && command != "update" && command != "verify" && command != "rollback" && command != "render" {
		fmt.Fprintln(stderr, "unknown command")
		return 2
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	clientValue := fs.String("client", "", "client family")
	targetRoot := fs.String("target-root", "", "selected client configuration root (Codex usually ~/.codex; Claude usually ~/.claude; ChatGPT/generic require an explicit directory)")
	dryRun := fs.Bool("dry-run", false, "plan without writes")
	discoveryPerformed := fs.Bool("discovery-performed", false, "confirm explicit tool discovery")
	discoveryFile := fs.String("discovery-file", "", "explicit discovery JSON file")
	contractPath := fs.String("contract-source", filepath.Join("docs", "model-usage-contract.md"), "normative contract source")
	suitePath := fs.String("suite-source", filepath.Join("conformancedata", "public", "v1", "scenarios.json"), "public conformance suite source")
	var caps, tools stringsFlag
	fs.Var(&caps, "capability", "capability state, e.g. memory=available (repeatable)")
	fs.Var(&tools, "tool", "discovered tool name (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected positional arguments")
		return 2
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	allowed := map[string]map[string]bool{
		"install":  {"client": true, "target-root": true, "dry-run": true, "contract-source": true, "suite-source": true, "capability": true, "discovery-file": true},
		"update":   {"client": true, "target-root": true, "dry-run": true, "contract-source": true, "suite-source": true, "capability": true, "discovery-file": true},
		"verify":   {"client": true, "target-root": true, "contract-source": true, "suite-source": true, "capability": true, "discovery-file": true, "discovery-performed": true, "tool": true},
		"rollback": {"client": true, "target-root": true, "contract-source": true, "suite-source": true, "capability": true},
		"render":   {"client": true, "target-root": true, "contract-source": true, "suite-source": true},
	}
	for name := range visited {
		if !allowed[command][name] {
			fmt.Fprintf(stderr, "--%s is not valid for %s\n", name, command)
			return 2
		}
	}
	if *clientValue == "" {
		fmt.Fprintln(stderr, "--client is required")
		return 2
	}
	if *targetRoot == "" {
		fmt.Fprintln(stderr, "--target-root is required")
		return 2
	}
	config, err := parseCapabilities(caps)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	contract, err := os.ReadFile(*contractPath)
	if err != nil {
		fmt.Fprintln(stderr, "cannot read contract source")
		return 1
	}
	suite, err := os.ReadFile(*suitePath)
	if err != nil {
		fmt.Fprintln(stderr, "cannot read conformance source")
		return 1
	}
	bundle, err := integrationbundle.Load(contract, suite)
	if err != nil {
		fmt.Fprintln(stderr, "bundle validation failed")
		return 1
	}
	sets, err := bundle.Render(config)
	if err != nil {
		fmt.Fprintln(stderr, "bundle render failed")
		return 1
	}
	discovery := integrationbundle.Discovery{Performed: *discoveryPerformed, Tools: tools}
	if *discoveryFile != "" {
		if *discoveryPerformed || len(tools) > 0 {
			fmt.Fprintln(stderr, "discovery flags and --discovery-file are mutually exclusive")
			return 2
		}
		file, e := os.Open(*discoveryFile)
		if e != nil {
			fmt.Fprintln(stderr, "cannot read discovery file")
			return 1
		}
		fromFile, decodeErr := decodeDiscovery(file)
		closeErr := file.Close()
		if decodeErr != nil {
			fmt.Fprintln(stderr, "invalid discovery file")
			return 2
		}
		if closeErr != nil {
			fmt.Fprintln(stderr, "cannot read discovery file")
			return 1
		}
		discovery = fromFile
	}
	client, err := parseClient(*clientValue)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	_, ok := selectSet(sets, client)
	if !ok {
		fmt.Fprintln(stderr, "client artifact set is unavailable")
		return 1
	}
	switch command {
	case "rollback":
		r, e := integrationbundle.Rollback(integrationbundle.RollbackOptions{TargetRoot: *targetRoot, Bundle: bundle, Client: client, Config: config})
		return emit(r, e, stdout, stderr)
	case "install":
		r, e := integrationbundle.Install(integrationbundle.InstallOptions{TargetRoot: *targetRoot, Bundle: bundle, Client: client, Config: config, Discovery: discovery, DryRun: *dryRun})
		return emit(r, e, stdout, stderr)
	case "update":
		r, e := integrationbundle.Update(integrationbundle.InstallOptions{TargetRoot: *targetRoot, Bundle: bundle, Client: client, Config: config, Discovery: discovery, DryRun: *dryRun})
		return emit(r, e, stdout, stderr)
	case "verify":
		r, e := integrationbundle.Verify(integrationbundle.VerifyOptions{TargetRoot: *targetRoot, Bundle: bundle, Client: client, Config: config, Discovery: discovery})
		code := emit(r, e, stdout, stderr)
		if code == 0 && r.Status != integrationbundle.StatusInstalled && r.Status != integrationbundle.StatusManualActionRequired {
			return 3
		}
		return code
	case "render":
		r, e := integrationbundle.WriteRendered(*targetRoot, bundle, client, config)
		return emit(r, e, stdout, stderr)
	}
	return 2
}

func decodeDiscovery(input io.Reader) (integrationbundle.Discovery, error) {
	const inputLimit = 1 << 20
	var discovery integrationbundle.Discovery
	data, err := io.ReadAll(io.LimitReader(input, inputLimit+1))
	if err != nil {
		return discovery, err
	}
	if len(data) > inputLimit {
		return discovery, fmt.Errorf("discovery input exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&discovery); err != nil {
		return discovery, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return discovery, fmt.Errorf("trailing JSON value")
		}
		return discovery, err
	}
	return discovery, nil
}

func runConformanceAdapter(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("conformance-adapter", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	contractPath := fs.String("contract-source", filepath.Join("docs", "model-usage-contract.md"), "normative contract source")
	suitePath := fs.String("suite-source", filepath.Join("conformancedata", "public", "v1", "scenarios.json"), "public conformance suite source")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "invalid adapter arguments")
		return 2
	}
	contract, err := os.ReadFile(*contractPath)
	if err != nil {
		fmt.Fprintln(stderr, "cannot read contract source")
		return 1
	}
	suite, err := os.ReadFile(*suitePath)
	if err != nil {
		fmt.Fprintln(stderr, "cannot read conformance source")
		return 1
	}
	bundle, err := integrationbundle.Load(contract, suite)
	if err != nil {
		fmt.Fprintln(stderr, "bundle validation failed")
		return 1
	}
	request, err := decodeAdapterRequest(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "invalid adapter request")
		return 2
	}
	trace, err := bundle.ConformanceTrace(request)
	if err != nil {
		fmt.Fprintln(stderr, "adapter trace unavailable")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(trace); err != nil {
		return 1
	}
	return 0
}

func decodeAdapterRequest(input io.Reader) (conformance.AdapterRequest, error) {
	const inputLimit = 1 << 20
	var request conformance.AdapterRequest
	data, err := io.ReadAll(io.LimitReader(input, inputLimit+1))
	if err != nil {
		return conformance.AdapterRequest{}, err
	}
	if len(data) > inputLimit {
		return conformance.AdapterRequest{}, fmt.Errorf("adapter request exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return conformance.AdapterRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return conformance.AdapterRequest{}, fmt.Errorf("trailing JSON value")
		}
		return conformance.AdapterRequest{}, err
	}
	return request, nil
}

func emit(value any, err error, stdout, stderr io.Writer) int {
	if err != nil {
		fmt.Fprintln(stderr, safeError(err))
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(value); err != nil {
		return 1
	}
	return 0
}
func safeError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, os.ErrPermission):
		return "operation denied"
	default:
		return err.Error()
	}
}
func parseClient(value string) (conformance.ClientFamily, error) {
	c := conformance.ClientFamily(value)
	switch c {
	case conformance.ClientCodex, conformance.ClientClaude, conformance.ClientChatGPT, conformance.ClientGenericMCP:
		return c, nil
	}
	return "", fmt.Errorf("unsupported client")
}
func selectSet(sets []integrationbundle.ArtifactSet, c conformance.ClientFamily) (integrationbundle.ArtifactSet, bool) {
	for _, s := range sets {
		if s.ClientID == c {
			return s, true
		}
	}
	return integrationbundle.ArtifactSet{}, false
}
func parseCapabilities(values []string) (integrationbundle.CapabilityConfig, error) {
	c := integrationbundle.CapabilityConfig{Memory: integrationbundle.CapabilityUnavailable, Documents: integrationbundle.CapabilityUnavailable, Todoist: integrationbundle.CapabilityDisabled}
	seen := map[string]bool{}
	for _, v := range values {
		parts := strings.Split(v, "=")
		if len(parts) != 2 || seen[parts[0]] {
			return c, fmt.Errorf("invalid or duplicate --capability")
		}
		state := integrationbundle.CapabilityState(parts[1])
		if state != integrationbundle.CapabilityAvailable && state != integrationbundle.CapabilityDisabled && state != integrationbundle.CapabilityUnavailable {
			return c, fmt.Errorf("invalid capability state")
		}
		switch parts[0] {
		case "memory":
			c.Memory = state
		case "documents":
			c.Documents = state
		case "todoist":
			c.Todoist = state
		default:
			return c, fmt.Errorf("unknown capability")
		}
		seen[parts[0]] = true
	}
	return c, nil
}
