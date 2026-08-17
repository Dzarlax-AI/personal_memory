package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuickInstallPresetsForCodexAndClaude(t *testing.T) {
	for _, client := range []string{"codex", "claude"} {
		for _, tc := range []struct {
			name          string
			flags         []string
			wantDocuments string
		}{
			{name: "memory_only", wantDocuments: "disabled"},
			{name: "with_documents", flags: []string{"--with-documents"}, wantDocuments: "available"},
		} {
			t.Run(client+"_"+tc.name, func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "."+client)
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
				args := append([]string{"quick-install", client, "--target-root", root, "--confirm-tools-discovered", "--json"}, tc.flags...)
				result := runQuickJSON(t, args)
				if result.Client != client || result.Root != root || result.BundleVersion != "0.1.0" || result.Outcome != quickOutcomeInstalled {
					t.Fatalf("unexpected result: %+v", result)
				}
				if result.Capabilities.Memory != "available" || result.Capabilities.Documents != tc.wantDocuments || result.Capabilities.Todoist != "disabled" {
					t.Fatalf("unexpected capabilities: %+v", result.Capabilities)
				}
			})
		}
	}
}

func TestQuickCommandsPreserveAndExplicitlyTransitionDocuments(t *testing.T) {
	for _, client := range []string{"codex", "claude"} {
		t.Run(client, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "."+client)
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}

			installed := runQuickJSON(t, []string{"quick-install", client, "--target-root", root, "--with-documents", "--confirm-tools-discovered", "--json"})
			if installed.Capabilities.Documents != "available" {
				t.Fatal(installed)
			}
			verified := runQuickJSON(t, []string{"quick-verify", client, "--target-root", root, "--confirm-tools-discovered", "--json"})
			if verified.Outcome != quickOutcomeVerified || verified.Capabilities.Documents != "available" {
				t.Fatalf("verify silently changed documents: %+v", verified)
			}
			updated := runQuickJSON(t, []string{"quick-update", client, "--target-root", root, "--confirm-tools-discovered", "--json"})
			if updated.Capabilities.Documents != "available" {
				t.Fatalf("update silently changed documents: %+v", updated)
			}
			memoryOnly := runQuickJSON(t, []string{"quick-update", client, "--target-root", root, "--memory-only", "--confirm-tools-discovered", "--json"})
			if memoryOnly.Capabilities.Documents != "disabled" {
				t.Fatalf("explicit transition failed: %+v", memoryOnly)
			}
			rolledBack := runQuickJSON(t, []string{"quick-rollback", client, "--target-root", root, "--json"})
			if rolledBack.Outcome != quickOutcomeRolledBack || rolledBack.Capabilities.Documents != "available" {
				t.Fatalf("rollback did not restore documents config: %+v", rolledBack)
			}
		})
	}
}

func TestQuickCommandsRefuseBeforeWriting(t *testing.T) {
	t.Run("missing root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), ".codex")
		var stdout, stderr bytes.Buffer
		if code := run([]string{"quick-install", "codex", "--target-root", root, "--confirm-tools-discovered", "--json"}, strings.NewReader(""), &stdout, &stderr); code == 0 {
			t.Fatal("missing root accepted")
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("missing root was created")
		}
	})

	t.Run("missing explicit root", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"quick-install", "codex", "--confirm-tools-discovered", "--json"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
			t.Fatalf("missing --target-root code=%d stderr=%q", code, stderr.String())
		}
	})

	t.Run("missing confirmation", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), ".claude")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := run([]string{"quick-install", "claude", "--target-root", root, "--json"}, strings.NewReader(""), &stdout, &stderr); code == 0 {
			t.Fatal("missing confirmation accepted")
		}
		if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
			t.Fatalf("refused command wrote files: entries=%v err=%v", entries, err)
		}
	})

	for _, args := range [][]string{
		{"quick-install", "chatgpt", "--confirm-tools-discovered"},
		{"quick-install", "codex", "--with-documents", "--memory-only", "--confirm-tools-discovered"},
		{"quick-rollback", "codex", "--with-documents"},
		{"quick-verify", "codex", "--unknown"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 2 {
			t.Fatalf("args=%v code=%d stderr=%q", args, code, stderr.String())
		}
	}
}

func TestQuickBinaryPathNeedsNoRepositorySources(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".codex")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	result := runQuickJSON(t, []string{"quick-install", "codex", "--target-root", root, "--confirm-tools-discovered", "--json"})
	if result.Outcome != quickOutcomeInstalled {
		t.Fatal(result)
	}
}

func TestQuickHumanAndJSONOutputAreContentFree(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".codex")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"quick-install", "codex", "--target-root", root, "--confirm-tools-discovered"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, forbidden := range []string{"BEGIN PERSONAL_MEMORY", "canonical policy", "api_key", "https://", "recall preference"} {
		if strings.Contains(strings.ToLower(stdout.String()), strings.ToLower(forbidden)) {
			t.Fatalf("human output leaked %q: %s", forbidden, stdout.String())
		}
	}
	if !strings.Contains(stdout.String(), "Outcome: installed") || !strings.Contains(stdout.String(), "Client: codex") {
		t.Fatalf("unexpected human output: %s", stdout.String())
	}
}

func runQuickJSON(t *testing.T, args []string) quickResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("args=%v code=%d stderr=%q stdout=%q", args, code, stderr.String(), stdout.String())
	}
	var result quickResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v output=%q", err, stdout.String())
	}
	return result
}
