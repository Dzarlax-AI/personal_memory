package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	memoryeval "github.com/Dzarlax-AI/personal-memory/internal/eval"
)

func TestCLIRejectsUnknownSubcommand(t *testing.T) {
	err := runCLI([]string{"unknown"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunCLIRejectsSameReportPath(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report")
	err := runCLI([]string{
		"run",
		"--dataset", filepath.Join(dir, "missing.json"),
		"--json", reportPath,
		"--markdown", filepath.Join(dir, ".", "report"),
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "different files") {
		t.Fatalf("error = %v, want same output path rejection", err)
	}
}

func TestRunCLIForbidsFixtureInputProfileRelabel(t *testing.T) {
	dir := t.TempDir()
	err := runCLI([]string{
		"run", "--source", "fixture",
		"--dataset", filepath.Join("..", "..", "evaldata", "public", "v1", "dataset.json"),
		"--json", filepath.Join(dir, "report.json"),
		"--markdown", filepath.Join(dir, "report.md"),
		"--input-profile", "multilingual-e5-v1",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot relabel") {
		t.Fatalf("error = %v, want fixture relabel rejection", err)
	}
}

func TestRunCLITEIFixtureRequiresV3BeforeExternalWork(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EMBED_URL", "http://127.0.0.1:1")
	err := runCLI([]string{
		"run", "--source", "tei-fixture",
		"--dataset", filepath.Join("..", "..", "evaldata", "public", "v1", "dataset.json"),
		"--json", filepath.Join(dir, "report.json"),
		"--markdown", filepath.Join(dir, "report.md"),
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "schema_version 3") ||
		strings.Contains(err.Error(), "TEI") {
		t.Fatalf("error = %v, want preflight schema rejection", err)
	}
}

func TestRunCLIFixtureIgnoresAmbientEmbedURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EMBED_URL", "http://127.0.0.1:1")
	err := runCLI([]string{
		"run", "--source", "fixture",
		"--dataset", filepath.Join("..", "..", "evaldata", "public", "v1", "dataset.json"),
		"--qdrant-url", "http://127.0.0.1:1",
		"--json", filepath.Join(dir, "report.json"),
		"--markdown", filepath.Join(dir, "report.md"),
		"--timeout", "10ms",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected unavailable Qdrant error")
	}
	if strings.Contains(err.Error(), "TEI") || strings.Contains(err.Error(), "embed") {
		t.Fatalf("fixture consumed ambient EMBED_URL: %v", err)
	}
}

func TestCompareCLIProducesDeterministicOutput(t *testing.T) {
	dir := t.TempDir()
	report := memoryeval.Report{
		SchemaVersion: 1, DatasetVersion: "1.0.0", Mode: "fixture",
		Embedding:     memoryeval.EmbeddingIdentity{Provider: "synthetic", ModelID: "m", ModelRevision: "r", DType: "float32", Pooling: "mean", VectorSize: 2},
		Configuration: memoryeval.Configuration{Name: "cfg"},
		TopK:          []int{1}, Aggregate: memoryeval.AggregateMetrics{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 1}, MRR: 1},
		Queries: []memoryeval.QueryReport{{
			ID: "q", Target: "facts", Mode: "flat",
			Metrics: memoryeval.QueryMetrics{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 1}, MRR: 1},
		}},
		GatesPassed: true,
	}
	data, err := memoryeval.RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(dir, "baseline.json")
	candidate := filepath.Join(dir, "candidate.json")
	if err := os.WriteFile(baseline, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var first, second bytes.Buffer
	args := []string{"compare", "--baseline", baseline, "--candidate", candidate, "--enforce-gates"}
	if err := runCLI(args, &first, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := runCLI(args, &second, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("comparison output is not deterministic")
	}
}

func TestWriteAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "report.json")
	if err := writeAtomic(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("content = %q", data)
	}
}
