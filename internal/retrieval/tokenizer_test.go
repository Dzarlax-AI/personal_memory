package retrieval

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	if got, want := Normalize("  ПАМЯТЬ\u2003Project\tNAME  "), "память project name"; got != want {
		t.Fatalf("Normalize() = %q, want %q", got, want)
	}
}

func TestNormalizeCanonicalEquivalenceAndCaseFold(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		want  string
	}{
		{name: "nfc and nfd", left: "CAFÉ", right: "cafe\u0301", want: "café"},
		{name: "greek sigma forms", left: "ΟΣ", right: "ο\u03c2", want: "οσ"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Normalize(tt.left); got != tt.want {
				t.Fatalf("Normalize(left) = %q, want %q", got, tt.want)
			}
			if got := Normalize(tt.right); got != tt.want {
				t.Fatalf("Normalize(right) = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTokenizeUnicodeLettersAndDigits(t *testing.T) {
	got := tokenize("Память-v2/Project_42: café")
	want := []string{"память", "v2", "project", "42", "café"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokenize() = %v, want %v", got, want)
	}
}

func TestTokenizeKeepsCombiningMarksAttached(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{name: "decomposed latin", value: "cafe\u0301", want: []string{"café"}},
		{name: "mark heavy script", value: "हिंदी भाषा", want: []string{"हिंदी", "भाषा"}},
		{name: "non composing mark", value: "x\u20dd", want: []string{"x\u20dd"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tokenize(tt.value); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("tokenize() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIdentifierLexemes(t *testing.T) {
	got := identifierLexemes("open projects/personal-memory/README.md and 550e8400-e29b-41d4-a716-446655440000")
	for _, want := range []string{
		"projects/personal-memory/readme.md",
		"personal-memory",
		"readme.md",
		"550e8400-e29b-41d4-a716-446655440000",
	} {
		if _, ok := got[want]; !ok {
			t.Fatalf("identifierLexemes() missing %q: %v", want, got)
		}
	}

	withMark := identifierLexemes("docs/x\u20dd-file/readme")
	if _, ok := withMark["x\u20dd-file"]; !ok {
		t.Fatalf("identifierLexemes() detached combining mark: %v", withMark)
	}
}

func TestExactPhraseRejectsIdentifierAndPathSubstringBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		query string
		field string
	}{
		{name: "kebab prefix", query: "personal-memory", field: "personal-memory-service"},
		{name: "kebab suffix", query: "personal-memory", field: "my-personal-memory"},
		{name: "uuid prefix", query: "550e8400-e29b-41d4-a716-446655440000", field: "550e8400-e29b-41d4-a716-446655440000-extra"},
		{name: "path prefix", query: "docs/project", field: "docs/project/readme"},
		{name: "dotted right continuation", query: "config", field: "config.prod"},
		{name: "dotted left continuation", query: "config", field: "prod.config"},
		{name: "connector run right continuation", query: "config", field: "config.-_prod"},
		{name: "connector run left continuation", query: "config", field: "prod_-.config"},
		{name: "dotted name", query: "config.prod", field: "config.prod.json"},
		{name: "underscore suffix", query: "memory", field: "memory_store"},
		{name: "colon suffix", query: "memory", field: "memory:service"},
		{name: "at prefix", query: "memory", field: "user@memory"},
		{name: "backslash path", query: `docs\project`, field: `docs\project\readme`},
		{name: "unicode letter prefix", query: "memory", field: "яmemory"},
		{name: "unicode letter suffix", query: "память", field: "памятью"},
		{name: "digit suffix", query: "memory", field: "memory2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if containsExactPhrase(Normalize(tt.field), Normalize(tt.query)) {
				t.Fatalf("containsExactPhrase() promoted identifier substring")
			}

			got := rank(t, tt.query, []Candidate{candidate("id", 0.5, tt.field)}, 1)
			if got[0].Lexical.ExactPhrase {
				t.Fatal("Rank() diagnosed identifier substring as exact phrase")
			}
		})
	}
}

func TestExactPhraseAcceptsProseBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		query string
		field string
	}{
		{name: "start and end", query: "personal-memory", field: "personal-memory"},
		{name: "comma", query: "personal-memory", field: "Use personal-memory, then continue."},
		{name: "whitespace", query: "personal-memory", field: "Use personal-memory here"},
		{name: "parentheses", query: "personal-memory", field: "(personal-memory)"},
		{name: "unicode prose punctuation", query: "память", field: "«память» — важна"},
		{name: "trailing period", query: "release checklist", field: "Release checklist."},
		{name: "colon before whitespace", query: "release checklist", field: "Release checklist: done"},
		{name: "leading period", query: "release checklist", field: ".Release checklist"},
		{name: "leading colon", query: "release checklist", field: ":Release checklist"},
		{name: "leading colon after whitespace", query: "release checklist", field: "done: Release checklist"},
		{name: "connector run at end", query: "release checklist", field: "Release checklist..."},
		{name: "connector next to prose punctuation", query: "release checklist", field: "Release checklist., then continue"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !containsExactPhrase(Normalize(tt.field), Normalize(tt.query)) {
				t.Fatalf("containsExactPhrase() rejected prose-bounded phrase")
			}

			got := rank(t, tt.query, []Candidate{candidate("id", 0.5, tt.field)}, 1)
			if !got[0].Lexical.ExactPhrase {
				t.Fatal("Rank() did not diagnose prose-bounded exact phrase")
			}
		})
	}
}

func TestIdentifierPrefixDoesNotGainStrongestPhraseSignal(t *testing.T) {
	got := rank(t, "personal-memory", []Candidate{
		candidate("prefix", 0.9, "personal-memory-service"),
		candidate("exact", 0.8, "project personal-memory"),
	}, 2)

	byID := make(map[string]Result, len(got))
	for _, result := range got {
		byID[result.Candidate.ID] = result
	}
	if byID["prefix"].Lexical.ExactPhrase {
		t.Fatal("identifier prefix received exact-phrase promotion")
	}
	if byID["prefix"].Lexical.IdentifierMatches != 0 {
		t.Fatalf("identifier prefix matches = %d, want 0", byID["prefix"].Lexical.IdentifierMatches)
	}
	if !byID["exact"].Lexical.ExactPhrase || byID["exact"].Lexical.IdentifierMatches != 1 {
		t.Fatalf("exact identifier diagnostics = %+v", byID["exact"].Lexical)
	}
}

func TestCanonicalEquivalentPathsAndGreekSigmaMatch(t *testing.T) {
	t.Run("macos decomposed path", func(t *testing.T) {
		got := rank(t, "docs/café/menu", []Candidate{
			candidate("id", 0.5, "document", Field{Name: "path", Value: "docs/cafe\u0301/menu"}),
		}, 1)
		if got[0].Lexical.IdentifierMatches != 1 {
			t.Fatalf("identifier matches = %d, want 1", got[0].Lexical.IdentifierMatches)
		}
	})

	t.Run("greek sigma", func(t *testing.T) {
		got := rank(t, "ΟΣ", []Candidate{candidate("id", 0.5, "ος")}, 1)
		if !got[0].Lexical.ExactPhrase || got[0].Lexical.TokenCoverage != 1 {
			t.Fatalf("Greek fold diagnostics = %+v", got[0].Lexical)
		}
	})
}

func TestCombiningMarkContinuesTokenAtPhraseBoundary(t *testing.T) {
	tests := []struct {
		name  string
		query string
		field string
	}{
		{name: "nfd acute", query: "e", field: "e\u0301"},
		{name: "non composing mark", query: "x", field: "x\u20dd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rank(t, tt.query, []Candidate{candidate("id", 0.5, tt.field)}, 1)
			if got[0].Lexical.ExactPhrase {
				t.Fatal("base letter received exact phrase across combining mark")
			}
		})
	}
}

func TestConnectorOnlyQueryUsesDenseOnlyFallback(t *testing.T) {
	got := rank(t, "...---///", []Candidate{
		candidate("lower", 0.5, "...---///"),
		candidate("higher", 0.9, "semantic candidate"),
	}, 2)
	if want := []string{"higher", "lower"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("ids = %v, want %v", ids(got), want)
	}
	for _, result := range got {
		if result.Lexical.HasSignal || result.Lexical.ExactPhrase || result.LexicalRank != 0 {
			t.Fatalf("connector-only query produced lexical signal: %+v", result)
		}
	}
}

func TestLongConnectorRunsRemainContextSensitive(t *testing.T) {
	const connectorCount = 1 << 20
	connectors := strings.Repeat("-._/:@\\", connectorCount/7)

	if containsExactPhrase(Normalize("x"+connectors+"y"), Normalize("x")) {
		t.Fatal("connector run leading to identifier continuation was accepted")
	}
	if !containsExactPhrase(Normalize("x"+connectors+" "), Normalize("x")) {
		t.Fatal("connector run leading to whitespace was rejected")
	}
	if containsExactPhrase(Normalize("y"+connectors+"x"), Normalize("x")) {
		t.Fatal("left connector run leading to identifier continuation was accepted")
	}
	if !containsExactPhrase(Normalize(" "+connectors+"x"), Normalize("x")) {
		t.Fatal("left connector run leading to whitespace was rejected")
	}
}
