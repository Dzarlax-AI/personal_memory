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
