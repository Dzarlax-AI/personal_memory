package retrieval

import (
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func candidate(id string, score float64, text string, fields ...Field) Candidate {
	return Candidate{
		ID:         id,
		DenseScore: score,
		Fields:     append([]Field{{Name: "text", Value: text}}, fields...),
	}
}

func rank(t *testing.T, query string, candidates []Candidate, limit int) []Result {
	t.Helper()
	got, err := Rank(query, candidates, Options{RRFConstant: 60, Limit: limit})
	if err != nil {
		t.Fatalf("Rank() error = %v", err)
	}
	return got
}

func ids(results []Result) []string {
	got := make([]string, len(results))
	for i := range results {
		got[i] = results[i].Candidate.ID
	}
	return got
}

func TestRankSemanticOnlyFallbackAndScorePreservation(t *testing.T) {
	candidates := []Candidate{
		candidate("second", 0.8, "unrelated"),
		candidate("first", 0.9, "different"),
		candidate("third", 0.7, "other"),
	}

	got := rank(t, "semantic query", candidates, 3)
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("ids = %v, want %v", ids(got), want)
	}
	for i, result := range got {
		if result.Lexical.HasSignal || result.LexicalRank != 0 {
			t.Fatalf("result %d has fake lexical signal: %+v", i, result)
		}
	}
	if got[0].Candidate.DenseScore != 0.9 || got[1].Candidate.DenseScore != 0.8 {
		t.Fatalf("dense scores changed: %+v", got)
	}
}

func TestRankLexicalSignalsLiftCandidates(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		candidates []Candidate
		wantFirst  string
		check      func(*testing.T, Result)
	}{
		{
			name:  "exact phrase",
			query: "release checklist",
			candidates: []Candidate{
				candidate("dense", 0.99, "unrelated semantic match"),
				candidate("phrase", 0.60, "The RELEASE \t CHECKLIST is ready"),
			},
			wantFirst: "phrase",
			check: func(t *testing.T, result Result) {
				if !result.Lexical.ExactPhrase {
					t.Fatal("exact phrase was not diagnosed")
				}
			},
		},
		{
			name:  "rare project name",
			query: "Dzarlax",
			candidates: []Candidate{
				candidate("dense", 0.99, "a generic profile"),
				candidate("name", 0.60, "Notes by DZARLAX"),
			},
			wantFirst: "name",
		},
		{
			name:  "uuid",
			query: "550e8400-e29b-41d4-a716-446655440000",
			candidates: []Candidate{
				candidate("tokens", 0.99, "unrelated semantic match"),
				candidate("uuid", 0.60, "point 550E8400-E29B-41D4-A716-446655440000"),
			},
			wantFirst: "uuid",
			check: func(t *testing.T, result Result) {
				if result.Lexical.IdentifierMatches != 1 {
					t.Fatalf("identifier matches = %d, want 1", result.Lexical.IdentifierMatches)
				}
			},
		},
		{
			name:  "kebab case path",
			query: "personal-memory",
			candidates: []Candidate{
				candidate("words", 0.99, "unrelated semantic match"),
				candidate("path", 0.60, "repository", Field{Name: "path", Value: "projects/personal-memory/README.md"}),
			},
			wantFirst: "path",
		},
		{
			name:  "russian",
			query: "ПАМЯТЬ проекта",
			candidates: []Candidate{
				candidate("dense", 0.99, "проектная документация"),
				candidate("russian", 0.60, "Память   проекта хранится локально"),
			},
			wantFirst: "russian",
		},
		{
			name:  "mixed language",
			query: "проект Memory",
			candidates: []Candidate{
				candidate("dense", 0.99, "project notes"),
				candidate("mixed", 0.60, "MEMORY для ПРОЕКТ"),
			},
			wantFirst: "mixed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rank(t, tt.query, tt.candidates, len(tt.candidates))
			if got[0].Candidate.ID != tt.wantFirst {
				t.Fatalf("first = %q, want %q; results=%+v", got[0].Candidate.ID, tt.wantFirst, got)
			}
			if !got[0].Lexical.HasSignal {
				t.Fatal("lifted candidate has no lexical signal")
			}
			if tt.check != nil {
				tt.check(t, got[0])
			}
		})
	}
}

func TestRankPartialCoverageAndMultipleFields(t *testing.T) {
	got := rank(t, "alpha beta gamma", []Candidate{
		candidate("one", 0.9, "alpha"),
		candidate("two", 0.8, "alpha", Field{Name: "heading", Value: "beta"}),
		candidate("three", 0.7, "unrelated", Field{Name: "heading", Value: "alpha"}, Field{Name: "path", Value: "docs/beta/gamma.md"}),
	}, 3)

	byID := make(map[string]Result, len(got))
	for _, result := range got {
		byID[result.Candidate.ID] = result
	}
	if byID["three"].LexicalRank != 1 || byID["two"].LexicalRank != 2 || byID["one"].LexicalRank != 3 {
		t.Fatalf("lexical ranks = one:%d two:%d three:%d", byID["one"].LexicalRank, byID["two"].LexicalRank, byID["three"].LexicalRank)
	}
	if byID["three"].Lexical.MatchedTokens != 3 || byID["three"].Lexical.TotalTokens != 3 || byID["three"].Lexical.TokenCoverage != 1 {
		t.Fatalf("unexpected coverage: %+v", byID["three"].Lexical)
	}
	if len(byID["three"].Lexical.Fields) != 3 {
		t.Fatalf("field diagnostics = %+v", byID["three"].Lexical.Fields)
	}
}

func TestRankLexicalTiesUseDenseOrder(t *testing.T) {
	got := rank(t, "needle", []Candidate{
		candidate("low", 0.7, "needle"),
		candidate("high", 0.9, "needle"),
	}, 2)
	if want := []string{"high", "low"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("ids = %v, want %v", ids(got), want)
	}
	if got[0].LexicalRank != got[1].LexicalRank {
		t.Fatalf("equal lexical signals got ranks %d and %d", got[0].LexicalRank, got[1].LexicalRank)
	}
}

func TestRankDenseAndFusedTiesAreDeterministic(t *testing.T) {
	t.Run("dense ties", func(t *testing.T) {
		got := rank(t, "absent", []Candidate{
			candidate("z", 0.8, "none"),
			candidate("a", 0.8, "none"),
		}, 2)
		if want := []string{"a", "z"}; !reflect.DeepEqual(ids(got), want) {
			t.Fatalf("ids = %v, want %v", ids(got), want)
		}
		if got[0].DenseRank != got[1].DenseRank || got[0].RRFScore != got[1].RRFScore {
			t.Fatalf("dense tie was not preserved: %+v", got)
		}
	})

	t.Run("fused ties prefer dense rank", func(t *testing.T) {
		got := rank(t, "needle phrase", []Candidate{
			candidate("dense-first", 0.9, "needle only"),
			candidate("lexical-first", 0.8, "needle phrase"),
		}, 2)
		if got[0].Candidate.ID != "dense-first" {
			t.Fatalf("fused tie order = %v", ids(got))
		}
		if got[0].RRFScore != got[1].RRFScore {
			t.Fatalf("test setup did not produce fused tie: %v != %v", got[0].RRFScore, got[1].RRFScore)
		}
	})
}

func TestRankInputOrderDoesNotMatter(t *testing.T) {
	base := []Candidate{
		candidate("a", 0.9, "alpha beta"),
		candidate("b", 0.8, "alpha"),
		candidate("c", 0.8, "beta"),
		candidate("d", 0.7, "nothing"),
	}
	want := ids(rank(t, "alpha beta", base, 4))

	random := rand.New(rand.NewSource(42))
	for i := 0; i < 100; i++ {
		permuted := append([]Candidate(nil), base...)
		random.Shuffle(len(permuted), func(i, j int) {
			permuted[i], permuted[j] = permuted[j], permuted[i]
		})
		if got := ids(rank(t, "alpha beta", permuted, 4)); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d ids = %v, want %v", i, got, want)
		}
	}
}

func TestRankValidation(t *testing.T) {
	valid := candidate("id", 0.5, "text")
	tests := []struct {
		name       string
		query      string
		candidates []Candidate
		options    Options
	}{
		{name: "empty query", query: " \t ", candidates: []Candidate{valid}, options: Options{RRFConstant: 60, Limit: 1}},
		{name: "empty candidates", query: "query", options: Options{RRFConstant: 60, Limit: 1}},
		{name: "empty id", query: "query", candidates: []Candidate{candidate("", 0.5, "text")}, options: Options{RRFConstant: 60, Limit: 1}},
		{name: "id surrounding whitespace", query: "query", candidates: []Candidate{candidate(" id ", 0.5, "text")}, options: Options{RRFConstant: 60, Limit: 1}},
		{name: "duplicate id", query: "query", candidates: []Candidate{valid, valid}, options: Options{RRFConstant: 60, Limit: 1}},
		{name: "nan score", query: "query", candidates: []Candidate{candidate("id", math.NaN(), "text")}, options: Options{RRFConstant: 60, Limit: 1}},
		{name: "positive infinity", query: "query", candidates: []Candidate{candidate("id", math.Inf(1), "text")}, options: Options{RRFConstant: 60, Limit: 1}},
		{name: "negative infinity", query: "query", candidates: []Candidate{candidate("id", math.Inf(-1), "text")}, options: Options{RRFConstant: 60, Limit: 1}},
		{name: "missing text field", query: "query", candidates: []Candidate{{ID: "id", DenseScore: 0.5, Fields: []Field{{Name: "heading", Value: "text"}}}}, options: Options{RRFConstant: 60, Limit: 1}},
		{name: "empty text field", query: "query", candidates: []Candidate{candidate("id", 0.5, "")}, options: Options{RRFConstant: 60, Limit: 1}},
		{name: "empty field name", query: "query", candidates: []Candidate{candidate("id", 0.5, "text", Field{Value: "value"})}, options: Options{RRFConstant: 60, Limit: 1}},
		{name: "field name surrounding whitespace", query: "query", candidates: []Candidate{candidate("id", 0.5, "text", Field{Name: " heading ", Value: "value"})}, options: Options{RRFConstant: 60, Limit: 1}},
		{name: "duplicate field", query: "query", candidates: []Candidate{candidate("id", 0.5, "text", Field{Name: "text", Value: "other"})}, options: Options{RRFConstant: 60, Limit: 1}},
		{name: "zero rrf constant", query: "query", candidates: []Candidate{valid}, options: Options{Limit: 1}},
		{name: "negative rrf constant", query: "query", candidates: []Candidate{valid}, options: Options{RRFConstant: -1, Limit: 1}},
		{name: "zero limit", query: "query", candidates: []Candidate{valid}, options: Options{RRFConstant: 60}},
		{name: "limit above maximum", query: "query", candidates: []Candidate{valid}, options: Options{RRFConstant: 60, Limit: MaxResults + 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Rank(tt.query, tt.candidates, tt.options)
			if err == nil {
				t.Fatal("Rank() error = nil")
			}
			message := err.Error()
			for _, secret := range []string{tt.query, "text", "value", " id ", " heading "} {
				if strings.TrimSpace(secret) != "" && strings.Contains(message, secret) {
					t.Fatalf("safe error %q echoed input %q", message, secret)
				}
			}
		})
	}
}

func TestRankCandidateCapAndLimit(t *testing.T) {
	candidates := make([]Candidate, MaxCandidates)
	for i := range candidates {
		candidates[i] = candidate(normalizedNumericID(i), 1-float64(i)/MaxCandidates, "text")
	}
	if got := rank(t, "text", candidates, MaxResults); len(got) != MaxResults {
		t.Fatalf("result count = %d, want %d", len(got), MaxResults)
	}

	tooMany := append(append([]Candidate(nil), candidates...), candidate("overflow", 0, "text"))
	if _, err := Rank("text", tooMany, Options{RRFConstant: 60, Limit: MaxResults}); err == nil {
		t.Fatal("Rank() accepted candidates above cap")
	}
}

func TestRankRRFConstantBounds(t *testing.T) {
	valid := []Candidate{candidate("id", 0.5, "text")}
	if _, err := Rank("text", valid, Options{RRFConstant: MaxRRFConstant, Limit: 1}); err != nil {
		t.Fatalf("Rank() rejected MaxRRFConstant: %v", err)
	}
	if _, err := Rank("text", valid, Options{RRFConstant: MaxRRFConstant + 1, Limit: 1}); err == nil {
		t.Fatal("Rank() accepted RRF constant above maximum")
	} else if strings.Contains(err.Error(), "text") {
		t.Fatalf("safe error echoed input: %v", err)
	}
}

func TestRankResultsOwnCandidateFields(t *testing.T) {
	input := []Candidate{candidate("id", 0.5, "original", Field{Name: "path", Value: "docs/original"})}
	got := rank(t, "original", input, 1)

	input[0].Fields[0].Name = "changed-input-name"
	input[0].Fields[0].Value = "changed input"
	if got[0].Candidate.Fields[0].Name != "text" || got[0].Candidate.Fields[0].Value != "original" {
		t.Fatalf("caller mutation changed result fields: %+v", got[0].Candidate.Fields)
	}

	got[0].Candidate.Fields[1].Name = "changed-result-name"
	got[0].Candidate.Fields[1].Value = "changed result"
	if input[0].Fields[1].Name != "path" || input[0].Fields[1].Value != "docs/original" {
		t.Fatalf("result mutation changed caller fields: %+v", input[0].Fields)
	}
}

func TestRankTreatsIDsAsOpaqueAndAllowsNamedFieldCase(t *testing.T) {
	got := rank(t, "title", []Candidate{
		candidate("ABC-123", 0.5, "body", Field{Name: "Heading", Value: "TITLE"}),
	}, 1)
	if got[0].Candidate.ID != "ABC-123" {
		t.Fatalf("candidate ID changed to %q", got[0].Candidate.ID)
	}
	if !got[0].Lexical.HasSignal {
		t.Fatal("mixed-case named field was not scored")
	}
}

func TestRankKeepsExplicitDenseOnlyCandidate(t *testing.T) {
	got, err := Rank("PM-1427", []Candidate{
		{ID: "dense", DenseScore: .9, DenseOnly: true},
		candidate("lexical", .2, "PM-1427"),
	}, Options{RRFConstant: 60, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Candidate.ID != "lexical" ||
		got[1].Candidate.ID != "dense" || got[1].LexicalRank != 0 {
		t.Fatalf("dense-only ranking = %+v", got)
	}
}

func TestRankAllReturnsEntireBoundedPoolWithoutChangingRankLimit(t *testing.T) {
	candidates := make([]Candidate, MaxCandidates)
	for i := range candidates {
		candidates[i] = candidate(normalizedNumericID(i), 1-float64(i)/MaxCandidates, "text")
	}
	all, err := RankAll("text", candidates, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != MaxCandidates {
		t.Fatalf("RankAll() result count = %d, want %d", len(all), MaxCandidates)
	}
	limited, err := Rank("text", candidates, Options{
		RRFConstant: 60, Limit: MaxResults,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all[:MaxResults], limited) {
		t.Fatal("RankAll() ordering or diagnostics differ from Rank()")
	}
	if _, err := Rank("text", candidates, Options{
		RRFConstant: 60, Limit: MaxResults + 1,
	}); err == nil {
		t.Fatal("Rank() accepted output above MaxResults")
	}
	tooMany := append(append([]Candidate(nil), candidates...),
		candidate("overflow", 0, "text"))
	if _, err := RankAll("text", tooMany, 60); err == nil {
		t.Fatal("RankAll() accepted candidates above MaxCandidates")
	}
}

func normalizedNumericID(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var reversed [20]byte
	n := 0
	for i > 0 {
		reversed[n] = digits[i%10]
		i /= 10
		n++
	}
	result := make([]byte, n)
	for j := range result {
		result[j] = reversed[n-1-j]
	}
	return string(result)
}
