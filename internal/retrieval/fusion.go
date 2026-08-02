package retrieval

import (
	"errors"
	"math"
	"slices"
	"strings"
)

const (
	// MaxCandidates is the hard candidate bound: current maximum output (100)
	// multiplied by the retrieval candidate factor (4).
	MaxCandidates = 400

	// MaxResults is the maximum accepted output limit.
	MaxResults = 100

	// MaxRRFConstant is a conservative operational bound that keeps every
	// denominator from RRFConstant+1 through RRFConstant+MaxCandidates exactly
	// representable and meaningfully separated in float64.
	MaxRRFConstant = 1_000_000
)

var (
	errInvalidQuery      = errors.New("retrieval: invalid query")
	errInvalidCandidates = errors.New("retrieval: invalid candidates")
	errInvalidCandidate  = errors.New("retrieval: invalid candidate")
	errInvalidOptions    = errors.New("retrieval: invalid options")
)

// Candidate is a bounded dense-search candidate with named fields used for
// local lexical matching. ID is an opaque stable tie-break; it must be unique,
// non-empty, and free of surrounding whitespace.
type Candidate struct {
	ID         string
	DenseScore float64
	Fields     []Field
}

// Options controls deterministic fusion. RRFConstant is intentionally
// required rather than supplied as a hidden package default and must be in
// the inclusive range [1, MaxRRFConstant].
type Options struct {
	RRFConstant int
	Limit       int
}

// Result preserves the original candidate and dense cosine score while adding
// ranks, the RRF score, and explainable lexical diagnostics. LexicalRank is
// zero when the candidate has no lexical signal.
type Result struct {
	Candidate   Candidate
	DenseRank   int
	LexicalRank int
	RRFScore    float64
	Lexical     LexicalDiagnostics
}

type rankedCandidate struct {
	candidate   Candidate
	denseRank   int
	lexicalRank int
	fusedScore  float64
	lexical     lexicalScore
}

// Rank validates, lexically scores, and fuses bounded candidates. Input order
// has no effect: dense ties and final ties are resolved by opaque ID.
func Rank(rawQuery string, candidates []Candidate, options Options) ([]Result, error) {
	normalizedQuery := Normalize(rawQuery)
	if normalizedQuery == "" {
		return nil, errInvalidQuery
	}
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	if err := validateCandidates(candidates); err != nil {
		return nil, err
	}

	ranked := make([]rankedCandidate, len(candidates))
	for index, candidate := range candidates {
		ranked[index] = rankedCandidate{candidate: candidate}
	}
	slices.SortFunc(ranked, func(left, right rankedCandidate) int {
		if left.candidate.DenseScore > right.candidate.DenseScore {
			return -1
		}
		if left.candidate.DenseScore < right.candidate.DenseScore {
			return 1
		}
		return compareID(left.candidate.ID, right.candidate.ID)
	})
	for index := range ranked {
		if index == 0 || ranked[index].candidate.DenseScore != ranked[index-1].candidate.DenseScore {
			ranked[index].denseRank = index + 1
			continue
		}
		ranked[index].denseRank = ranked[index-1].denseRank
	}

	queryTokens := tokenSet(normalizedQuery)
	queryIdentifiers := identifierLexemes(normalizedQuery)
	for index := range ranked {
		ranked[index].lexical = scoreLexical(
			normalizedQuery,
			queryTokens,
			queryIdentifiers,
			ranked[index].candidate.Fields,
		)
	}
	assignLexicalRanks(ranked)
	for index := range ranked {
		ranked[index].fusedScore = rrfTerm(ranked[index].denseRank, options.RRFConstant)
		if ranked[index].lexicalRank > 0 {
			ranked[index].fusedScore += rrfTerm(ranked[index].lexicalRank, options.RRFConstant)
		}
	}
	sortFused(ranked)

	limit := min(options.Limit, len(ranked))
	results := make([]Result, limit)
	for index := range results {
		results[index] = Result{
			Candidate:   cloneCandidate(ranked[index].candidate),
			DenseRank:   ranked[index].denseRank,
			LexicalRank: ranked[index].lexicalRank,
			RRFScore:    ranked[index].fusedScore,
			Lexical:     ranked[index].lexical.diagnostics,
		}
	}
	return results, nil
}

func validateOptions(options Options) error {
	if options.RRFConstant <= 0 ||
		options.RRFConstant > MaxRRFConstant ||
		options.Limit <= 0 ||
		options.Limit > MaxResults {
		return errInvalidOptions
	}
	return nil
}

func cloneCandidate(candidate Candidate) Candidate {
	cloned := candidate
	cloned.Fields = slices.Clone(candidate.Fields)
	return cloned
}

func validateCandidates(candidates []Candidate) error {
	if len(candidates) == 0 || len(candidates) > MaxCandidates {
		return errInvalidCandidates
	}
	ids := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.ID == "" || candidate.ID != strings.TrimSpace(candidate.ID) {
			return errInvalidCandidate
		}
		if _, exists := ids[candidate.ID]; exists {
			return errInvalidCandidates
		}
		ids[candidate.ID] = struct{}{}
		if math.IsNaN(candidate.DenseScore) || math.IsInf(candidate.DenseScore, 0) {
			return errInvalidCandidate
		}
		if err := validateFields(candidate.Fields); err != nil {
			return err
		}
	}
	return nil
}

func validateFields(fields []Field) error {
	if len(fields) == 0 {
		return errInvalidCandidate
	}
	names := make(map[string]struct{}, len(fields))
	hasText := false
	for _, field := range fields {
		if field.Name == "" || field.Name != strings.TrimSpace(field.Name) {
			return errInvalidCandidate
		}
		if _, exists := names[field.Name]; exists {
			return errInvalidCandidate
		}
		names[field.Name] = struct{}{}
		if field.Name == "text" {
			hasText = Normalize(field.Value) != ""
		}
	}
	if !hasText {
		return errInvalidCandidate
	}
	return nil
}

func assignLexicalRanks(ranked []rankedCandidate) {
	positive := make([]int, 0, len(ranked))
	for index := range ranked {
		if ranked[index].lexical.diagnostics.HasSignal {
			positive = append(positive, index)
		}
	}
	slices.SortFunc(positive, func(leftIndex, rightIndex int) int {
		comparison := compareLexical(ranked[leftIndex].lexical, ranked[rightIndex].lexical)
		if comparison != 0 {
			return comparison
		}
		if ranked[leftIndex].denseRank != ranked[rightIndex].denseRank {
			return ranked[leftIndex].denseRank - ranked[rightIndex].denseRank
		}
		return compareID(ranked[leftIndex].candidate.ID, ranked[rightIndex].candidate.ID)
	})
	for position, rankedIndex := range positive {
		if position == 0 || compareLexical(ranked[positive[position-1]].lexical, ranked[rankedIndex].lexical) != 0 {
			ranked[rankedIndex].lexicalRank = position + 1
			continue
		}
		ranked[rankedIndex].lexicalRank = ranked[positive[position-1]].lexicalRank
	}
}

func sortFused(ranked []rankedCandidate) {
	slices.SortFunc(ranked, func(left, right rankedCandidate) int {
		if left.fusedScore > right.fusedScore {
			return -1
		}
		if left.fusedScore < right.fusedScore {
			return 1
		}
		if left.denseRank != right.denseRank {
			return left.denseRank - right.denseRank
		}
		return compareID(left.candidate.ID, right.candidate.ID)
	})
}

func compareID(left, right string) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func rrfTerm(rank, constant int) float64 {
	return 1 / (float64(constant) + float64(rank))
}
