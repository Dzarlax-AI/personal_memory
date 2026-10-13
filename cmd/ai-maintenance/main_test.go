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
