package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

func projectSources(t *testing.T) (string, string) {
	t.Helper()
	contract := filepath.Join("..", "..", "docs", "model-usage-contract.md")
	suite := filepath.Join("..", "..", "conformancedata", "public", "v1", "scenarios.json")
	if _, e := os.Stat(contract); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(suite); e != nil {
		t.Fatal(e)
	}
	return contract, suite
}
func TestCLIParsingAndDryRun(t *testing.T) {
	contract, suite := projectSources(t)
	root := t.TempDir()
	var out, errOut bytes.Buffer
	args := []string{"install", "--client", "codex", "--target-root", root, "--dry-run", "--contract-source", contract, "--suite-source", suite, "--capability", "memory=unavailable"}
	if code := run(args, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"status":"planned"`) || strings.Contains(out.String(), `"status":"installed"`) {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "codex")); !os.IsNotExist(err) {
		t.Fatal("dry run wrote files")
	}
}
func TestCLIStrictRequiredAndUnknown(t *testing.T) {
	for _, args := range [][]string{{"install"}, {"wat"}, {"verify", "--client", "codex", "--target-root", t.TempDir(), "extra"}} {
		var out, errOut bytes.Buffer
		if code := run(args, strings.NewReader(""), &out, &errOut); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}
func TestCLIRenderSummaryContainsNoContent(t *testing.T) {
	contract, suite := projectSources(t)
	root := t.TempDir()
	var out, errOut bytes.Buffer
	code := run([]string{"render", "--client", "codex", "--target-root", root, "--contract-source", contract, "--suite-source", suite}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatal(errOut.String())
	}
	if strings.Contains(out.String(), "canonical") || strings.Contains(out.String(), "policy") {
		t.Fatal("artifact content leaked")
	}
	if strings.Contains(out.String(), `"status":"installed"`) {
		t.Fatal("render falsely reported installed")
	}
}

func TestCLIRejectsInvalidFlagCombinations(t *testing.T) {
	contract, suite := projectSources(t)
	root := t.TempDir()
	cases := [][]string{
		{"install", "--client", "codex", "--target-root", root, "--tool", "recall_facts"},
		{"update", "--client", "codex", "--target-root", root, "--discovery-performed"},
		{"render", "--client", "codex", "--target-root", root, "--capability", "memory=available"},
		{"rollback", "--client", "codex", "--target-root", root, "--dry-run"},
		{"render", "--client", "codex", "--contract-source", contract, "--suite-source", suite},
	}
	for _, args := range cases {
		var out, errOut bytes.Buffer
		if code := run(args, strings.NewReader(""), &out, &errOut); code != 2 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, errOut.String())
		}
	}
}

func TestConformanceAdapterStrictStdinAndIdentity(t *testing.T) {
	contract, suitePath := projectSources(t)
	suiteSource, err := os.ReadFile(suitePath)
	if err != nil {
		t.Fatal(err)
	}
	suite, err := conformance.LoadSuite(bytes.NewReader(suiteSource))
	if err != nil {
		t.Fatal(err)
	}
	scenario := suite.Scenarios[0]
	request := adapterRequestForCLI("codex", suite.ContractVersion, scenario)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"conformance-adapter", "--contract-source", contract, "--suite-source", suitePath}
	var stdout, stderr bytes.Buffer
	if code := run(args, bytes.NewReader(encoded), &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if strings.Count(strings.TrimSpace(stdout.String()), "\n") != 0 {
		t.Fatalf("expected exactly one JSON value: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), scenario.SyntheticInput) {
		t.Fatalf("request content leaked: %s", stdout.String())
	}

	for name, input := range map[string][]byte{
		"unknown":  bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":1,"unknown":true`), 1),
		"trailing": append(append([]byte{}, encoded...), []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			stdout.Reset()
			stderr.Reset()
			if code := run(args, bytes.NewReader(input), &stdout, &stderr); code == 0 {
				t.Fatal("expected strict stdin rejection")
			}
			if stdout.Len() != 0 || strings.Contains(stderr.String(), scenario.SyntheticInput) {
				t.Fatalf("unsafe failure output stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestConformanceAdapterInputLimitAndContentFreeFlagErrors(t *testing.T) {
	contract, suitePath := projectSources(t)
	args := []string{"conformance-adapter", "--contract-source", contract, "--suite-source", suitePath}

	var stdout, stderr bytes.Buffer
	secretArg := "--secret-token=do-not-echo-this-value"
	if code := run(append(args, secretArg), strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), secretArg) || strings.Contains(stderr.String(), "do-not-echo") {
		t.Fatalf("flag error leaked attacker content: %q", stderr.String())
	}

	suiteSource, err := os.ReadFile(suitePath)
	if err != nil {
		t.Fatal(err)
	}
	suite, err := conformance.LoadSuite(bytes.NewReader(suiteSource))
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(adapterRequestForCLI("codex", suite.ContractVersion, suite.Scenarios[0]))
	if err != nil {
		t.Fatal(err)
	}
	const limit = 1 << 20
	for name, input := range map[string][]byte{
		"exact boundary":           append(append([]byte{}, request...), bytes.Repeat([]byte(" "), limit-len(request))...),
		"oversized trailing value": append(append(append([]byte{}, request...), bytes.Repeat([]byte(" "), limit+1)...), []byte("{}")...),
	} {
		t.Run(name, func(t *testing.T) {
			stdout.Reset()
			stderr.Reset()
			code := run(args, bytes.NewReader(input), &stdout, &stderr)
			if name == "exact boundary" && code != 0 {
				t.Fatalf("boundary rejected: code=%d stderr=%q", code, stderr.String())
			}
			if name != "exact boundary" && (code == 0 || stdout.Len() != 0) {
				t.Fatalf("oversized input accepted: code=%d stdout=%q", code, stdout.String())
			}
		})
	}
}

func adapterRequestForCLI(client, contractVersion string, scenario conformance.Scenario) map[string]any {
	return map[string]any{
		"schema_version": conformance.CurrentSchemaVersion, "contract_version": contractVersion, "client_family": client,
		"scenario_id": scenario.ID, "intent_class": scenario.IntentClass,
		"synthetic_input": scenario.SyntheticInput, "capabilities": scenario.Capabilities,
	}
}

func TestDiscoveryFileDecodingIsStrictAndBounded(t *testing.T) {
	valid := []byte(`{"performed":true,"tools":["recall_facts"]}`)
	got, err := decodeDiscovery(bytes.NewReader(valid))
	if err != nil || !got.Performed || len(got.Tools) != 1 {
		t.Fatalf("valid discovery: got=%+v err=%v", got, err)
	}
	for name, input := range map[string][]byte{
		"unknown field":  []byte(`{"performed":true,"tool":["recall_facts"]}`),
		"trailing value": append(append([]byte{}, valid...), []byte(` {}`)...),
		"oversized":      bytes.Repeat([]byte(" "), (1<<20)+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeDiscovery(bytes.NewReader(input)); err == nil {
				t.Fatal("expected strict discovery rejection")
			}
		})
	}
}

func TestCLITemporaryProfileLifecycleAndExports(t *testing.T) {
	contract, suite := projectSources(t)
	base := []string{"--contract-source", contract, "--suite-source", suite,
		"--capability", "memory=disabled", "--capability", "documents=disabled", "--capability", "todoist=disabled"}
	runOK := func(t *testing.T, args []string, wantStatus string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), `"status":"`+wantStatus+`"`) {
			t.Fatalf("args=%v stdout=%s", args, stdout.String())
		}
		return stdout.String()
	}
	for _, client := range []string{"codex", "claude"} {
		t.Run(client, func(t *testing.T) {
			root := t.TempDir()
			install := append([]string{"install", "--client", client, "--target-root", root}, base...)
			dryRun := append(append([]string{}, install...), "--dry-run")
			runOK(t, dryRun, "planned")
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
				t.Fatalf("dry run changed profile: entries=%v err=%v", entries, err)
			}
			runOK(t, install, "installed")
			runOK(t, append([]string{"verify", "--client", client, "--target-root", root}, base...), "installed")
			runOK(t, append([]string{"update", "--client", client, "--target-root", root}, base...), "installed")
			runOK(t, append([]string{"rollback", "--client", client, "--target-root", root}, base...), "installed")
		})
	}

	t.Run("chatgpt_manual", func(t *testing.T) {
		root := t.TempDir()
		runOK(t, append([]string{"install", "--client", "chatgpt", "--target-root", root}, base...), "manual_action_required")
		runOK(t, append([]string{"verify", "--client", "chatgpt", "--target-root", root}, base...), "manual_action_required")
	})
	t.Run("generic_render", func(t *testing.T) {
		root := t.TempDir()
		runOK(t, []string{"render", "--client", "generic_mcp", "--target-root", root, "--contract-source", contract, "--suite-source", suite}, "missing")
		for _, name := range []string{"generic-mcp/policy.json", "generic-mcp/tool-mapping.json"} {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
				t.Fatalf("rendered %s: %v", name, err)
			}
		}
	})
}
