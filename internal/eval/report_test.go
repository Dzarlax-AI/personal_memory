package eval

import (
	"bytes"
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
			ID: "q", Results: []RetrievedItem{},
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
			ID: "q",
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
