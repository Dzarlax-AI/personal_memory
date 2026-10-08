package aijudgment

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testCatalog() contextcatalog.Snapshot {
	return contextcatalog.Snapshot{SchemaVersion: 1, Version: "test-v1", CreatedAt: "2026-10-07T10:00:00Z", Entries: []contextcatalog.Entry{{
		Namespace: "projects", Tag: "health", Name: "Health", Summary: "Personal health observations and sync software.",
		Aliases: []string{"wellness"}, OwnedComponents: []string{"sync"}, Boundaries: []string{"not medical monitoring"}, Uses: []string{"health records"},
		PositiveExamples: []string{"Health sync pipeline"}, NegativeExamples: []string{"A fact merely recorded by the health client"}, EvidenceRefs: []string{"/private/local/evidence.md"}, ReviewStatus: "approved",
	}}}
}

func newTestProvider(t *testing.T, name string, mutate func([]byte) []byte) (Provider, *int, *string) {
	t.Helper()
	calls := 0
	var requestBody string
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("unexpected request headers/method: %s %v", req.Method, req.Header)
		}
		b, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		requestBody = string(b)
		response := makeResponse(name, b)
		if mutate != nil {
			response = mutate(response)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(response)))}, nil
	})
	model, endpoint := DecisionsModel, DecisionsURL
	if name == "jev" {
		model, endpoint = JevModel, JevURL
	}
	p, err := New(name, Config{Model: model, Endpoint: endpoint, APIKey: "test-secret", Transport: transport, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return p, &calls, &requestBody
}

func makeResponse(name string, request []byte) []byte {
	var questions []struct {
		Name    string `json:"name"`
		Choices []struct {
			Value string `json:"value"`
		} `json:"choices"`
	}
	var jevQuestions map[string]struct {
		Criteria map[string]string `json:"criteria"`
	}
	var model string
	if name == "decisions" {
		var req struct {
			Model     string `json:"model"`
			Questions []struct {
				Name    string `json:"name"`
				Choices []struct {
					Value string `json:"value"`
				} `json:"choices"`
			} `json:"questions"`
		}
		_ = jsonUnmarshal(request, &req)
		model, questions = req.Model, req.Questions
	} else {
		var req struct {
			Model     string `json:"model"`
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		_ = jsonUnmarshal(request, &req)
		model, jevQuestions = req.Model, req.Questions
	}
	var b strings.Builder
	b.WriteString(`{"model":"` + model + `","answers":`)
	if name == "decisions" {
		b.WriteByte('[')
		for i, q := range questions {
			if i > 0 {
				b.WriteByte(',')
			}
			writeAnswer(&b, q.Name, valuesOfChoices(q.Choices), true)
		}
		b.WriteByte(']')
	} else {
		b.WriteByte('{')
		i := 0
		for key, q := range jevQuestions {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`"` + key + `":`)
			values := make([]string, 0, len(q.Criteria))
			for k := range q.Criteria {
				values = append(values, k)
			}
			sort.Strings(values)
			writeAnswer(&b, key, values, false)
			i++
		}
		b.WriteByte('}')
	}
	b.WriteString(`,"usage":{"input_tokens":12,"output_tokens":4}}`)
	return []byte(b.String())
}

func valuesOfChoices(in []struct {
	Value string `json:"value"`
}) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		out = append(out, c.Value)
	}
	return out
}
func writeAnswer(b *strings.Builder, name string, choices []string, array bool) {
	selected := choices[0]
	if name == "primary" {
		for _, value := range choices {
			if value == "health" {
				selected = value
				break
			}
		}
	}
	if strings.HasPrefix(name, "related_") {
		selected = "yes"
	}
	if strings.HasPrefix(name, "c") {
		selected = "relevant"
	}
	if name == "none_relevant" {
		selected = "no"
	}
	if array {
		b.WriteString(`{"name":"` + name + `","type":"choice","choice":"` + selected + `","confidence":0.75,"probabilities":[`)
		for i, c := range choices {
			if i > 0 {
				b.WriteByte(',')
			}
			p := 0
			if c == selected {
				p = 1
			}
			b.WriteString(`{"value":"` + c + `","probability":` + itoa(p) + `}`)
		}
		b.WriteString(`]}`)
		return
	}
	b.WriteString(`{"type":"choice","choice":"` + selected + `","confidence":0.75,"probabilities":{`)
	for i, c := range choices {
		if i > 0 {
			b.WriteByte(',')
		}
		p := 0
		if c == selected {
			p = 1
		}
		b.WriteString(`"` + c + `":` + itoa(p))
	}
	b.WriteString(`}}`)
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
}

// Kept behind a helper so fixtures stay compact and do not depend on provider packages.
func jsonUnmarshal(data []byte, out any) error { return json.Unmarshal(data, out) }

func dropRelatedAnswer(data []byte) []byte {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil {
		return data
	}
	var answers map[string]json.RawMessage
	if json.Unmarshal(root["answers"], &answers) == nil {
		delete(answers, "related_health")
		root["answers"], _ = json.Marshal(answers)
	} else {
		var rows []map[string]json.RawMessage
		if json.Unmarshal(root["answers"], &rows) != nil {
			return data
		}
		filtered := rows[:0]
		for _, row := range rows {
			var name string
			_ = json.Unmarshal(row["name"], &name)
			if name != "related_health" {
				filtered = append(filtered, row)
			}
		}
		root["answers"], _ = json.Marshal(filtered)
	}
	b, err := json.Marshal(root)
	if err != nil {
		return data
	}
	return b
}

func TestBuildRequestsOmitLocalEvidence(t *testing.T) {
	cat := testCatalog()
	for _, providerName := range []string{"decisions", "jev"} {
		body, err := BuildClassifyRequest(providerName, modelFor(providerName), ClassifyInput{FactText: "I changed the health sync service.", Namespace: "projects", Origin: &Origin{SourceProject: "health", SourceKind: "client_declared", RecordedAt: "2026-10-07T10:00:00Z"}, Catalog: cat})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "/private/local/evidence.md") || strings.Contains(string(body), "point_id") {
			t.Fatalf("private evidence or point identifier in %s request: %s", providerName, body)
		}
		textOnly, err := BuildClassifyRequest(providerName, modelFor(providerName), ClassifyInput{FactText: "I changed the health sync service.", Namespace: "projects", Catalog: cat, OmitCatalog: true})
		if err != nil || strings.Contains(string(textOnly), "Personal health observations") || !strings.Contains(string(textOnly), "Health") {
			t.Fatalf("text-only classification choices changed: %s (%v)", textOnly, err)
		}
		rank, err := BuildRankRequest(providerName, modelFor(providerName), RankInput{Query: "health sync", LifecycleMode: "current", Candidates: []Candidate{{Alias: "c01", Text: "Health sync service", Namespace: "projects", Tags: []string{"health"}, AuthorityBucket: "canonical_current"}}, Catalog: &cat})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rank), "/private/local/evidence.md") {
			t.Fatalf("private evidence in rank request: %s", rank)
		}
	}
}

func TestProviderValidClassifyAndRankBothProtocols(t *testing.T) {
	for _, name := range []string{"decisions", "jev"} {
		t.Run(name, func(t *testing.T) {
			p, calls, _ := newTestProvider(t, name, nil)
			cr, usage, err := p.Classify(context.Background(), ClassifyInput{FactText: "Health sync uses Go.", Namespace: "projects", Catalog: testCatalog()})
			if err != nil || cr.Status != "decided" || cr.PrimaryTag != "health" || !usage.Known || usage.InputTokens != 12 {
				t.Fatalf("classify = %#v %#v %v", cr, usage, err)
			}
			rr, usage, err := p.Rank(context.Background(), RankInput{Query: "health sync", LifecycleMode: "current", Candidates: []Candidate{{Alias: "c01", Text: "Health sync uses Go.", Namespace: "projects", AuthorityBucket: "canonical_current"}}})
			if err != nil || rr.Status != "decided" || rr.Scores["c01"] != 1 || !usage.Known {
				t.Fatalf("rank = %#v %#v %v", rr, usage, err)
			}
			if *calls != 2 {
				t.Fatalf("calls=%d", *calls)
			}
		})
	}
}

func TestProviderRejectsMalformedAnswersBothProtocols(t *testing.T) {
	mutations := map[string]func([]byte) []byte{
		"missing": func(b []byte) []byte { return dropRelatedAnswer(b) },
		"duplicate": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"model":`, `"model":"duplicate","model":`, 1))
		},
		"unknown": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"related_health"`, `"unknown_alias"`, 1))
		},
		"nan": func(b []byte) []byte {
			s := string(b)
			s = strings.Replace(s, `"yes":1`, `"yes":NaN`, 1)
			s = strings.Replace(s, `"value":"yes","probability":1`, `"value":"yes","probability":NaN`, 1)
			return []byte(s)
		},
		"refusal": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"type":"choice"`, `"type":"refusal","refusal":"no"`, 1))
		},
		"model": func(b []byte) []byte {
			s := string(b)
			s = strings.Replace(s, `"model":"jev-1.13.0"`, `"model":"jev-latest"`, 1)
			return []byte(strings.Replace(s, `"model":"gpt-6-luna"`, `"model":"gpt-6-luna-other"`, 1))
		},
	}
	for _, providerName := range []string{"decisions", "jev"} {
		for label, mutate := range mutations {
			t.Run(providerName+"/"+label, func(t *testing.T) {
				p, _, _ := newTestProvider(t, providerName, mutate)
				_, _, err := p.Classify(context.Background(), ClassifyInput{FactText: "Health sync fact", Namespace: "projects", Catalog: testCatalog()})
				if err == nil {
					t.Fatal("malformed response accepted")
				}
			})
		}
	}
}

func TestProviderDoesNotFollowRedirects(t *testing.T) {
	calls := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://elsewhere.invalid"}}, Body: io.NopCloser(strings.NewReader("redirect"))}, nil
	})
	p, err := New("jev", Config{Model: JevModel, APIKey: "test-secret", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = p.Classify(context.Background(), ClassifyInput{FactText: "fact", Namespace: "projects", Catalog: testCatalog()})
	if err == nil || calls != 1 {
		t.Fatalf("redirect followed or accepted: err=%v calls=%d", err, calls)
	}
}

func TestDecisionsDocumentedUsageDetails(t *testing.T) {
	p, _, _ := newTestProvider(t, "decisions", func(b []byte) []byte {
		return bytes.Replace(b, []byte(`"usage":{"input_tokens":12,"output_tokens":4}`), []byte(`"usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":16}`), 1)
	})
	_, u, err := p.Classify(context.Background(), ClassifyInput{FactText: "Health sync fact", Namespace: "projects", Catalog: testCatalog()})
	if err != nil || !u.Known {
		t.Fatalf("documented usage rejected: %v", err)
	}
}

func TestDeclaredProjectDescriptionAndExplicitRankContext(t *testing.T) {
	e := contextcatalog.Entry{Namespace: "projects", Tag: "declared", Name: "Declared project", Summary: "Synthetic purpose", ReviewStatus: "declared"}
	catalog := contextcatalog.Snapshot{SchemaVersion: 1, Version: "declared-v1", CreatedAt: "2026-10-08T00:00:00Z", Entries: []contextcatalog.Entry{e}}
	description := DescribeProject(e)
	for _, providerName := range []string{"decisions", "jev"} {
		raw, err := BuildRankRequest(providerName, modelFor(providerName), RankInput{Query: "synthetic", LifecycleMode: "current", Catalog: &catalog, ProjectContext: &description, Candidates: []Candidate{{Alias: "c1", Text: "Synthetic fact", Namespace: "projects", AuthorityBucket: "other_current"}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"project_context", "client_declared", "declared"} {
			if !strings.Contains(string(raw), field) {
				t.Errorf("missing %s in %s", field, raw)
			}
		}
		classification, err := BuildClassifyRequest(providerName, modelFor(providerName), ClassifyInput{FactText: "Synthetic fact", Namespace: "projects", Catalog: catalog})
		if err != nil || !strings.Contains(string(classification), "related_declared") {
			t.Fatalf("declared class membership %v %s", err, classification)
		}
	}
}
