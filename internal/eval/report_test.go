package eval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderReportIsDeterministic(t *testing.T) {
	report := Report{
		SchemaVersion:  1,
		DatasetVersion: "1.0.0",
		Mode:           "fixture",
		TopK:           []int{3, 1},
		Queries: []QueryReport{
			{ID: "z", Metrics: QueryMetrics{HitAt: map[int]float64{3: 1, 1: 0}}},
			{ID: "a", Metrics: QueryMetrics{HitAt: map[int]float64{3: 1, 1: 1}}},
		},
	}
	firstJSON, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("JSON render is not deterministic")
	}
	firstMarkdown := RenderMarkdown(report)
	secondMarkdown := RenderMarkdown(report)
	if firstMarkdown != secondMarkdown {
		t.Fatal("Markdown render is not deterministic")
	}
	if bytes.Index(firstJSON, []byte(`"id": "a"`)) > bytes.Index(firstJSON, []byte(`"id": "z"`)) {
		t.Fatal("queries are not sorted by ID")
	}
}

func TestDecodeReportRejectsTrailingJSON(t *testing.T) {
	report := Report{SchemaVersion: SchemaVersion, DatasetVersion: "1.0.0"}
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecodeReport(append(data, []byte(`{}`)...))
	if err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("DecodeReport() error = %v, want trailing JSON error", err)
	}
}

func TestRenderAndDecodeV2ReportAreDeterministic(t *testing.T) {
	report := Report{
		SchemaVersion: CurrentReportSchemaVersion, DatasetVersion: "2.0.0",
		TopK: []int{1},
		Queries: []QueryReport{{
			ID: "q", Target: "facts", Results: []RetrievedItem{},
			Lifecycle: &QueryLifecycleReport{
				Intent: QueryIntentCurrent,
				Candidates: []LifecycleCandidateReport{
					{ID: "99", Valid: false, Decision: PresentationSuppress, ReasonCodes: []LifecycleReasonCode{ReasonInvalidLifecycle}},
					{ID: "1", State: "current", Valid: true, Decision: PresentationInclude, ReasonCodes: []LifecycleReasonCode{ReasonCurrentTruth}},
				},
			},
		}},
		Lifecycle: &LifecycleReport{
			Aggregate: LifecycleAggregateMetrics{Checks: 2},
			Transitions: []TransitionReport{
				{ID: "z", Valid: true, ReasonCode: ReasonTransitionValid, Passed: true},
				{ID: "a", Valid: false, ReasonCode: ReasonTargetInvalid, Passed: true},
			},
		},
	}
	first, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || RenderMarkdown(report) != RenderMarkdown(report) {
		t.Fatal("v2 rendering is not deterministic")
	}
	decoded, err := DecodeReport(first)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != CurrentReportSchemaVersion || decoded.Lifecycle == nil {
		t.Fatalf("decoded report = %#v", decoded)
	}
	if bytes.Index(first, []byte(`"id": "1"`)) > bytes.Index(first, []byte(`"id": "99"`)) {
		t.Fatal("v2 lifecycle entries are not sorted")
	}
}

func TestDecodeReportEnforcesLifecycleSectionBySchema(t *testing.T) {
	v2WithoutLifecycle := []byte(`{"schema_version":2,"dataset_version":"2","mode":"","embedding":{"provider":"","model_id":"","model_revision":"","dtype":"","pooling":"","vector_size":0},"configuration":{"name":"","fact_collection":"","chunk_collection":"","folder_collection":"","folder_top_k":0,"folder_threshold":0,"top_k":null},"top_k":null,"aggregate":{"hit_at":null,"mrr":0,"ndcg_at":null,"invariant_violations":0},"queries":[],"gates_passed":true}`)
	if _, err := DecodeReport(v2WithoutLifecycle); err == nil || !strings.Contains(err.Error(), "requires lifecycle") {
		t.Fatalf("DecodeReport() error = %v, want required lifecycle section", err)
	}
}

func TestDecodeReportRejectsUnknownLifecycleReasonCode(t *testing.T) {
	report := Report{
		SchemaVersion: CurrentReportSchemaVersion, DatasetVersion: "2",
		Queries: []QueryReport{{
			ID: "q", Target: "facts",
			Lifecycle: &QueryLifecycleReport{
				Intent: QueryIntentCurrent,
				Candidates: []LifecycleCandidateReport{{
					ID: "1", State: "current", Valid: true,
					Decision: PresentationInclude, ReasonCodes: []LifecycleReasonCode{"private text"},
				}},
			},
		}},
		Lifecycle: &LifecycleReport{},
	}
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(data); err == nil || !strings.Contains(err.Error(), "unknown reason code") {
		t.Fatalf("DecodeReport() error = %v, want unknown reason code", err)
	}
}

func TestDecodeReportRejectsMalformedV2LifecycleContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Report)
		want   string
	}{
		{
			name: "historical canonical",
			mutate: func(report *Report) {
				candidate := &report.Queries[0].Lifecycle.Candidates[0]
				candidate.State = "historical"
				candidate.Decision = PresentationSuppress
				candidate.ReasonCodes = []LifecycleReasonCode{ReasonHistorical}
			},
			want: "lifecycle constraints",
		},
		{
			name: "mismatched reason and decision",
			mutate: func(report *Report) {
				candidate := &report.Queries[0].Lifecycle.Candidates[0]
				candidate.Decision = PresentationSuppress
			},
			want: "inconsistent decision or reason",
		},
		{
			name: "missing fact lifecycle",
			mutate: func(report *Report) {
				report.Queries[0].Lifecycle = nil
			},
			want: "requires lifecycle subsection",
		},
		{
			name: "duplicate candidates",
			mutate: func(report *Report) {
				candidate := report.Queries[0].Lifecycle.Candidates[0]
				report.Queries[0].Lifecycle.Candidates =
					append(report.Queries[0].Lifecycle.Candidates, candidate)
			},
			want: "duplicate candidate",
		},
		{
			name: "aggregate mismatch",
			mutate: func(report *Report) {
				report.Lifecycle.Aggregate.Checks = 1
			},
			want: "aggregate check or violation",
		},
		{
			name: "canonical aggregate mismatch",
			mutate: func(report *Report) {
				report.Queries[0].Lifecycle.Candidates =
					append(report.Queries[0].Lifecycle.Candidates, LifecycleCandidateReport{
						ID: "2", State: "current", Valid: true,
						Decision:    PresentationDemote,
						ReasonCodes: []LifecycleReasonCode{ReasonCanonicalPreference},
					})
			},
			want: "canonical preference counts",
		},
		{
			name: "unknown structured violation invariant",
			mutate: func(report *Report) {
				report.Queries[0].Lifecycle.Checks = 1
				report.Queries[0].Lifecycle.Violations = []LifecycleViolation{{
					Scope: ViolationScopeQuery, QueryID: "q",
					CandidateID: "1", Invariant: "private_text",
				}}
				report.Lifecycle.Aggregate = LifecycleAggregateMetrics{Checks: 1, Violations: 1}
			},
			want: "unknown lifecycle violation invariant",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := validV2LifecycleReport()
			tt.mutate(&report)
			data, err := RenderJSON(report)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeReport(data); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DecodeReport() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDecodeReportRejectsFreeFormLifecycleViolation(t *testing.T) {
	report := validV2LifecycleReport()
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	query := document["queries"].([]any)[0].(map[string]any)
	lifecycleSection := query["lifecycle"].(map[string]any)
	lifecycleSection["violations"] = []any{"private query or fact text"}
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(data); err == nil {
		t.Fatal("DecodeReport accepted a free-form lifecycle violation string")
	}
}

func validV2LifecycleReport() Report {
	return Report{
		SchemaVersion:  CurrentReportSchemaVersion,
		DatasetVersion: "2",
		Queries: []QueryReport{{
			ID: "q", Target: "facts",
			Lifecycle: &QueryLifecycleReport{
				Intent: QueryIntentCurrent,
				Candidates: []LifecycleCandidateReport{{
					ID: "1", State: "current", Canonical: true, Valid: true,
					Decision:    PresentationInclude,
					ReasonCodes: []LifecycleReasonCode{ReasonCurrentTruth},
				}},
			},
		}},
		Lifecycle: &LifecycleReport{Transitions: []TransitionReport{}},
	}
}
