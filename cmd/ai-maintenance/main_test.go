package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunStatusReportsConfiguredDisabledCatalogScopeWithoutEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "optional-ai.json")
	config := `{"schema_version":1,"catalog_dir":"/private/catalog","state_dir":"/private/state","maintenance":{"enabled":false,"scope":"catalog_only","profile":"maintenance"},"write":{"mode":"off","provider":"none"},"read":{"mode":"off","provider":"none"},"profiles":{"maintenance":{"protocol":"openai-responses","model":"operator-chosen-model","endpoint":"https://provider.example.invalid/v1/responses","key_file":"/private/key"}},"egress":{"allowed_namespaces":[],"allowed_project_tags":[],"unassigned_namespaces":[]}}`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runStatus([]string{"--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{`"enabled":false`, `"scope":"catalog_only"`, `"model":"operator-chosen-model"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("status missing %s: %s", want, got)
		}
	}
	for _, forbidden := range []string{"provider.example.invalid", "/private/", "key_file", "endpoint"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("status exposed %q: %s", forbidden, got)
		}
	}
}

func TestRunStatusRequiresConfigAndDoesNotExposeParseDetails(t *testing.T) {
	var out bytes.Buffer
	if err := runStatus(nil, &out); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("expected generic missing-config error, got %v", err)
	}
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"secret":"private marker"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runStatus([]string{"--config", path}, &out); err == nil || strings.Contains(err.Error(), "private marker") {
		t.Fatalf("expected generic config error, got %v", err)
	}
}

func TestRunHistoryMissingAndExactMissingAreExplicitAndReadOnly(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	path := filepath.Join(root, "ai.json")
	config := `{"schema_version":1,"catalog_dir":"/private/catalog","state_dir":"` + state + `","maintenance":{"enabled":false,"scope":"catalog_only","profile":"maintenance"},"write":{"mode":"off","provider":"none"},"read":{"mode":"off","provider":"none"},"profiles":{"maintenance":{"protocol":"openai-responses","model":"operator-model","endpoint":"https://provider.invalid/v1/responses","key_file":"/private/key"}},"egress":{"allowed_namespaces":[],"allowed_project_tags":[],"unassigned_namespaces":[]}}`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runHistory([]string{"--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"availability":"missing"`) || strings.Contains(out.String(), "provider.invalid") {
		t.Fatalf("unexpected missing history response: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(state, "maintenance-runs")); !os.IsNotExist(err) {
		t.Fatalf("read-only history created a directory: %v", err)
	}
	out.Reset()
	id := "20261013T120000000000000Z-0123456789abcdef0123456789abcdef-00000001"
	if err := runHistory([]string{"--config", path, "--run-id", id}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"history_status":"missing_run"`) || !strings.Contains(out.String(), id) {
		t.Fatalf("exact missing run was hidden: %s", out.String())
	}
	if err := runHistory([]string{"--config", path, "--run-id", id, "--cursor", id}, &out); err == nil {
		t.Fatal("accepted conflicting exact ID and cursor")
	}
}
