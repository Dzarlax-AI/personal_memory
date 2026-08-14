package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/maintenance"
)

func TestParseAnalyzeOptions(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	got, err := parseAnalyzeOptions([]string{"--output", "report.json", "--reference-time", "2026-08-01T00:00:00Z"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.output != "report.json" || got.referenceTime.Format(time.RFC3339) != "2026-08-01T00:00:00Z" {
		t.Fatalf("options = %#v", got)
	}
}

func TestParseMutationOptionsRequiresExplicitBoundedInputs(t *testing.T) {
	valid := []string{"--qdrant-url", "http://127.0.0.1:6333", "--collection", "memory", "--manifest", "report.json", "--journal", "result.json", "--point-id", "42", "--confirm-server-stopped"}
	got, err := parseMutationOptions("quarantine", valid)
	if err != nil || got.collection != "memory" || len(got.pointIDs) != 1 {
		t.Fatalf("options=%#v err=%v", got, err)
	}
	for _, args := range [][]string{
		{},
		{"--qdrant-url", "u", "--collection", "c", "--manifest", "m", "--journal", "j"},
		{"--qdrant-url", "u", "--collection", "c", "--manifest", "m", "--journal", "j", "--point-id", "42"},
		append(append([]string{}, valid...), "extra"),
		{"--qdrant-url", "u", "--collection", "c", "--manifest", "m", "--journal", "j", "--point-id", " "},
	} {
		if _, err := parseMutationOptions("quarantine", args); err == nil {
			t.Fatalf("expected rejection for %v", args)
		}
	}
	if _, err := parseMutationOptions("restore", append(valid, "--eligible")); err == nil {
		t.Fatal("restore accepted quarantine-only eligible selection")
	}
}

func TestRunMutationRejectsInvalidManifestWithoutLeakingContents(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	const private = "PRIVATE_FACT_TEXT_MUST_NOT_LEAK"
	if err := os.WriteFile(manifest, []byte(`{"unknown":"`+private+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	err := run(context.Background(), []string{"quarantine", "--qdrant-url", "http://127.0.0.1:6333", "--collection", "memory", "--manifest", manifest, "--journal", filepath.Join(dir, "journal.json"), "--point-id", "1", "--confirm-server-stopped"}, &stdout, time.Now)
	if err == nil || strings.Contains(err.Error(), private) || stdout.Len() != 0 {
		t.Fatalf("err=%v stdout=%q", err, stdout.String())
	}
}

func TestReadManifestRejectsTrailingValueAndUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(`{} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.ReadManifest(path); err == nil {
		t.Fatal("accepted trailing JSON value")
	}
}

func TestWriteMutationSummaryFailsClosedOnUnresolvedOutcomes(t *testing.T) {
	for _, status := range []maintenance.OutcomeStatus{maintenance.OutcomeFailed, maintenance.OutcomeAmbiguous} {
		var output strings.Builder
		result := maintenance.Result{BatchID: "batch", Outcomes: []maintenance.PointOutcome{{PointID: "1", Status: status}}}
		if err := writeMutationSummary(&output, "quarantine", result); err == nil {
			t.Fatalf("status %q returned success: %s", status, output.String())
		}
		if !strings.Contains(output.String(), string(status)+"=1") {
			t.Fatalf("status %q missing from summary: %s", status, output.String())
		}
	}
}

func TestParseAnalyzeOptionsRejectsUnsafeInputs(t *testing.T) {
	now := time.Now()
	for _, args := range [][]string{{}, {"--output", "x", "--stale-days", "0"}, {"--output", "x", "--reference-time", "tomorrow"}, {"--output", "x", "extra"}} {
		if _, err := parseAnalyzeOptions(args, now); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}
