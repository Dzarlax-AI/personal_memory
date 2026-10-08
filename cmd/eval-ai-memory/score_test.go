package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
)

func replayFixture(t *testing.T) (Corpus, Manifest, []byte) {
	t.Helper()
	raw, err := os.ReadFile("../../evaldata/experiments/optional-ai-memory-v1/synthetic.json")
	if err != nil {
		t.Fatal(err)
	}
	var c Corpus
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	m, err := preview(c, raw)
	if err != nil {
		t.Fatal(err)
	}
	return c, m, raw
}

func completeResultRows(c Corpus, m Manifest) []ResultRow {
	writes := map[string]WriteCase{}
	for _, w := range c.Writes {
		writes[w.ID] = w
	}
	reads := map[string]ReadCase{}
	for _, r := range c.Reads {
		reads[r.ID] = r
	}
	out := make([]ResultRow, 0, len(m.Requests))
	for _, req := range m.Requests {
		row := ResultRow{RequestSHA256: req.SHA256, Task: req.Task, CaseID: req.CaseID, Provider: req.Provider, Arm: req.Arm, Status: "decided"}
		if req.Task == "write" {
			w := writes[req.CaseID]
			if w.Expected == "insufficient_context" {
				row.Status = "abstained"
			} else {
				row.PrimaryTag = w.Expected
			}
		} else {
			r := reads[req.CaseID]
			row.Scores = map[string]float64{}
			expected := map[string]bool{}
			for _, a := range r.Expected {
				expected[a] = true
			}
			for _, candidate := range r.Input.Candidates {
				if expected[candidate.Alias] {
					row.Scores[candidate.Alias] = 0.9
				} else {
					row.Scores[candidate.Alias] = 0.1
				}
			}
			none := len(r.Expected) == 0
			row.NoneRelevant = &none
		}
		out = append(out, row)
	}
	return out
}

func getArm(t *testing.T, r ReplayReport, provider, arm string) ArmReport {
	t.Helper()
	for _, g := range r.Arms {
		if g.Provider == provider && g.Arm == arm {
			return g
		}
	}
	t.Fatalf("missing arm %s/%s", provider, arm)
	return ArmReport{}
}

func TestOfflineReplayScoresPairedArmsAndNoRelevant(t *testing.T) {
	c, m, _ := replayFixture(t)
	results := ResultManifest{SchemaVersion: 1, CorpusSHA256: m.CorpusSHA256, Rows: completeResultRows(c, m)}
	rawResults, _ := json.Marshal(results)
	report, err := scoreReplay(c, m, results, rawResults)
	if err != nil {
		t.Fatal(err)
	}
	if report.ExpectedRows != 84 || report.ReceivedRows != 84 || report.MissingRows != 0 || report.QualityClaims || report.QualityVerdict != "not_evaluated" || !report.SyntheticLabels {
		t.Fatalf("report binding/counts: %#v", report)
	}
	write := getArm(t, report, "decisions", "both").Write
	if write == nil || write.Cases != 8 || write.Correct != 8 || write.Accuracy != 1 || write.Abstained != 1 || write.Invalid != 0 {
		t.Fatalf("write metrics: %#v", write)
	}
	read := getArm(t, report, "jev", "catalog").Read
	if read == nil || read.Cases != 5 || read.RelevantCases != 4 || read.NoRelevantCases != 1 || read.NoRelevantCorrect != 1 || read.NoRelevantAccuracy != 1 || read.MRR != 0.8 || read.BaselineMRR != 0.8 || read.DeltaMRR != 0 {
		t.Fatalf("read metrics: %#v", read)
	}
}

func TestMissingAndInvalidRowsStayInDenominator(t *testing.T) {
	c, m, _ := replayFixture(t)
	rows := completeResultRows(c, m)
	filtered := rows[:0]
	for _, row := range rows {
		if row.Provider == "decisions" && row.Arm == "text" && row.Task == "write" && row.CaseID == "w1" {
			continue
		}
		if row.Provider == "jev" && row.Arm == "both" && row.Task == "write" && row.CaseID == "w2" {
			row.Status = "invalid"
			row.PrimaryTag = ""
		}
		if row.Provider == "decisions" && row.Arm == "catalog" && row.Task == "write" && row.CaseID == "w1" {
			row.Status = "abstained"
			row.PrimaryTag = ""
		}
		filtered = append(filtered, row)
	}
	results := ResultManifest{SchemaVersion: 1, CorpusSHA256: m.CorpusSHA256, Rows: filtered}
	encoded, _ := json.Marshal(results)
	report, err := scoreReplay(c, m, results, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if report.MissingRows != 1 || report.ReceivedRows != 83 {
		t.Fatalf("row accounting: %#v", report)
	}
	missing := getArm(t, report, "decisions", "text").Write
	if missing.Cases != 8 || missing.Missing != 1 || missing.Invalid != 1 || missing.Accuracy != 0.875 {
		t.Fatalf("missing row denominator: %#v", missing)
	}
	invalid := getArm(t, report, "jev", "both").Write
	if invalid.Cases != 8 || invalid.Invalid != 1 || invalid.Accuracy != 0.875 {
		t.Fatalf("invalid row denominator: %#v", invalid)
	}
	abstained := getArm(t, report, "decisions", "catalog").Write
	if abstained.Abstained != 2 || abstained.Accuracy != 0.875 {
		t.Fatalf("abstention metrics: %#v", abstained)
	}
}

func TestReadScoringPreservesAuthorityAndRejectsAliasSetMismatch(t *testing.T) {
	c, m, _ := replayFixture(t)
	for i := range c.Reads {
		if c.Reads[i].ID == "r4" {
			c.Reads[i].Expected = []string{"c1"}
		}
	}
	rows := completeResultRows(c, m)
	for i := range rows {
		r := &rows[i]
		if r.Task == "read" && r.CaseID == "r4" {
			r.Scores["c1"] = 0.1
			r.Scores["c2"] = 0.99
		}
		if r.Task == "read" && r.CaseID == "r1" && r.Provider == "decisions" && r.Arm == "text" {
			delete(r.Scores, "c2")
			r.Scores["unknown"] = 0.5
		}
	}
	results := ResultManifest{SchemaVersion: 1, CorpusSHA256: m.CorpusSHA256, Rows: rows}
	encoded, _ := json.Marshal(results)
	report, err := scoreReplay(c, m, results, encoded)
	if err != nil {
		t.Fatal(err)
	}
	read := getArm(t, report, "jev", "catalog").Read
	if read.MRR != 0.7 || read.BaselineMRR != 0.7 || read.DeltaMRR != 0 {
		t.Fatalf("scores crossed authority buckets: %#v", read)
	}
	bad := getArm(t, report, "decisions", "text").Read
	if bad.Invalid != 1 || bad.Cases != 5 || bad.MRR != 0.5 {
		t.Fatalf("unknown alias did not count invalid: %#v", bad)
	}
}

func TestRankComparatorUsesRuntimeAuthorityOrderOnInterleavedCandidates(t *testing.T) {
	candidates := []aijudgment.Candidate{
		{Alias: "history", AuthorityBucket: "historical"},
		{Alias: "other-a", AuthorityBucket: "other_current"},
		{Alias: "disputed", AuthorityBucket: "disputed"},
		{Alias: "canonical", AuthorityBucket: "canonical_current"},
		{Alias: "other-b", AuthorityBucket: "other_current"},
		{Alias: "superseded", AuthorityBucket: "superseded"},
	}
	baseline := rankWithinAuthority(candidates, nil)
	wantBaseline := []string{"canonical", "other-a", "other-b", "disputed", "history", "superseded"}
	if !reflect.DeepEqual(baseline, wantBaseline) {
		t.Fatalf("baseline order=%v want=%v", baseline, wantBaseline)
	}
	scored := rankWithinAuthority(candidates, map[string]float64{"superseded": 1, "history": 1, "disputed": 1, "canonical": 1, "other-a": 0.1, "other-b": 0.9})
	wantScored := []string{"canonical", "other-b", "other-a", "disputed", "history", "superseded"}
	if !reflect.DeepEqual(scored, wantScored) {
		t.Fatalf("scored order=%v want=%v", scored, wantScored)
	}
}

func TestResultManifestRejectsBindingAndIdentityErrors(t *testing.T) {
	c, m, _ := replayFixture(t)
	rows := completeResultRows(c, m)
	base := ResultManifest{SchemaVersion: 1, CorpusSHA256: m.CorpusSHA256, Rows: rows}
	encoded, _ := json.Marshal(base)
	badCorpus := base
	badCorpus.CorpusSHA256 = "bad"
	if _, err := scoreReplay(c, m, badCorpus, encoded); err == nil {
		t.Fatal("corpus mismatch accepted")
	}
	badHash := base
	badHash.Rows = append([]ResultRow(nil), rows...)
	badHash.Rows[0].RequestSHA256 = "bad"
	if _, err := scoreReplay(c, m, badHash, encoded); err == nil {
		t.Fatal("request hash mismatch accepted")
	}
	duplicate := base
	duplicate.Rows = append(append([]ResultRow(nil), rows...), rows[0])
	if _, err := scoreReplay(c, m, duplicate, encoded); err == nil {
		t.Fatal("duplicate row accepted")
	}
	unknown := base
	unknown.Rows = append([]ResultRow(nil), rows...)
	unknown.Rows[0].CaseID = "unknown"
	if _, err := scoreReplay(c, m, unknown, encoded); err == nil {
		t.Fatal("unknown row accepted")
	}
	invalidScore := base
	invalidScore.Rows = append([]ResultRow(nil), rows...)
	for i := range invalidScore.Rows {
		if invalidScore.Rows[i].Task == "read" {
			invalidScore.Rows[i].Scores["c1"] = 0.5
			invalidScore.Rows[i].Scores["unknown"] = 0.5
			break
		}
	}
	if r, err := scoreReplay(c, m, invalidScore, encoded); err != nil || getArm(t, r, "decisions", "text").Read.Invalid != 1 {
		t.Fatalf("bad alias row should stay in invalid denominator: %#v %v", r, err)
	}
}

func TestRunWritesPrivateReplayReport(t *testing.T) {
	c, m, corpusRaw := replayFixture(t)
	results := ResultManifest{SchemaVersion: 1, CorpusSHA256: m.CorpusSHA256, Rows: completeResultRows(c, m)}
	tmp, err := os.MkdirTemp("/private/tmp", "eval-ai-memory-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	corpusPath := filepath.Join(tmp, "corpus.json")
	resultsPath := filepath.Join(tmp, "results.json")
	dir := filepath.Join(tmp, "out")
	if err := os.WriteFile(corpusPath, corpusRaw, 0600); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(results)
	if err := os.WriteFile(resultsPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--corpus", corpusPath, "--dir", dir, "--results", resultsPath}, io.Discard); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("report mode=%o", info.Mode().Perm())
	}
}
