package retrieval

import (
	"reflect"
	"testing"
)

func TestNormalize(t *testing.T) {
	if got, want := Normalize("  ПАМЯТЬ\u2003Project\tNAME  "), "память project name"; got != want {
		t.Fatalf("Normalize() = %q, want %q", got, want)
	}
}

func TestTokenizeUnicodeLettersAndDigits(t *testing.T) {
	got := tokenize("Память-v2/Project_42: café")
	want := []string{"память", "v2", "project", "42", "café"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokenize() = %v, want %v", got, want)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !containsExactPhrase(Normalize(tt.field), Normalize(tt.query)) {
				t.Fatalf("containsExactPhrase() rejected prose-bounded phrase")
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
