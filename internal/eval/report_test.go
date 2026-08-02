package eval

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
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

func TestRenderMarkdownEscapesLifecycleCandidateFields(t *testing.T) {
	report := Report{
		SchemaVersion: CurrentReportSchemaVersion,
		Lifecycle:     &LifecycleReport{Aggregate: LifecycleAggregateMetrics{}, Transitions: []TransitionReport{}},
		Queries: []QueryReport{{
			ID: "query|id", Target: "facts", Mode: "flat",
			Lifecycle: &QueryLifecycleReport{
				Intent: QueryIntentCurrent,
				Candidates: []LifecycleCandidateReport{{
					ID: "candidate|id", State: lifecycle.State("state|value"),
					Decision:    PresentationDecision("decision|value"),
					ReasonCodes: []LifecycleReasonCode{"reason|value"},
				}},
			},
		}},
	}
	markdown := RenderMarkdown(report)
	for _, want := range []string{
		`query\|id`, `candidate\|id`, `state\|value`, `decision\|value`, `reason\|value`,
	} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("Markdown report does not contain escaped value %q:\n%s", want, markdown)
		}
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
			ID: "q", Target: "facts", Mode: "flat", Results: []RetrievedItem{},
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
	firstMarkdown := RenderMarkdown(report)
	secondMarkdown := RenderMarkdown(report)
	if !bytes.Equal(first, second) || firstMarkdown != secondMarkdown {
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

func TestLifecycleRenderingDoesNotMutateCallerAndIsConcurrentSafe(t *testing.T) {
	report := validV2LifecycleReport()
	report.Queries[0].Lifecycle.Candidates = []LifecycleCandidateReport{
		{
			ID: "2", State: "current", Valid: true,
			Decision:    PresentationInclude,
			ReasonCodes: []LifecycleReasonCode{ReasonCurrentTruth},
		},
		{
			ID: "1", State: "historical", Valid: true,
			Decision:    PresentationSuppress,
			ReasonCodes: []LifecycleReasonCode{ReasonHistorical},
		},
	}
	report.Lifecycle.Transitions = []TransitionReport{
		{ID: "z", Valid: true, ReasonCode: ReasonTransitionValid, Passed: true},
		{ID: "a", Valid: false, ReasonCode: ReasonTargetInvalid, Passed: true},
	}
	report.Lifecycle.Aggregate.Checks = 2
	before, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	expectedJSON, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	expectedMarkdown := RenderMarkdown(report)
	after, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("render mutated caller report\nbefore: %s\nafter: %s", before, after)
	}

	const workers = 24
	var wait sync.WaitGroup
	errors := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			gotJSON, renderErr := RenderJSON(report)
			if renderErr != nil {
				errors <- renderErr.Error()
				return
			}
			if !bytes.Equal(gotJSON, expectedJSON) {
				errors <- "concurrent JSON render differed"
				return
			}
			if gotMarkdown := RenderMarkdown(report); gotMarkdown != expectedMarkdown {
				errors <- "concurrent Markdown render differed"
			}
		}()
	}
	wait.Wait()
	close(errors)
	for renderError := range errors {
		t.Error(renderError)
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
			ID: "q", Target: "facts", Mode: "flat",
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
			want: "lifecycle",
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
				report.Lifecycle.Aggregate.Checks = 2
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
				report.Lifecycle.Aggregate = LifecycleAggregateMetrics{Checks: 2, Violations: 1}
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

func TestDecodeReportValidatesPerQueryContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Report)
		field  string
	}{
		{
			name: "facts require flat mode",
			mutate: func(report *Report) {
				report.Queries[0].Mode = "hierarchical"
			},
			field: "mode",
		},
		{
			name: "target",
			mutate: func(report *Report) {
				report.Queries[0].Target = "other"
			},
			field: "target",
		},
		{
			name: "current rejects as of",
			mutate: func(report *Report) {
				report.Queries[0].Lifecycle.AsOf = "2025-03-14"
			},
			field: "as_of",
		},
		{
			name: "as of requires date",
			mutate: func(report *Report) {
				report.Queries[0].Lifecycle.Intent = QueryIntentAsOf
			},
			field: "as_of",
		},
		{
			name: "documents reject lifecycle",
			mutate: func(report *Report) {
				report.Queries[0].Target = "documents"
			},
			field: "lifecycle",
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
			if _, err := DecodeReport(data); err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("DecodeReport() error = %v, want field %q rejection", err, tt.field)
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

func TestDecodeReportRequiresV2LifecycleFieldPresence(t *testing.T) {
	t.Run("invalid candidate required fields", func(t *testing.T) {
		for _, field := range []string{"id", "canonical", "expired", "decision", "reason_codes", "valid"} {
			t.Run(field, func(t *testing.T) {
				report := validV2LifecycleReport()
				candidate := &report.Queries[0].Lifecycle.Candidates[0]
				candidate.State = ""
				candidate.Canonical = false
				candidate.Valid = false
				candidate.Decision = PresentationSuppress
				candidate.ReasonCodes = []LifecycleReasonCode{ReasonInvalidLifecycle}
				document := reportDocument(t, report)
				candidateObject := lifecycleCandidateObject(t, document)
				delete(candidateObject, field)
				assertReportDecodeFails(t, document, field)
			})
		}
	})

	t.Run("valid candidate state", func(t *testing.T) {
		document := reportDocument(t, validV2LifecycleReport())
		delete(lifecycleCandidateObject(t, document), "state")
		assertReportDecodeFails(t, document, "state")
	})

	t.Run("query lifecycle zero checks and arrays", func(t *testing.T) {
		for _, field := range []string{"intent", "candidates", "checks", "violations"} {
			t.Run(field, func(t *testing.T) {
				document := reportDocument(t, validV2LifecycleReport())
				queryLifecycle := reportQueryLifecycleObject(t, document)
				delete(queryLifecycle, field)
				assertReportDecodeFails(t, document, field)
			})
		}
	})

	t.Run("zero aggregate counters", func(t *testing.T) {
		for _, field := range []string{
			"checks", "violations",
			"canonical_preference_checks", "canonical_preference_violations",
		} {
			t.Run(field, func(t *testing.T) {
				report := validV2LifecycleReport()
				report.Lifecycle.Transitions = []TransitionReport{}
				report.Lifecycle.Aggregate = LifecycleAggregateMetrics{}
				document := reportDocument(t, report)
				aggregate := document["lifecycle"].(map[string]any)["aggregate"].(map[string]any)
				delete(aggregate, field)
				assertReportDecodeFails(t, document, field)
			})
		}
	})

	t.Run("top lifecycle fields", func(t *testing.T) {
		for _, field := range []string{"aggregate", "transitions"} {
			t.Run(field, func(t *testing.T) {
				document := reportDocument(t, validV2LifecycleReport())
				delete(document["lifecycle"].(map[string]any), field)
				assertReportDecodeFails(t, document, field)
			})
		}
	})

	t.Run("false transition result and required fields", func(t *testing.T) {
		for _, field := range []string{"id", "valid", "reason_code", "passed", "violations"} {
			t.Run(field, func(t *testing.T) {
				document := reportDocument(t, validV2LifecycleReport())
				transition := document["lifecycle"].(map[string]any)["transitions"].([]any)[0].(map[string]any)
				delete(transition, field)
				assertReportDecodeFails(t, document, field)
			})
		}
	})

	t.Run("false transition passed", func(t *testing.T) {
		report := validV2LifecycleReport()
		transition := &report.Lifecycle.Transitions[0]
		transition.Passed = false
		transition.Violations = []LifecycleViolation{{
			Scope: ViolationScopeTransition, ScenarioID: transition.ID,
			Invariant: InvariantTransitionValid,
		}}
		report.Lifecycle.Aggregate.Violations = 1
		document := reportDocument(t, report)
		transitionObject := document["lifecycle"].(map[string]any)["transitions"].([]any)[0].(map[string]any)
		delete(transitionObject, "passed")
		assertReportDecodeFails(t, document, "passed")
	})

	t.Run("structured violation identifiers", func(t *testing.T) {
		for _, field := range []string{"scope", "query_id", "candidate_id", "invariant"} {
			t.Run(field, func(t *testing.T) {
				report := validV2LifecycleReport()
				report.Queries[0].Lifecycle.Checks = 1
				report.Queries[0].Lifecycle.Violations = []LifecycleViolation{{
					Scope: ViolationScopeQuery, QueryID: "q",
					CandidateID: "2", Invariant: InvariantCandidatePresent,
				}}
				report.Lifecycle.Aggregate = LifecycleAggregateMetrics{Checks: 2, Violations: 1}
				document := reportDocument(t, report)
				violation := reportQueryLifecycleObject(t, document)["violations"].([]any)[0].(map[string]any)
				delete(violation, field)
				assertReportDecodeFails(t, document, field)
			})
		}
	})

	t.Run("structured transition violation identifiers", func(t *testing.T) {
		for _, field := range []string{"scope", "scenario_id", "invariant"} {
			t.Run(field, func(t *testing.T) {
				report := validV2LifecycleReport()
				transition := &report.Lifecycle.Transitions[0]
				transition.Passed = false
				transition.Violations = []LifecycleViolation{{
					Scope: ViolationScopeTransition, ScenarioID: transition.ID,
					Invariant: InvariantTransitionReason,
				}}
				report.Lifecycle.Aggregate.Violations = 1
				document := reportDocument(t, report)
				transitionObject := document["lifecycle"].(map[string]any)["transitions"].([]any)[0].(map[string]any)
				violation := transitionObject["violations"].([]any)[0].(map[string]any)
				delete(violation, field)
				assertReportDecodeFails(t, document, field)
			})
		}
	})
}

func reportDocument(t *testing.T, report Report) map[string]any {
	t.Helper()
	data, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func reportQueryLifecycleObject(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	return document["queries"].([]any)[0].(map[string]any)["lifecycle"].(map[string]any)
}

func lifecycleCandidateObject(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	return reportQueryLifecycleObject(t, document)["candidates"].([]any)[0].(map[string]any)
}

func assertReportDecodeFails(t *testing.T, document map[string]any, field string) {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(data); err == nil || !strings.Contains(err.Error(), field) {
		t.Fatalf("DecodeReport() error = %v, want omitted field %q rejection", err, field)
	}
}

func validV2LifecycleReport() Report {
	return Report{
		SchemaVersion:  CurrentReportSchemaVersion,
		DatasetVersion: "2",
		Queries: []QueryReport{{
			ID: "q", Target: "facts", Mode: "flat",
			Lifecycle: &QueryLifecycleReport{
				Intent: QueryIntentCurrent,
				Candidates: []LifecycleCandidateReport{{
					ID: "1", State: "current", Canonical: true, Valid: true,
					Decision:    PresentationInclude,
					ReasonCodes: []LifecycleReasonCode{ReasonCurrentTruth},
				}},
			},
		}},
		Lifecycle: &LifecycleReport{
			Aggregate: LifecycleAggregateMetrics{Checks: 1},
			Transitions: []TransitionReport{{
				ID: "invalid-transition", Valid: false,
				ReasonCode: ReasonTargetInvalid, Passed: true,
				Violations: []LifecycleViolation{},
			}},
		},
	}
}
