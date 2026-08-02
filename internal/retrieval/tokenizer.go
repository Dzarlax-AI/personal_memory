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
		raw = strings.TrimFunc(raw, isIdentifierConnector)
		if raw == "" || !strings.ContainsFunc(raw, isIdentifierConnector) {
			return
		}
		result[raw] = struct{}{}

		for _, part := range strings.FieldsFunc(raw, isPathSeparator) {
			part = strings.TrimFunc(part, isIdentifierConnector)
			if part != "" && strings.ContainsFunc(part, isIdentifierConnector) {
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
		if unicode.IsLetter(current) || unicode.IsDigit(current) || isIdentifierConnector(current) {
			lexeme.WriteRune(current)
			continue
		}
		flush()
	}
	flush()
	return result
}

// isIdentifierConnector is the shared contract for lexeme extraction and
// phrase boundaries: adjacent connectors keep text in the same identifier.
func isIdentifierConnector(current rune) bool {
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
		return !hasIdentifierContinuation(value, byteIndex, scanLeft)
	}
	return !hasIdentifierContinuation(value, byteIndex, scanRight)
}

type scanDirection int

const (
	scanLeft  scanDirection = -1
	scanRight scanDirection = 1
)

// hasIdentifierContinuation scans outward from a phrase edge. A letter or
// digit directly adjacent to the phrase always continues the identifier.
// Connector runs do so only when they lead to an outer letter or digit;
// reaching whitespace, prose punctuation, or the string edge is a boundary.
func hasIdentifierContinuation(value string, byteIndex int, direction scanDirection) bool {
	cursor := byteIndex
	for cursor >= 0 && cursor <= len(value) {
		var (
			current rune
			size    int
		)
		if direction == scanLeft {
			if cursor == 0 {
				return false
			}
			current, size = runeBefore(value, cursor)
			cursor -= size
		} else {
			if cursor == len(value) {
				return false
			}
			current, size = runeAt(value, cursor)
			cursor += size
		}

		if unicode.IsLetter(current) || unicode.IsDigit(current) {
			return true
		}
		if !isIdentifierConnector(current) {
			return false
		}
	}
	return false
}

func runeBefore(value string, byteIndex int) (rune, int) {
	return utf8.DecodeLastRuneInString(value[:byteIndex])
}

func runeAt(value string, byteIndex int) (rune, int) {
	return utf8.DecodeRuneInString(value[byteIndex:])
}
