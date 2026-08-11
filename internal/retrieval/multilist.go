package retrieval

import (
	"errors"
	"slices"
	"strings"
)

const (
	// MaxRankedSources bounds the number of independent ranked lists that can
	// participate in one fusion operation.
	MaxRankedSources = 8
)

var errInvalidRankedLists = errors.New("retrieval: invalid ranked lists")

// RankedList is a named, already-ranked list of opaque candidate IDs. IDs are
// ordered best-first and must be unique within the list.
type RankedList struct {
	Name string
	IDs  []string
}

// MultiListOptions controls deterministic reciprocal-rank fusion. FlatSource
// enables the flat-top rescue invariant when it names one of the input lists.
type MultiListOptions struct {
	RRFConstant int
	Limit       int
	FlatSource  string
}

// SourceRank explains one candidate's membership and one-based rank in a
// named source list.
type SourceRank struct {
	Source string `json:"source"`
	Rank   int    `json:"rank"`
}

// FusedResult is a storage-agnostic fused candidate. FlatRescued is true only
// when the flat-top invariant replaced the natural last top-K result.
type FusedResult struct {
	ID          string
	RRFScore    float64
	Sources     []SourceRank
	FlatRescued bool
}

// FusionDiagnostics records bounded routing policy decisions without
// carrying candidate contents or backend payloads.
type FusionDiagnostics struct {
	FlatRescueApplied bool
}

type accumulatedCandidate struct {
	ID      string
	Score   float64
	Sources []SourceRank
}

// FuseRankedLists performs deterministic reciprocal-rank fusion over bounded,
// already-ranked named sources. Candidate IDs are deduplicated across lists;
// equal fused scores are ordered by ID.
func FuseRankedLists(lists []RankedList, options MultiListOptions) ([]FusedResult, FusionDiagnostics, error) {
	if err := validateRankedLists(lists, options); err != nil {
		return nil, FusionDiagnostics{}, err
	}

	ordered := slices.Clone(lists)
	slices.SortFunc(ordered, func(left, right RankedList) int {
		return compareID(left.Name, right.Name)
	})

	byID := make(map[string]*accumulatedCandidate)
	for _, list := range ordered {
		for index, id := range list.IDs {
			candidate := byID[id]
			if candidate == nil {
				candidate = &accumulatedCandidate{ID: id}
				byID[id] = candidate
			}
			rank := index + 1
			candidate.Score += rrfTerm(rank, options.RRFConstant)
			candidate.Sources = append(candidate.Sources, SourceRank{Source: list.Name, Rank: rank})
		}
	}

	all := make([]FusedResult, 0, len(byID))
	for _, candidate := range byID {
		all = append(all, FusedResult{
			ID:       candidate.ID,
			RRFScore: candidate.Score,
			Sources:  slices.Clone(candidate.Sources),
		})
	}
	slices.SortFunc(all, func(left, right FusedResult) int {
		if left.RRFScore > right.RRFScore {
			return -1
		}
		if left.RRFScore < right.RRFScore {
			return 1
		}
		return compareID(left.ID, right.ID)
	})

	resultCount := min(options.Limit, len(all))
	results := append([]FusedResult(nil), all[:resultCount]...)
	diagnostics := FusionDiagnostics{}
	if options.FlatSource == "" || len(all) <= options.Limit {
		return results, diagnostics, nil
	}

	flatTop := flatTopID(ordered, options.FlatSource)
	for _, result := range results {
		if result.ID == flatTop {
			return results, diagnostics, nil
		}
	}
	for _, candidate := range all[options.Limit:] {
		if candidate.ID == flatTop {
			candidate.FlatRescued = true
			results[len(results)-1] = candidate
			diagnostics.FlatRescueApplied = true
			break
		}
	}
	return results, diagnostics, nil
}

func validateRankedLists(lists []RankedList, options MultiListOptions) error {
	if len(lists) == 0 || len(lists) > MaxRankedSources ||
		validateRRFConstant(options.RRFConstant) != nil ||
		options.Limit <= 0 || options.Limit > MaxResults {
		return errInvalidRankedLists
	}

	names := make(map[string]struct{}, len(lists))
	uniqueCandidates := make(map[string]struct{})
	for _, list := range lists {
		if list.Name == "" || list.Name != strings.TrimSpace(list.Name) || len(list.IDs) == 0 || len(list.IDs) > MaxCandidates {
			return errInvalidRankedLists
		}
		if _, exists := names[list.Name]; exists {
			return errInvalidRankedLists
		}
		names[list.Name] = struct{}{}
		listIDs := make(map[string]struct{}, len(list.IDs))
		for _, id := range list.IDs {
			if id == "" || id != strings.TrimSpace(id) {
				return errInvalidRankedLists
			}
			if _, exists := listIDs[id]; exists {
				return errInvalidRankedLists
			}
			listIDs[id] = struct{}{}
			uniqueCandidates[id] = struct{}{}
		}
	}
	if len(uniqueCandidates) > MaxCandidates {
		return errInvalidRankedLists
	}
	if options.FlatSource != "" {
		if options.FlatSource != strings.TrimSpace(options.FlatSource) {
			return errInvalidRankedLists
		}
		if _, exists := names[options.FlatSource]; !exists {
			return errInvalidRankedLists
		}
	}
	return nil
}

func flatTopID(lists []RankedList, flatSource string) string {
	for _, list := range lists {
		if list.Name == flatSource {
			return list.IDs[0]
		}
	}
	return ""
}
