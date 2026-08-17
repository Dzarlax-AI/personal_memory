package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Dzarlax-AI/personal-memory/integrationbundle"
	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

type quickOutcome string

const (
	quickOutcomeInstalled  quickOutcome = "installed"
	quickOutcomeVerified   quickOutcome = "verified"
	quickOutcomeRolledBack quickOutcome = "rolled_back"
	quickOutcomeFailed     quickOutcome = "failed"
)

type quickCapabilities struct {
	Memory    string `json:"memory"`
	Documents string `json:"documents"`
	Todoist   string `json:"todoist"`
}

type quickResult struct {
	Client        string            `json:"client"`
	Root          string            `json:"root"`
	Capabilities  quickCapabilities `json:"capabilities"`
	BundleVersion string            `json:"bundle_version"`
	Outcome       quickOutcome      `json:"outcome"`
	Changed       bool              `json:"changed,omitempty"`
}

func runQuick(command string, args []string, stdout, stderr io.Writer) int {
	if command != "quick-install" && command != "quick-update" && command != "quick-verify" && command != "quick-rollback" {
		fmt.Fprintln(stderr, "unknown quick command")
		return 2
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "client is required: codex or claude")
		return 2
	}
	client, err := parseQuickClient(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	withDocuments := fs.Bool("with-documents", false, "enable document search")
	memoryOnly := fs.Bool("memory-only", false, "disable document search")
	confirmed := fs.Bool("confirm-tools-discovered", false, "confirm the tools are visible in the client session")
	jsonOutput := fs.Bool("json", false, "emit JSON")
	if err = fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return 2
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if command == "quick-rollback" && (visited["with-documents"] || visited["memory-only"] || visited["confirm-tools-discovered"]) {
		fmt.Fprintln(stderr, "quick-rollback accepts only --json")
		return 2
	}
	if *withDocuments && *memoryOnly {
		fmt.Fprintln(stderr, "--with-documents and --memory-only are mutually exclusive")
		return 2
	}
	if command != "quick-rollback" && !*confirmed {
		fmt.Fprintln(stderr, "--confirm-tools-discovered is required after observing the tools in the client")
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "cannot resolve the home directory")
		return 1
	}
	root := filepath.Join(home, "."+string(client))
	if info, statErr := os.Lstat(root); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		fmt.Fprintln(stderr, "the client configuration root must already exist as a real directory")
		return 1
	}
	bundle, err := integrationbundle.LoadEmbedded()
	if err != nil {
		fmt.Fprintln(stderr, "embedded bundle validation failed")
		return 1
	}

	transitionRequested := visited["with-documents"] || visited["memory-only"]
	config := quickPreset(*withDocuments)
	if command != "quick-install" && !transitionRequested {
		config, err = integrationbundle.LoadInstalledConfig(root, bundle, client, nil)
		if err != nil {
			fmt.Fprintln(stderr, safeError(err))
			return 1
		}
	}
	discovery := integrationbundle.Discovery{}
	if command != "quick-rollback" {
		discovery = integrationbundle.Discovery{Performed: true, Tools: integrationbundle.RequiredTools(config)}
	}

	result := quickResult{Client: string(client), Root: root, Capabilities: quickCapabilitiesFrom(config), BundleVersion: integrationbundle.BundleVersion}
	switch command {
	case "quick-install", "quick-update":
		options := integrationbundle.InstallOptions{TargetRoot: root, Bundle: bundle, Client: client, Config: config, Discovery: discovery}
		var applied integrationbundle.Result
		if command == "quick-install" {
			applied, err = integrationbundle.Install(options)
		} else {
			applied, err = integrationbundle.Update(options)
		}
		if err == nil {
			var verified integrationbundle.Result
			verified, err = integrationbundle.Verify(integrationbundle.VerifyOptions{TargetRoot: root, Bundle: bundle, Client: client, Config: config, Discovery: discovery})
			if err == nil && verified.Status != integrationbundle.StatusInstalled {
				err = fmt.Errorf("post-install verification failed")
			}
		}
		result.Changed = applied.Changed
		result.Outcome = quickOutcomeInstalled
	case "quick-verify":
		var verified integrationbundle.Result
		verified, err = integrationbundle.Verify(integrationbundle.VerifyOptions{TargetRoot: root, Bundle: bundle, Client: client, Config: config, Discovery: discovery})
		if err == nil && verified.Status != integrationbundle.StatusInstalled {
			err = fmt.Errorf("installation is not verified: %s", verified.Status)
		}
		result.Outcome = quickOutcomeVerified
	case "quick-rollback":
		var rolledBack integrationbundle.Result
		rolledBack, err = integrationbundle.Rollback(integrationbundle.RollbackOptions{TargetRoot: root, Bundle: bundle, Client: client, Config: config})
		result.Changed = rolledBack.Changed
		if err == nil {
			if restored, loadErr := integrationbundle.LoadInstalledConfig(root, bundle, client, nil); loadErr == nil {
				config = restored
				result.Capabilities = quickCapabilitiesFrom(config)
			}
		}
		result.Outcome = quickOutcomeRolledBack
	}
	if err != nil {
		result.Outcome = quickOutcomeFailed
		fmt.Fprintln(stderr, safeError(err))
		if *jsonOutput {
			_ = writeQuickResult(stdout, result, true)
		}
		return 1
	}
	if err = writeQuickResult(stdout, result, *jsonOutput); err != nil {
		return 1
	}
	return 0
}

func parseQuickClient(value string) (conformance.ClientFamily, error) {
	client := conformance.ClientFamily(value)
	if client != conformance.ClientCodex && client != conformance.ClientClaude {
		return "", fmt.Errorf("quick commands support only codex or claude")
	}
	return client, nil
}

func quickPreset(documents bool) integrationbundle.CapabilityConfig {
	documentState := integrationbundle.CapabilityDisabled
	if documents {
		documentState = integrationbundle.CapabilityAvailable
	}
	return integrationbundle.CapabilityConfig{Memory: integrationbundle.CapabilityAvailable, Documents: documentState, Todoist: integrationbundle.CapabilityDisabled}
}

func quickCapabilitiesFrom(config integrationbundle.CapabilityConfig) quickCapabilities {
	return quickCapabilities{Memory: string(config.Memory), Documents: string(config.Documents), Todoist: string(config.Todoist)}
}

func writeQuickResult(output io.Writer, result quickResult, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(true)
		return encoder.Encode(result)
	}
	_, err := fmt.Fprintf(output, "Outcome: %s\nClient: %s\nRoot: %s\nCapabilities: memory=%s, documents=%s, todoist=%s\nBundle: %s\n", result.Outcome, result.Client, result.Root, result.Capabilities.Memory, result.Capabilities.Documents, result.Capabilities.Todoist, result.BundleVersion)
	return err
}
