package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"sort"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
)

// ResultManifest is an offline replay ledger. It is never an instruction to
// dispatch requests; each row binds to a preview request and its corpus bytes.
type ResultManifest struct {
	SchemaVersion int         `json:"schema_version"`
	CorpusSHA256  string      `json:"corpus_sha256"`
	Rows          []ResultRow `json:"rows"`
}

type ResultRow struct {
	SubjectDecision string             `json:"subject_decision,omitempty"`
	RequestSHA256   string             `json:"request_sha256"`
	Task            string             `json:"task"`
	CaseID          string             `json:"case_id"`
	Provider        string             `json:"provider"`
	Arm             string             `json:"arm"`
	Status          string             `json:"status"`
	PrimaryTag      string             `json:"primary_tag,omitempty"`
	Scores          map[string]float64 `json:"scores,omitempty"`
	NoneRelevant    *bool              `json:"none_relevant,omitempty"`
}

type ReplayReport struct {
	SchemaVersion   int         `json:"schema_version"`
	CorpusSHA256    string      `json:"corpus_sha256"`
	ResultsSHA256   string      `json:"results_sha256"`
	ExpectedRows    int         `json:"expected_rows"`
	ReceivedRows    int         `json:"received_rows"`
	MissingRows     int         `json:"missing_rows"`
	QualityVerdict  string      `json:"quality_verdict"`
	QualityClaims   bool        `json:"quality_claims"`
	SyntheticLabels bool        `json:"synthetic_labels"`
	Arms            []ArmReport `json:"arms"`
}

type ArmReport struct {
	Provider string        `json:"provider"`
	Arm      string        `json:"arm"`
	Write    *WriteMetrics `json:"write,omitempty"`
	Read     *ReadMetrics  `json:"read,omitempty"`
}

type WriteMetrics struct {
	Cases     int     `json:"cases"`
	Correct   int     `json:"correct"`
	Accuracy  float64 `json:"accuracy"`
	Abstained int     `json:"abstained"`
	Invalid   int     `json:"invalid"`
	Missing   int     `json:"missing"`
}

type ReadMetrics struct {
	Cases              int     `json:"cases"`
	MRR                float64 `json:"mrr"`
	BaselineMRR        float64 `json:"baseline_mrr"`
	DeltaMRR           float64 `json:"delta_mrr"`
	RelevantCases      int     `json:"relevant_cases"`
	NoRelevantCases    int     `json:"no_relevant_cases"`
	NoRelevantCorrect  int     `json:"no_relevant_correct"`
	NoRelevantAccuracy float64 `json:"no_relevant_accuracy"`
	Abstained          int     `json:"abstained"`
	Invalid            int     `json:"invalid"`
	Missing            int     `json:"missing"`
}

type requestKey struct{ task, caseID, provider, arm string }

// scoreReplay validates the supplied rows against the exact generated preview.
// Missing rows are retained in every denominator as invalid; unknown, duplicate,
// or mismatched rows reject the manifest instead of silently changing the set.
func scoreReplay(c Corpus, previewManifest Manifest, results ResultManifest, resultsBytes []byte) (ReplayReport, error) {
	if results.SchemaVersion != 1 || results.CorpusSHA256 == "" || results.CorpusSHA256 != previewManifest.CorpusSHA256 {
		return ReplayReport{}, errors.New("results manifest corpus binding mismatch")
	}
	requests := make(map[requestKey]Request, len(previewManifest.Requests))
	for _, req := range previewManifest.Requests {
		if digest(req.Body) != req.SHA256 {
			return ReplayReport{}, errors.New("preview request hash mismatch")
		}
		key := requestKey{req.Task, req.CaseID, req.Provider, req.Arm}
		if _, exists := requests[key]; exists {
			return ReplayReport{}, errors.New("preview contains duplicate request identity")
		}
		requests[key] = req
	}
	rows := make(map[requestKey]ResultRow, len(results.Rows))
	for _, row := range results.Rows {
		key := requestKey{row.Task, row.CaseID, row.Provider, row.Arm}
		req, ok := requests[key]
		if !ok {
			return ReplayReport{}, errors.New("results manifest contains unknown request")
		}
		if row.RequestSHA256 == "" || row.RequestSHA256 != req.SHA256 {
			return ReplayReport{}, errors.New("results manifest request hash mismatch")
		}
		if _, exists := rows[key]; exists {
			return ReplayReport{}, errors.New("results manifest contains duplicate request")
		}
		if row.Status != "decided" && row.Status != "abstained" && row.Status != "invalid" {
			return ReplayReport{}, errors.New("results manifest has invalid status")
		}
		rows[key] = row
	}
	writeCases := make(map[string]WriteCase, len(c.Writes))
	for _, w := range c.Writes {
		writeCases[w.ID] = w
	}
	readCases := make(map[string]ReadCase, len(c.Reads))
	for _, r := range c.Reads {
		readCases[r.ID] = r
	}
	aliasSet := make(map[string]map[string]bool, len(c.Reads))
	allSynthetic := len(c.Writes)+len(c.Reads) > 0
	for _, r := range c.Reads {
		aliasSet[r.ID] = aliasesFor(r)
		if r.LabelStatus != "synthetic-author-reference" {
			allSynthetic = false
		}
		seenExpected := map[string]bool{}
		for _, alias := range r.Expected {
			if !aliasSet[r.ID][alias] || seenExpected[alias] {
				return ReplayReport{}, errors.New("read expectation is not an exact candidate alias set")
			}
			seenExpected[alias] = true
		}
	}
	for _, w := range c.Writes {
		if w.LabelStatus != "synthetic-author-reference" {
			allSynthetic = false
		}
		caseTags := projectTagsForNamespace(c, w.Input.Namespace)
		if !abstentionDecision(w.Expected) && !caseTags[w.Expected] {
			return ReplayReport{}, errors.New("write expectation is not an approved catalog tag")
		}
	}

	report := ReplayReport{SchemaVersion: 1, CorpusSHA256: previewManifest.CorpusSHA256, ResultsSHA256: digest(resultsBytes), ExpectedRows: len(previewManifest.Requests), ReceivedRows: len(rows), MissingRows: len(previewManifest.Requests) - len(rows), QualityVerdict: "not_evaluated", QualityClaims: false, SyntheticLabels: allSynthetic}
	groups := make(map[string]*ArmReport)
	for _, req := range previewManifest.Requests {
		groupKey := req.Provider + "\x00" + req.Arm
		g := groups[groupKey]
		if g == nil {
			g = &ArmReport{Provider: req.Provider, Arm: req.Arm}
			groups[groupKey] = g
		}
		key := requestKey{req.Task, req.CaseID, req.Provider, req.Arm}
		row, found := rows[key]
		if req.Task == "write" {
			if g.Write == nil {
				g.Write = &WriteMetrics{}
			}
			m := g.Write
			m.Cases++
			if !found {
				m.Invalid++
				m.Missing++
				continue
			}
			if row.Status == "invalid" {
				m.Invalid++
				continue
			}
			w, ok := writeCases[req.CaseID]
			if !ok {
				return ReplayReport{}, errors.New("preview write case missing")
			}
			correct, valid, abstained := scoreWriteRow(row, w, projectTagsForNamespace(c, w.Input.Namespace))
			if !valid {
				m.Invalid++
				continue
			}
			if abstained {
				m.Abstained++
			}
			if correct {
				m.Correct++
			}
		} else if req.Task == "read" {
			if g.Read == nil {
				g.Read = &ReadMetrics{}
			}
			m := g.Read
			m.Cases++
			r, ok := readCases[req.CaseID]
			if !ok {
				return ReplayReport{}, errors.New("preview read case missing")
			}
			if len(r.Expected) > 0 {
				m.RelevantCases++
			} else {
				m.NoRelevantCases++
			}
			m.BaselineMRR += reciprocalRank(r.Expected, rankWithinAuthority(r.Input.Candidates, nil))
			if !found {
				m.Invalid++
				m.Missing++
				continue
			}
			if row.Status == "invalid" {
				m.Invalid++
				continue
			}
			mrr, noRelCorrect, valid, abstained := scoreReadRow(row, r, aliasSet[r.ID])
			if !valid {
				m.Invalid++
				continue
			}
			if abstained {
				m.Abstained++
			}
			m.MRR += mrr
			if len(r.Expected) == 0 {
				if noRelCorrect {
					m.NoRelevantCorrect++
				}
			}
		} else {
			return ReplayReport{}, errors.New("preview has unknown task")
		}
	}
	for _, g := range groups {
		if g.Write != nil && g.Write.Cases > 0 {
			g.Write.Accuracy = float64(g.Write.Correct) / float64(g.Write.Cases)
		}
		if g.Read != nil && g.Read.Cases > 0 {
			g.Read.MRR /= float64(g.Read.Cases)
			g.Read.BaselineMRR /= float64(g.Read.Cases)
			g.Read.DeltaMRR = g.Read.MRR - g.Read.BaselineMRR
		}
		if g.Read != nil && g.Read.NoRelevantCases > 0 {
			g.Read.NoRelevantAccuracy = float64(g.Read.NoRelevantCorrect) / float64(g.Read.NoRelevantCases)
		}
		report.Arms = append(report.Arms, *g)
	}
	sort.Slice(report.Arms, func(i, j int) bool {
		if report.Arms[i].Provider != report.Arms[j].Provider {
			return report.Arms[i].Provider < report.Arms[j].Provider
		}
		return report.Arms[i].Arm < report.Arms[j].Arm
	})
	return report, nil
}

func scoreWriteRow(row ResultRow, c WriteCase, allowed map[string]bool) (correct, valid, abstained bool) {
	switch row.Status {
	case "abstained":
		if row.PrimaryTag != "" || row.Scores != nil || row.NoneRelevant != nil {
			return false, false, true
		}
		if !abstentionDecision(row.SubjectDecision) {
			return false, false, true
		}
		return c.Expected == row.SubjectDecision, true, true
	case "decided":
		if (row.SubjectDecision != "" && row.SubjectDecision != "known_project") || row.PrimaryTag == "" || !allowed[row.PrimaryTag] || row.Scores != nil || row.NoneRelevant != nil {
			return false, false, false
		}
		return row.PrimaryTag == c.Expected, true, false
	default:
		return false, false, false
	}
}

func scoreReadRow(row ResultRow, c ReadCase, aliases map[string]bool) (mrr float64, noRelevantCorrect, valid, abstained bool) {
	switch row.Status {
	case "abstained":
		if row.SubjectDecision != "" || row.PrimaryTag != "" || row.Scores != nil || row.NoneRelevant != nil {
			return 0, false, false, true
		}
		return 0, false, true, true
	case "decided":
		if row.PrimaryTag != "" || row.NoneRelevant == nil || len(row.Scores) != len(aliases) {
			return 0, false, false, false
		}
		for alias, score := range row.Scores {
			if !aliases[alias] || math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
				return 0, false, false, false
			}
		}
		if len(c.Expected) == 0 {
			return 0, *row.NoneRelevant, true, false
		}
		if *row.NoneRelevant {
			return 0, false, true, false
		}
		for _, alias := range c.Expected {
			if !aliases[alias] {
				return 0, false, false, false
			}
		}
		ranked := rankWithinAuthority(c.Input.Candidates, row.Scores)
		return reciprocalRank(c.Expected, ranked), false, true, false
	default:
		return 0, false, false, false
	}
}

// rankWithinAuthority applies the runtime authority order globally, then sorts
// by score only inside the same bucket. Stable ties retain the baseline order.
func rankWithinAuthority(candidates []aijudgment.Candidate, scores map[string]float64) []string {
	indices := make([]int, len(candidates))
	for i := range candidates {
		indices[i] = i
	}
	authority := map[string]int{"canonical_current": 0, "other_current": 1, "disputed": 2, "historical": 3, "superseded": 4}
	sort.SliceStable(indices, func(i, j int) bool {
		a, b := candidates[indices[i]], candidates[indices[j]]
		if authority[a.AuthorityBucket] != authority[b.AuthorityBucket] {
			return authority[a.AuthorityBucket] < authority[b.AuthorityBucket]
		}
		return scores[a.Alias] > scores[b.Alias]
	})
	ranked := make([]string, len(candidates))
	for i, index := range indices {
		ranked[i] = candidates[index].Alias
	}
	return ranked
}

func reciprocalRank(expected, ranked []string) float64 {
	if len(expected) == 0 {
		return 0
	}
	relevant := make(map[string]bool, len(expected))
	for _, alias := range expected {
		relevant[alias] = true
	}
	for i, alias := range ranked {
		if relevant[alias] {
			return 1 / float64(i+1)
		}
	}
	return 0
}

func projectTags(c Corpus) map[string]bool {
	out := map[string]bool{}
	for _, e := range c.Catalog.Entries {
		if e.Namespace == "projects" && contextcatalog.Eligible(e) {
			out[e.Tag] = true
		}
	}
	return out
}

func projectTagsForNamespace(c Corpus, namespace string) map[string]bool {
	if namespace != "projects" {
		return map[string]bool{}
	}
	return projectTags(c)
}
func aliasesFor(c ReadCase) map[string]bool {
	out := map[string]bool{}
	for _, candidate := range c.Input.Candidates {
		out[candidate.Alias] = true
	}
	return out
}

func readResults(path string) (ResultManifest, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ResultManifest{}, nil, errors.New("cannot read results manifest")
	}
	if len(data) > 4<<20 {
		return ResultManifest{}, nil, errors.New("results manifest exceeds byte limit")
	}
	if err := rejectJSONDuplicates(data); err != nil {
		return ResultManifest{}, nil, errors.New("invalid results manifest")
	}
	var m ResultManifest
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return ResultManifest{}, nil, errors.New("invalid results manifest")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ResultManifest{}, nil, errors.New("invalid results manifest")
	}
	return m, data, nil
}

func rejectJSONDuplicates(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				t, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := t.(string)
				if !ok || seen[key] {
					return errors.New("duplicate key")
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		default:
			return nil
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

// An unspecified abstention remains invalid rather than receiving semantic credit.
func abstentionDecision(s string) bool {
	return s == "insufficient_context" || s == "other_project" || s == "non_project" || s == "multiple_projects"
}
func decodeStrictJSON(raw []byte, out any) error {
	if err := rejectJSONDuplicates(raw); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
