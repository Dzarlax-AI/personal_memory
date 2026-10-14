package aimaintenance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
)

func validReviewTestRun() string {
	return "20261014T120000000000000Z-0123456789abcdef0123456789abcdef-00000001"
}
func reviewTempDir(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func validReviewTestRequest() ReviewAssessmentRequest {
	return ReviewAssessmentRequest{ExpectedRevision: 0, Verdict: "acceptable", Reason: "none", Note: "looks good", BaseEntryHash: strings.Repeat("a", 64), ProposedEntryHash: strings.Repeat("b", 64), CurrentEntryHash: strings.Repeat("c", 64)}
}

func TestReviewAssessmentRevisionConflictIdempotencyAndReopen(t *testing.T) {
	cfg := aipolicy.Config{StateDir: reviewTempDir(t)}
	run, digest, id := validReviewTestRun(), strings.Repeat("d", 64), strings.Repeat("e", 64)
	req := validReviewTestRequest()
	first, conflict, err := SaveReviewAssessment(cfg, run, digest, id, req)
	if err != nil || conflict || first.Revision != 1 {
		t.Fatalf("first save: %+v %v %v", first, conflict, err)
	}
	retry, conflict, err := SaveReviewAssessment(cfg, run, digest, id, req)
	if err != nil || conflict || retry.Revision != 1 {
		t.Fatalf("exact retry: %+v %v %v", retry, conflict, err)
	}
	stale := req
	stale.Note = "changed stale"
	_, conflict, err = SaveReviewAssessment(cfg, run, digest, id, stale)
	if err != nil || !conflict {
		t.Fatalf("stale request must conflict: %v %v", conflict, err)
	}
	next := req
	next.ExpectedRevision = 1
	next.Verdict = "needs_correction"
	saved, conflict, err := SaveReviewAssessment(cfg, run, digest, id, next)
	if err != nil || conflict || saved.Revision != 2 {
		t.Fatalf("second revision: %+v %v %v", saved, conflict, err)
	}
	opened, err := ReadReviewAssessments(cfg, run, digest)
	if err != nil || len(opened) != 1 || opened[0].Revision != 2 || opened[0].Request.Verdict != "needs_correction" {
		t.Fatalf("reopen: %+v %v", opened, err)
	}
}

func TestReviewAssessmentConcurrentInitialSavesHaveOneWinner(t *testing.T) {
	cfg := aipolicy.Config{StateDir: reviewTempDir(t)}
	run, digest, id := validReviewTestRun(), strings.Repeat("d", 64), strings.Repeat("e", 64)
	req := validReviewTestRequest()
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners, conflicts := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := req
			candidate.Note = fmt.Sprintf("candidate %02d", i)
			_, c, e := SaveReviewAssessment(cfg, run, digest, id, candidate)
			mu.Lock()
			defer mu.Unlock()
			if e != nil {
				t.Errorf("save: %v", e)
			} else if c {
				conflicts++
			} else {
				winners++
			}
		}(i)
	}
	wg.Wait()
	if winners != 1 || conflicts != 11 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
}

func TestReviewAssessmentSymlinkAndCorruptionAreVisible(t *testing.T) {
	base := t.TempDir()
	target := t.TempDir()
	state := filepath.Join(base, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(state, "maintenance-reviews")); err != nil {
		t.Fatal(err)
	}
	cfg := aipolicy.Config{StateDir: state}
	_, _, err := SaveReviewAssessment(cfg, validReviewTestRun(), strings.Repeat("d", 64), strings.Repeat("e", 64), validReviewTestRequest())
	if err == nil {
		t.Fatal("accepted symlinked store root")
	}
	cfg.StateDir = reviewTempDir(t)
	run, digest, id := validReviewTestRun(), strings.Repeat("d", 64), strings.Repeat("e", 64)
	_, _, err = SaveReviewAssessment(cfg, run, digest, id, validReviewTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	head := filepath.Join(cfg.StateDir, "maintenance-reviews", run, digest, id, "head.json")
	if err := os.WriteFile(head, []byte(`{"schema_version":1,"revision":999}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReviewAssessments(cfg, run, digest); err == nil {
		t.Fatal("corrupt head was hidden")
	}
}

func TestReviewAssessmentOrphanRevisionCannotBeOverwritten(t *testing.T) {
	cfg := aipolicy.Config{StateDir: reviewTempDir(t)}
	run, digest, id := validReviewTestRun(), strings.Repeat("d", 64), strings.Repeat("e", 64)
	dir := filepath.Join(cfg.StateDir, "maintenance-reviews", run, digest, id)
	if err := aipolicy.SecureDir(dir); err != nil {
		t.Fatal(err)
	}
	other := validReviewTestRequest()
	other.Note = "different orphan"
	record := ReviewAssessment{SchemaVersion: reviewSchema, Revision: 1, RunID: run, SHA256: digest, ProjectID: id, Request: other, SavedAt: "2026-10-14T12:00:00Z"}
	raw, _ := json.Marshal(record)
	if err := writeReviewImmutable(dir, "revision-00000001.json", raw); err != nil {
		t.Fatal(err)
	}
	incoming := validReviewTestRequest()
	_, conflict, err := SaveReviewAssessment(cfg, run, digest, id, incoming)
	if err != nil || !conflict {
		t.Fatalf("orphan must conflict: %v %v", conflict, err)
	}
	visible, err := ReadReviewAssessments(cfg, run, digest)
	if err != nil || len(visible) != 1 || visible[0].Revision != 1 || visible[0].Request.Note != "different orphan" {
		t.Fatalf("orphan was not adopted visibly: %+v %v", visible, err)
	}
	orphanBefore, err := os.ReadFile(filepath.Join(dir, "revision-00000001.json"))
	if err != nil {
		t.Fatal(err)
	}
	next := incoming
	next.ExpectedRevision = 1
	next.Note = "explicit follow-up"
	saved, conflict, err := SaveReviewAssessment(cfg, run, digest, id, next)
	if err != nil || conflict || saved.Revision != 2 {
		t.Fatalf("follow-up after adopting orphan: %+v %v %v", saved, conflict, err)
	}
	orphanAfter, err := os.ReadFile(filepath.Join(dir, "revision-00000001.json"))
	if err != nil || string(orphanBefore) != string(orphanAfter) {
		t.Fatalf("immutable orphan was overwritten: %v", err)
	}
}
