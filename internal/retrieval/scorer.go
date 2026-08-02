package retrieval

// Field is a named candidate field considered for lexical matching. Every
// candidate must include a non-empty "text" field. Callers may also supply
// fields such as "heading" and "path".
type Field struct {
	Name  string
	Value string
}

// LexicalFieldDiagnostics describes the independent signals found in one
// candidate field.
type LexicalFieldDiagnostics struct {
	Name              string
	ExactPhrase       bool
	MatchedTokens     int
	IdentifierMatches int
}

// LexicalDiagnostics describes lexical evidence. TokenCoverage is the
// fraction of distinct normalized query tokens matched across all fields; it
// is an explainability measure, not a probability.
type LexicalDiagnostics struct {
	HasSignal         bool
	ExactPhrase       bool
	MatchedTokens     int
	TotalTokens       int
	TokenCoverage     float64
	IdentifierMatches int
	Fields            []LexicalFieldDiagnostics
}

type lexicalScore struct {
	diagnostics LexicalDiagnostics
}

func scoreLexical(normalizedQuery string, queryTokens, queryIdentifiers map[string]struct{}, fields []Field) lexicalScore {
	matchedTokens := make(map[string]struct{})
	matchedIdentifiers := make(map[string]struct{})
	diagnostics := LexicalDiagnostics{
		TotalTokens: len(queryTokens),
		Fields:      make([]LexicalFieldDiagnostics, 0, len(fields)),
	}

	for _, field := range fields {
		normalizedField := Normalize(field.Value)
		fieldTokens := tokenSet(normalizedField)
		fieldIdentifiers := identifierLexemes(normalizedField)
		fieldDiagnostics := LexicalFieldDiagnostics{
			Name: field.Name,
		}
		if len(queryTokens) > 0 || len(queryIdentifiers) > 0 {
			fieldDiagnostics.ExactPhrase = containsExactPhrase(normalizedField, normalizedQuery)
		}

		for queryToken := range queryTokens {
			if _, ok := fieldTokens[queryToken]; ok {
				fieldDiagnostics.MatchedTokens++
				matchedTokens[queryToken] = struct{}{}
			}
		}
		for queryIdentifier := range queryIdentifiers {
			if _, ok := fieldIdentifiers[queryIdentifier]; ok {
				fieldDiagnostics.IdentifierMatches++
				matchedIdentifiers[queryIdentifier] = struct{}{}
			}
		}

		diagnostics.ExactPhrase = diagnostics.ExactPhrase || fieldDiagnostics.ExactPhrase
		diagnostics.Fields = append(diagnostics.Fields, fieldDiagnostics)
	}

	diagnostics.MatchedTokens = len(matchedTokens)
	diagnostics.IdentifierMatches = len(matchedIdentifiers)
	if diagnostics.TotalTokens > 0 {
		diagnostics.TokenCoverage = float64(diagnostics.MatchedTokens) / float64(diagnostics.TotalTokens)
	}
	diagnostics.HasSignal = diagnostics.ExactPhrase ||
		diagnostics.MatchedTokens > 0 ||
		diagnostics.IdentifierMatches > 0
	return lexicalScore{diagnostics: diagnostics}
}

// compareLexical defines the complete, explainable lexical ordering. It uses
// no hidden weighted sum: exact phrase wins first, followed by the number of
// exact identifier/path-like lexemes, then distinct query-token coverage.
func compareLexical(left, right lexicalScore) int {
	if left.diagnostics.ExactPhrase != right.diagnostics.ExactPhrase {
		if left.diagnostics.ExactPhrase {
			return -1
		}
		return 1
	}
	if left.diagnostics.IdentifierMatches != right.diagnostics.IdentifierMatches {
		if left.diagnostics.IdentifierMatches > right.diagnostics.IdentifierMatches {
			return -1
		}
		return 1
	}
	if left.diagnostics.MatchedTokens != right.diagnostics.MatchedTokens {
		if left.diagnostics.MatchedTokens > right.diagnostics.MatchedTokens {
			return -1
		}
		return 1
	}
	return 0
}
