package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
)

func TestCLIValidateImportPublishExport(t *testing.T) {
	root := canonicalTempDir(t)
	input := filepath.Join(root, "input.json")
	s := contextcatalog.Snapshot{SchemaVersion: 1, Version: "1.0", CreatedAt: "2026-10-07T10:00:00Z", Entries: []contextcatalog.Entry{{Namespace: "projects", Tag: "health", Name: "Health", Summary: "Health app context", ReviewStatus: "approved"}}}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(input, b, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if err := run([]string{"validate", "-file", input}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "valid entries=1") {
		t.Fatalf("unexpected validation output %q", out.String())
	}
	staged := filepath.Join(root, "staged.json")
	if err := run([]string{"import", "-file", input, "-output", staged}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"import", "-file", input, "-output", staged}, &out, &stderr); err == nil {
		t.Fatal("import overwrote existing file")
	}
	dir := filepath.Join(root, "catalog")
	if err := run([]string{"publish", "-dir", dir, "-file", staged, "-expected-hash", ""}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"export", "-dir", dir}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"catalog_version":"1.0"`) {
		t.Fatalf("unexpected export %s", out.String())
	}
}

func TestCLIPublishConflictIsReported(t *testing.T) {
	root := canonicalTempDir(t)
	input := filepath.Join(root, "input.json")
	s := contextcatalog.Snapshot{SchemaVersion: 1, Version: "1.0", CreatedAt: "2026-10-07T10:00:00Z"}
	b, _ := json.Marshal(s)
	if err := os.WriteFile(input, b, 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "state")
	var out, stderr bytes.Buffer
	if err := run([]string{"publish", "-dir", dir, "-file", input}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"publish", "-dir", dir, "-file", input}, &out, &stderr); err == nil || !strings.Contains(err.Error(), "active catalog changed") {
		t.Fatalf("expected clear conflict, got %v", err)
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}
