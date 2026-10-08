package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
)

func TestSubjectNonProjectWritesNeverInfer(t *testing.T) {
	for _, tc := range []struct{ ns, scope, want string }{
		{"personal", "non_project", "non_project"},
		{"tech", "non_project", "non_project"},
		{"projects", "non_project", "namespace_scope_mismatch"},
		{"personal", "project", "namespace_scope_mismatch"},
	} {
		t.Run(tc.ns+tc.scope, func(t *testing.T) {
			b := &aiTestBackend{}
			s := newAIServer(t, b)
			p := &fakeAIProvider{}
			if err := configureAIForTest(t, s, context.Background(), aiTestConfig(t, "on", "off"), p, nil); err != nil {
				t.Fatal(err)
			}
			r := aiStoredResult(t, s, map[string]interface{}{"fact": "I live in Serbia", "namespace": tc.ns, "subject_scope": tc.scope, "source_project": "alpha", "source_kind": "client_declared"})
			if !r.Stored || r.SubjectDecision != tc.want || r.AI != nil || p.writes.Load() != 0 {
				t.Fatalf("unexpected result: %#v calls %d", r, p.writes.Load())
			}
			if b.stored.Payload["namespace"] != tc.ns || b.stored.Payload["primary_tag"] != "" || originPayload(b.stored.Payload) == nil || subjectPayload(b.stored.Payload) == nil {
				t.Fatal("namespace, origin or subject changed")
			}
		})
	}
}

func TestSubjectFreshLifeWriteWithoutAIOrRegistry(t *testing.T) {
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	r := aiStoredResult(t, s, map[string]interface{}{"fact": "I enjoy walking", "namespace": "personal", "subject_scope": "non_project"})
	if r.SubjectDecision != "non_project" || !r.Stored || originPayload(b.stored.Payload) != nil {
		t.Fatal(r)
	}
}

func TestSubjectContextFlowsToProviderAndAbstentionsPreserveGrouping(t *testing.T) {
	for _, choice := range []string{"non_project", "multiple_projects", "other_project", "insufficient_context", "known_project"} {
		t.Run(choice, func(t *testing.T) {
			b := &aiTestBackend{}
			s := newAIServer(t, b)
			p := &fakeAIProvider{classify: func(_ context.Context, in aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
				if in.SubjectScope != "unknown" || in.SubjectContext != "This component stores explicit assertions" || in.Origin != nil {
					t.Fatalf("input %#v", in)
				}
				result := aijudgment.ClassifyResult{Status: "abstained", SubjectDecision: choice}
				if choice == "known_project" {
					result.Status = "decided"
					result.PrimaryTag = "alpha"
				}
				return result, aijudgment.Usage{Known: true, InputTokens: 1}, nil
			}}
			if err := configureAIForTest(t, s, context.Background(), aiTestConfig(t, "on", "off"), p, nil); err != nil {
				t.Fatal(err)
			}
			r := aiStoredResult(t, s, map[string]interface{}{"fact": "Component behavior", "namespace": "projects", "subject_context": "This component stores explicit assertions"})
			if r.AI == nil || r.AI.SubjectDecision != choice || !r.Stored || p.writes.Load() != 1 {
				t.Fatal(r)
			}
			if choice != "known_project" && b.stored.Payload["primary_tag"] != "" {
				t.Fatal("abstention forced grouping")
			}
			if subjectPayload(b.stored.Payload).Context != "This component stores explicit assertions" {
				t.Fatal("context lost")
			}
		})
	}
}

func TestSubjectValidationAndUpdateClearsStaleContext(t *testing.T) {
	for _, args := range []map[string]interface{}{{"subject_scope": "bad"}, {"subject_context": 4}, {"subject_context": strings.Repeat("a", 2049)}, {"subject_context": string([]byte{255})}} {
		if _, err := subjectArguments(args); err == nil {
			t.Fatal("invalid subject accepted", args)
		}
	}
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	r := aiStoredResult(t, s, map[string]interface{}{"fact": "Old assertion", "namespace": "projects", "subject_context": "Old component"})
	q, err := s.updateFact(context.Background(), toolRequest(map[string]interface{}{"point_id": r.PointID, "new_fact": "Changed assertion"}))
	if err != nil || q.IsError {
		t.Fatalf("update %v %#v", err, q)
	}
	if _, exists := b.stored.Payload["subject"]; exists {
		t.Fatal("changed text retained stale context")
	}
	q, err = s.updateFact(context.Background(), toolRequest(map[string]interface{}{"point_id": b.stored.ID, "new_fact": "Changed assertion", "subject_scope": "non_project"}))
	if err != nil || q.IsError || subjectPayload(b.stored.Payload).Scope != "non_project" {
		t.Fatal("explicit subject replacement failed", err)
	}
}

func TestSubjectImportPreservesAndValidatesMetadata(t *testing.T) {
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	for _, valid := range []bool{false, true} {
		scope := "invalid"
		if valid {
			scope = "non_project"
		}
		raw, _ := json.Marshal([]map[string]interface{}{{"text": "Imported life fact", "namespace": "personal", "subject": FactSubject{Scope: scope, SourceKind: "client_declared"}}})
		r, err := s.importFacts(context.Background(), toolRequest(map[string]interface{}{"facts": string(raw)}))
		if err != nil || r.IsError {
			t.Fatal(err, r)
		}
		out := r.StructuredContent.(ImportFactsResult)
		if valid && (out.Imported != 1 || subjectPayload(b.stored.Payload) == nil) {
			t.Fatal("subject import lost", out)
		}
		if !valid && out.Imported != 0 {
			t.Fatal("invalid import stored", out)
		}
	}
}
