package retrieval

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Normalize applies deterministic Unicode lower-casing and collapses every
// whitespace run to one ASCII space.
func Normalize(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

func tokenize(value string) []string {
	normalized := Normalize(value)
	tokens := make([]string, 0)
	var token strings.Builder
	flush := func() {
		if token.Len() == 0 {
			return
		}
		tokens = append(tokens, token.String())
		token.Reset()
	}
	for _, current := range normalized {
		if unicode.IsLetter(current) || unicode.IsDigit(current) {
			token.WriteRune(current)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

func tokenSet(value string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, token := range tokenize(value) {
		result[token] = struct{}{}
	}
	return result
}

func identifierLexemes(value string) map[string]struct{} {
	normalized := Normalize(value)
	result := make(map[string]struct{})
	var lexeme strings.Builder

	add := func(raw string) {
		raw = strings.TrimFunc(raw, isIdentifierSeparator)
		if raw == "" || !strings.ContainsFunc(raw, isIdentifierSeparator) {
			return
		}
		result[raw] = struct{}{}

		for _, part := range strings.FieldsFunc(raw, isPathSeparator) {
			part = strings.TrimFunc(part, isIdentifierSeparator)
			if part != "" && strings.ContainsFunc(part, isIdentifierSeparator) {
				result[part] = struct{}{}
			}
		}
	}
	flush := func() {
		if lexeme.Len() == 0 {
			return
		}
		add(lexeme.String())
		lexeme.Reset()
	}

	for _, current := range normalized {
		if unicode.IsLetter(current) || unicode.IsDigit(current) || isIdentifierSeparator(current) {
			lexeme.WriteRune(current)
			continue
		}
		flush()
	}
	flush()
	return result
}

func isIdentifierSeparator(current rune) bool {
	switch current {
	case '-', '_', '.', '/', '\\', ':', '@':
		return true
	default:
		return false
	}
}

func isPathSeparator(current rune) bool {
	switch current {
	case '/', '\\', ':':
		return true
	default:
		return false
	}
}

func containsExactPhrase(normalizedField, normalizedQuery string) bool {
	if normalizedQuery == "" {
		return false
	}
	start := 0
	for {
		offset := strings.Index(normalizedField[start:], normalizedQuery)
		if offset < 0 {
			return false
		}
		offset += start
		end := offset + len(normalizedQuery)
		if phraseBoundary(normalizedField, offset, true) && phraseBoundary(normalizedField, end, false) {
			return true
		}
		start = offset + 1
	}
}

func phraseBoundary(value string, byteIndex int, before bool) bool {
	if byteIndex == 0 || byteIndex == len(value) {
		return true
	}
	if before {
		current, _ := runeBefore(value, byteIndex)
		return !unicode.IsLetter(current) && !unicode.IsDigit(current)
	}
	current, _ := runeAt(value, byteIndex)
	return !unicode.IsLetter(current) && !unicode.IsDigit(current)
}

func runeBefore(value string, byteIndex int) (rune, int) {
	return utf8.DecodeLastRuneInString(value[:byteIndex])
}

func runeAt(value string, byteIndex int) (rune, int) {
	return utf8.DecodeRuneInString(value[byteIndex:])
}
