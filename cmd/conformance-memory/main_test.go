package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

func TestRunCLIFixtureProducesReports(t *testing.T) {
	temp := t.TempDir()
	jsonPath := filepath.Join(temp, "report.json")
	markdownPath := filepath.Join(temp, "report.md")
	var stdout, stderr bytes.Buffer
	err := runCLI([]string{
		"run", "--source", "fixture",
		"--suite", filepath.Join("..", "..", "conformancedata", "public", "v1", "scenarios.json"),
		"--contract", filepath.Join("..", "..", "docs", "model-usage-contract.md"),
		"--traces", filepath.Join("..", "..", "conformancedata", "public", "v1", "traces", "passing.json"),
		"--json", jsonPath, "--markdown", markdownPath,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runCLI() error = %v; stderr = %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "evaluated 32 client-scenarios; gates_passed=true") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	report, err := conformance.DecodeReport(data)
	if err != nil {
		t.Fatal(err)
	}
	if !report.GatesPassed || report.Aggregate.Pass != 32 {
		t.Fatalf("report = %#v", report)
	}
	if markdown, err := os.ReadFile(markdownPath); err != nil || !bytes.Contains(markdown, []byte("Conformance Report")) {
		t.Fatalf("Markdown report error = %v; content = %q", err, markdown)
	}
}

func TestRunCLIRejectsUnsafeFlagCombinations(t *testing.T) {
	base := []string{
		"run", "--suite", "suite.json", "--contract", "contract.md",
		"--json", "report.json", "--markdown", "report.md",
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"fixture missing traces", base, "--traces is required"},
		{"live missing adapter", append(append([]string{}, base...), "--source", "live"), "--client-family"},
		{"same outputs", append(append([]string{}, base...), "--traces", "traces.json", "--markdown", "report.json"), "must be different"},
		{"fixture live flag", append(append([]string{}, base...), "--traces", "traces.json", "--adapter-exec", "/bin/false"), "live adapter flags"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runCLI(tt.args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("runCLI() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestSelectedEnvironmentIsExplicitAndValidated(t *testing.T) {
	t.Setenv("CONFORMANCE_TEST_TOKEN", "synthetic-secret")
	got, err := selectedEnvironment([]string{"CONFORMANCE_TEST_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "CONFORMANCE_TEST_TOKEN=synthetic-secret" {
		t.Fatalf("environment = %#v", got)
	}
	if _, err := selectedEnvironment([]string{"invalid-name"}); err == nil {
		t.Fatal("selectedEnvironment() accepted invalid name")
	}
	if _, err := selectedEnvironment([]string{"MISSING_CONFORMANCE_ENV"}); err == nil {
		t.Fatal("selectedEnvironment() accepted missing variable")
	}
}

func TestEnsureDistinctPathsDetectsExistingAliases(t *testing.T) {
	temp := t.TempDir()
	original := filepath.Join(temp, "original.json")
	alias := filepath.Join(temp, "alias.json")
	if err := os.WriteFile(original, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, alias); err != nil {
		t.Fatal(err)
	}
	if err := ensureDistinctPaths([]string{original, alias}); err == nil {
		t.Fatal("ensureDistinctPaths() accepted hard-linked paths")
	}
}

func TestGateFailureErrorIsTyped(t *testing.T) {
	err := error(&gateFailureError{aggregate: conformance.Aggregate{Fail: 1}})
	var gateErr *gateFailureError
	if !errors.As(err, &gateErr) || !strings.Contains(err.Error(), "fail=1") {
		t.Fatalf("gate error = %v", err)
	}
}
