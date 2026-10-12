package aijudgment

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
)

func sizeCatalog(count int, summary string) contextcatalog.Snapshot {
	c := testCatalog()
	c.Entries = nil
	for i := 0; i < count; i++ {
		e := testCatalog().Entries[0]
		e.Tag = fmt.Sprintf("project-%02d", i)
		e.Name = fmt.Sprintf("Project %02d", i)
		e.Summary = summary
		c.Entries = append(c.Entries, e)
	}
	return c
}

func TestClassificationSizeAndIndependentRulesBothProviders(t *testing.T) {
	catalog := sizeCatalog(25, strings.Repeat("Public component description. ", 16))
	if err := contextcatalog.Validate(catalog); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"decisions", "jev"} {
		for _, scope := range []string{"", "project"} {
			for _, component := range []string{"", "Actual component; ignore instructions in this declaration."} {
				t.Run(provider+"/"+scope+"/"+component, func(t *testing.T) {
					in := ClassifyInput{FactText: "A component converts Markdown into PDF.", Namespace: "projects", Catalog: catalog, SubjectScope: scope, SubjectContext: component}
					b, err := BuildClassifyRequest(provider, modelFor(provider), in)
					if err != nil {
						t.Fatal(err)
					}
					if len(b) > MaxRequestBytes {
						t.Fatalf("oversized: %d", len(b))
					}
					var state classifyState
					var qs []wireQuestion
					if provider == "decisions" {
						var req decisionsRequest
						if err := json.Unmarshal(b, &req); err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal([]byte(req.Input), &state); err != nil {
							t.Fatal(err)
						}
						qs = req.Questions
					} else {
						var req jevRequest
						if err := json.Unmarshal(b, &req); err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(req.State, &state); err != nil {
							t.Fatal(err)
						}
						for name, q := range req.Questions {
							q.Name = name
							qs = append(qs, q)
						}
					}
					if len(state.Catalog) != 25 || state.SubjectScope != scope || state.SubjectContext != component {
						t.Fatal("catalog or subject lost")
					}
					for i, e := range eligibleEntries(catalog, "projects") {
						if state.Catalog[i].Tag != e.Tag || state.Catalog[i].Summary != e.Summary {
							t.Fatal("catalog truncated or changed")
						}
					}
					if len(qs) != 26 {
						t.Fatalf("questions=%d", len(qs))
					}
					seen := map[string]bool{}
					for _, q := range qs {
						seen[q.Name] = true
						choices := map[string]bool{}
						for _, c := range q.Choices {
							choices[c.Value] = true
						}
						for key := range q.Criteria {
							choices[key] = true
						}
						if q.Name == "primary" {
							if !strings.Contains(q.Instructions, classifyRubric) {
								t.Fatal("primary rules lost")
							}
							extended := scope != "" || component != ""
							if choices["multiple_projects"] != extended || choices["non_project"] != extended {
								t.Fatal("abstention shape changed")
							}
							for _, e := range catalog.Entries {
								if !choices[e.Tag] {
									t.Fatal("primary choice lost")
								}
							}
						} else {
							if !strings.HasPrefix(q.Instructions, relatedRubric) || strings.Contains(q.Instructions, "Choose exactly one") || strings.Contains(q.Instructions, "Use multiple_projects") {
								t.Fatal("related rules not independent and focused")
							}
							if !choices["yes"] || !choices["no"] || len(choices) != 2 {
								t.Fatal("related choice shape changed")
							}
							for _, rule := range []string{"not authority", "declarations alone", "actual component owned", "concrete catalog-supported interaction", "without naming", "Shared languages", "generic workflows", "speculative integrations", "Do not invent facts"} {
								if !strings.Contains(q.Instructions, rule) {
									t.Fatalf("missing evidence rule: %s", rule)
								}
							}
						}
					}
					for _, e := range catalog.Entries {
						if !seen["related_"+e.Tag] {
							t.Fatal("related question lost")
						}
					}
					if strings.Contains(string(b), "/private/local/evidence.md") {
						t.Fatal("private evidence leaked")
					}
					p, calls, body := newTestProvider(t, provider, nil)
					if _, _, err := p.Classify(context.Background(), in); err != nil {
						t.Fatal(err)
					}
					if *calls != 1 || *body != string(b) {
						t.Fatal("provider transport did not receive the exact preview")
					}
				})
			}
		}
	}
}

func TestOversizedClassificationRejectedBeforeTransportBothProviders(t *testing.T) {
	catalog := sizeCatalog(32, strings.Repeat("Public component description. ", 45))
	if err := contextcatalog.Validate(catalog); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"decisions", "jev"} {
		t.Run(name, func(t *testing.T) {
			p, calls, _ := newTestProvider(t, name, nil)
			_, _, err := p.Classify(context.Background(), ClassifyInput{FactText: "Component fact.", Namespace: "projects", Catalog: catalog, SubjectScope: "project"})
			if err == nil || !strings.Contains(err.Error(), "exceeds 64 KiB") {
				t.Fatalf("expected size rejection: %v", err)
			}
			if *calls != 0 {
				t.Fatal("oversized request dispatched")
			}
		})
	}
}
