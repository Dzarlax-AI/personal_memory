package main

import (
	"testing"
	"time"
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

func TestParseAnalyzeOptionsRejectsUnsafeInputs(t *testing.T) {
	now := time.Now()
	for _, args := range [][]string{{}, {"--output", "x", "--stale-days", "0"}, {"--output", "x", "--reference-time", "tomorrow"}, {"--output", "x", "extra"}} {
		if _, err := parseAnalyzeOptions(args, now); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}
