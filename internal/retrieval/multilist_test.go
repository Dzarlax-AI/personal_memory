package retrieval

import (
	"reflect"
	"strconv"
	"testing"
)

func rankedList(name string, ids ...string) RankedList {
	return RankedList{Name: name, IDs: ids}
}

func fusedIDs(results []FusedResult) []string {
	ids := make([]string, len(results))
	for i := range results {
		ids[i] = results[i].ID
	}
	return ids
}

func TestFuseRankedListsDeduplicatesAndExplainsMembership(t *testing.T) {
	results, diagnostics, err := FuseRankedLists([]RankedList{
		rankedList("flat", "shared", "flat-only"),
		rankedList("filtered", "shared", "filtered-only"),
	}, MultiListOptions{RRFConstant: 60, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics.FlatRescueApplied {
		t.Fatal("unexpected flat rescue")
	}
	if want := []string{"shared", "filtered-only", "flat-only"}; !reflect.DeepEqual(fusedIDs(results), want) {
		t.Fatalf("ids = %v, want %v", fusedIDs(results), want)
	}
	if want := []SourceRank{{Source: "filtered", Rank: 1}, {Source: "flat", Rank: 1}}; !reflect.DeepEqual(results[0].Sources, want) {
		t.Fatalf("shared sources = %#v, want %#v", results[0].Sources, want)
	}
}

func TestFuseRankedListsIsIndependentOfNamedSourceInputOrderAndBreaksTiesByID(t *testing.T) {
	options := MultiListOptions{RRFConstant: 60, Limit: 4}
	left, _, err := FuseRankedLists([]RankedList{
		rankedList("z-source", "b", "d"),
		rankedList("a-source", "a", "c"),
	}, options)
	if err != nil {
		t.Fatal(err)
	}
	right, _, err := FuseRankedLists([]RankedList{
		rankedList("a-source", "a", "c"),
		rankedList("z-source", "b", "d"),
	}, options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("source permutation changed results:\nleft=%#v\nright=%#v", left, right)
	}
	if want := []string{"a", "b", "c", "d"}; !reflect.DeepEqual(fusedIDs(left), want) {
		t.Fatalf("tie order = %v, want %v", fusedIDs(left), want)
	}
}

func TestFuseRankedListsFlatRescue(t *testing.T) {
	tests := []struct {
		name            string
		lists           []RankedList
		limit           int
		wantIDs         []string
		wantRescue      bool
		wantLastRescued bool
	}{
		{
			name: "replaces last result",
			lists: []RankedList{
				rankedList("filtered", "a", "b", "c"),
				rankedList("flat", "flat-top", "a", "b"),
			},
			limit: 2, wantIDs: []string{"a", "flat-top"}, wantRescue: true, wantLastRescued: true,
		},
		{
			name: "flat top already selected",
			lists: []RankedList{
				rankedList("filtered", "a", "b"),
				rankedList("flat", "a", "c"),
			},
			limit: 2, wantIDs: []string{"a", "b"},
		},
		{
			name: "output has room",
			lists: []RankedList{
				rankedList("filtered", "a"),
				rankedList("flat", "flat-top"),
			},
			limit: 3, wantIDs: []string{"a", "flat-top"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, diagnostics, err := FuseRankedLists(tt.lists, MultiListOptions{
				RRFConstant: 60,
				Limit:       tt.limit,
				FlatSource:  "flat",
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fusedIDs(results), tt.wantIDs) {
				t.Fatalf("ids = %v, want %v", fusedIDs(results), tt.wantIDs)
			}
			if diagnostics.FlatRescueApplied != tt.wantRescue {
				t.Fatalf("FlatRescueApplied = %v, want %v", diagnostics.FlatRescueApplied, tt.wantRescue)
			}
			if len(results) > 0 && results[len(results)-1].FlatRescued != tt.wantLastRescued {
				t.Fatalf("last FlatRescued = %v, want %v", results[len(results)-1].FlatRescued, tt.wantLastRescued)
			}
		})
	}
}

func TestFuseRankedListsValidatesBoundsAndOpaqueIDs(t *testing.T) {
	valid := []RankedList{rankedList("flat", "id")}
	tests := []struct {
		name    string
		lists   []RankedList
		options MultiListOptions
	}{
		{name: "no sources", options: MultiListOptions{RRFConstant: 60, Limit: 1}},
		{name: "empty source name", lists: []RankedList{rankedList("", "id")}, options: MultiListOptions{RRFConstant: 60, Limit: 1}},
		{name: "duplicate source", lists: []RankedList{rankedList("flat", "a"), rankedList("flat", "b")}, options: MultiListOptions{RRFConstant: 60, Limit: 1}},
		{name: "empty list", lists: []RankedList{rankedList("flat")}, options: MultiListOptions{RRFConstant: 60, Limit: 1}},
		{name: "empty id", lists: []RankedList{rankedList("flat", "")}, options: MultiListOptions{RRFConstant: 60, Limit: 1}},
		{name: "whitespace id", lists: []RankedList{rankedList("flat", " id ")}, options: MultiListOptions{RRFConstant: 60, Limit: 1}},
		{name: "duplicate id within list", lists: []RankedList{rankedList("flat", "id", "id")}, options: MultiListOptions{RRFConstant: 60, Limit: 1}},
		{name: "zero constant", lists: valid, options: MultiListOptions{Limit: 1}},
		{name: "constant above cap", lists: valid, options: MultiListOptions{RRFConstant: MaxRRFConstant + 1, Limit: 1}},
		{name: "zero limit", lists: valid, options: MultiListOptions{RRFConstant: 60}},
		{name: "limit above cap", lists: valid, options: MultiListOptions{RRFConstant: 60, Limit: MaxResults + 1}},
		{name: "missing flat source", lists: valid, options: MultiListOptions{RRFConstant: 60, Limit: 1, FlatSource: "other"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := FuseRankedLists(tt.lists, tt.options); err == nil {
				t.Fatal("FuseRankedLists() error = nil")
			}
		})
	}

	tooManySources := make([]RankedList, MaxRankedSources+1)
	for i := range tooManySources {
		tooManySources[i] = rankedList("source-"+strconv.Itoa(i), "id-"+strconv.Itoa(i))
	}
	if _, _, err := FuseRankedLists(tooManySources, MultiListOptions{RRFConstant: 60, Limit: 1}); err == nil {
		t.Fatal("accepted sources above cap")
	}

	tooManyCandidates := make([]string, MaxCandidates+1)
	for i := range tooManyCandidates {
		tooManyCandidates[i] = "id-" + strconv.Itoa(i)
	}
	if _, _, err := FuseRankedLists([]RankedList{rankedList("flat", tooManyCandidates...)}, MultiListOptions{RRFConstant: 60, Limit: 1}); err == nil {
		t.Fatal("accepted candidates above cap")
	}

	firstHalf := make([]string, MaxCandidates/2+1)
	secondHalf := make([]string, MaxCandidates/2+1)
	for i := range firstHalf {
		firstHalf[i] = "first-" + strconv.Itoa(i)
		secondHalf[i] = "second-" + strconv.Itoa(i)
	}
	if _, _, err := FuseRankedLists([]RankedList{
		rankedList("first", firstHalf...), rankedList("second", secondHalf...),
	}, MultiListOptions{RRFConstant: 60, Limit: 1}); err == nil {
		t.Fatal("accepted total unique candidates above cap")
	}
}
