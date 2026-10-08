// Package aijudgment provides bounded, opt-in judgment adapters for memory
// write classification and recall ranking. It contains no mutation authority.
package aijudgment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
)

const (
	DecisionsModel       = "gpt-6-luna"
	RubricVersion        = "optional-ai-judgment-v1"
	JevModel             = "jev-1.13.0"
	DecisionsURL         = "https://api.openai.com/v1/decisions"
	JevURL               = "https://api.typesafe.ai/v1/systemone"
	MaxRequestBytes      = 64 << 10
	MaxResponseBytes     = 64 << 10
	defaultTimeout       = time.Second
	probabilityTolerance = 0.02
)

var knownNamespaces = map[string]bool{"personal": true, "work": true, "projects": true, "job-search": true, "tech": true}

type Origin struct {
	SourceProject string `json:"source_project"`
	SourceKind    string `json:"source_kind"`
	RecordedAt    string `json:"recorded_at"`
}

type ClassifyInput struct {
	SubjectScope   string                  `json:"subject_scope,omitempty"`
	SubjectContext string                  `json:"subject_context,omitempty"`
	FactText       string                  `json:"fact_text"`
	Namespace      string                  `json:"namespace"`
	Origin         *Origin                 `json:"origin,omitempty"`
	Catalog        contextcatalog.Snapshot `json:"catalog"`
	OmitCatalog    bool                    `json:"-"`
}

type ClassifyResult struct {
	SubjectDecision     string   `json:"subject_decision,omitempty"`
	Status              string   `json:"status"`
	PrimaryTag          string   `json:"primary_tag,omitempty"`
	RelatedTags         []string `json:"related_tags,omitempty"`
	MissingContextCodes []string `json:"missing_context_codes,omitempty"`
}

type Candidate struct {
	Alias           string   `json:"alias"`
	Text            string   `json:"text"`
	Namespace       string   `json:"namespace"`
	Tags            []string `json:"tags,omitempty"`
	AuthorityBucket string   `json:"authority_bucket"`
}

type RankInput struct {
	ProjectContext *ProjectDescription      `json:"project_context,omitempty"`
	Query          string                   `json:"query"`
	LifecycleMode  string                   `json:"lifecycle_mode"`
	Catalog        *contextcatalog.Snapshot `json:"catalog,omitempty"`
	Candidates     []Candidate              `json:"candidates"`
	OmitCatalog    bool                     `json:"-"`
}

type RankResult struct {
	Status       string             `json:"status"`
	Scores       map[string]float64 `json:"scores,omitempty"`
	NoneRelevant bool               `json:"none_relevant"`
}

type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	Known        bool  `json:"known"`
}

type Provider interface {
	Classify(context.Context, ClassifyInput) (ClassifyResult, Usage, error)
	Rank(context.Context, RankInput) (RankResult, Usage, error)
}

type Config struct {
	Model            string
	Endpoint         string
	APIKey           string
	Transport        http.RoundTripper
	MaxRequestBytes  int
	MaxResponseBytes int
	Timeout          time.Duration
}

type provider struct {
	name, model, endpoint, key string
	transport                  http.RoundTripper
	maxRequest, maxResponse    int
	timeout                    time.Duration
}

func New(name string, cfg Config) (Provider, error) {
	var wantModel, wantEndpoint string
	switch name {
	case "decisions":
		wantModel, wantEndpoint = DecisionsModel, DecisionsURL
	case "jev":
		wantModel, wantEndpoint = JevModel, JevURL
	default:
		return nil, errors.New("unsupported judgment provider")
	}
	if cfg.Model != wantModel {
		return nil, errors.New("judgment model must match the pinned provider model")
	}
	if strings.TrimSpace(cfg.APIKey) == "" || strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, errors.New("invalid judgment API key")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = wantEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != urlPath(wantEndpoint) || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname()))) {
		return nil, errors.New("invalid judgment endpoint")
	}
	if cfg.Endpoint != "" && u.Host != urlHost(wantEndpoint) && !isLoopbackHost(u.Hostname()) {
		return nil, errors.New("judgment endpoint host must be the provider host or loopback")
	}
	maxReq, maxResp := cfg.MaxRequestBytes, cfg.MaxResponseBytes
	if maxReq == 0 {
		maxReq = MaxRequestBytes
	}
	if maxResp == 0 {
		maxResp = MaxResponseBytes
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if maxReq < 1 || maxReq > MaxRequestBytes || maxResp < 1 || maxResp > MaxResponseBytes || timeout <= 0 || timeout > 30*time.Second {
		return nil, errors.New("invalid judgment request bounds")
	}
	tr := cfg.Transport
	if tr == nil {
		tr = http.DefaultTransport
	}
	return &provider{name: name, model: wantModel, endpoint: endpoint, key: cfg.APIKey, transport: tr, maxRequest: maxReq, maxResponse: maxResp, timeout: timeout}, nil
}

func urlPath(s string) string      { u, _ := url.Parse(s); return u.Path }
func urlHost(s string) string      { u, _ := url.Parse(s); return u.Host }
func isLoopbackHost(s string) bool { return s == "localhost" || s == "127.0.0.1" || s == "::1" }

func (p *provider) Classify(ctx context.Context, in ClassifyInput) (ClassifyResult, Usage, error) {
	result := ClassifyResult{Status: "invalid"}
	ctx, cancel := boundedContext(ctx, p.timeout)
	defer cancel()
	body, err := BuildClassifyRequest(p.name, p.model, in)
	if err != nil {
		return result, Usage{}, err
	}
	data, err := p.call(ctx, body)
	if err != nil {
		result.Status = "unavailable"
		return result, Usage{}, err
	}
	answers, usage, err := decodeResponse(p.name, data)
	if err != nil {
		return result, Usage{}, err
	}
	result, err = validateClassify(in, answers)
	if err != nil {
		result.Status = "invalid"
	}
	return result, usage, err
}

func (p *provider) Rank(ctx context.Context, in RankInput) (RankResult, Usage, error) {
	result := RankResult{Status: "invalid"}
	ctx, cancel := boundedContext(ctx, p.timeout)
	defer cancel()
	body, err := BuildRankRequest(p.name, p.model, in)
	if err != nil {
		return result, Usage{}, err
	}
	data, err := p.call(ctx, body)
	if err != nil {
		result.Status = "unavailable"
		return result, Usage{}, err
	}
	answers, usage, err := decodeResponse(p.name, data)
	if err != nil {
		return result, usage, err
	}
	result, err = validateRank(in, answers)
	if err != nil {
		result.Status = "invalid"
	}
	return result, usage, err
}

func (p *provider) call(ctx context.Context, body []byte) ([]byte, error) {
	if len(body) > p.maxRequest {
		return nil, errors.New("judgment request exceeds configured limit")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("cannot create judgment request")
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: noRedirectTransport{base: p.transport}, Timeout: p.timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, contextOr(err, "judgment provider transport failed")
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(p.maxResponse)+1))
	if readErr != nil {
		return nil, errors.New("cannot read judgment provider response")
	}
	if len(data) > p.maxResponse {
		return nil, errors.New("judgment provider response exceeds configured limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("judgment provider returned HTTP %d", resp.StatusCode)
	}
	return data, nil
}

type noRedirectTransport struct{ base http.RoundTripper }

func (t noRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(req)
}

func contextOr(err error, fallback string) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New(fallback)
}

func boundedContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, timeout)
}

type answer struct {
	Choice        string
	Probabilities map[string]float64
	Refusal       bool
}
type responseEnvelope struct {
	Model   string          `json:"model"`
	Answers json.RawMessage `json:"answers"`
	Usage   *struct {
		Input  *int64 `json:"input_tokens"`
		Output *int64 `json:"output_tokens"`
	} `json:"usage"`
}

func decodeResponse(name string, data []byte) (map[string]answer, Usage, error) {
	if err := rejectDuplicateKeys(data); err != nil {
		return nil, Usage{}, errors.New("invalid judgment response")
	}
	var env responseEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, Usage{}, errors.New("invalid judgment response")
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || root == nil || !exactFields(root, "model", "answers", "usage") {
		return nil, Usage{}, errors.New("invalid judgment response")
	}
	want := DecisionsModel
	if name == "jev" {
		want = JevModel
	}
	if env.Model != want {
		return nil, Usage{}, errors.New("judgment response model mismatch")
	}
	if len(env.Answers) == 0 || env.Usage == nil || env.Usage.Input == nil || env.Usage.Output == nil || *env.Usage.Input < 0 || *env.Usage.Output < 0 {
		return nil, Usage{}, errors.New("judgment response missing required fields")
	}
	var usageObject map[string]json.RawMessage
	if json.Unmarshal(root["usage"], &usageObject) != nil || !exactFieldsOptional(usageObject, []string{"input_tokens", "output_tokens"}, []string{"input_tokens_details", "output_tokens_details", "total_tokens"}) {
		return nil, Usage{}, errors.New("invalid judgment usage")
	}
	answers := make(map[string]answer)
	if name == "decisions" {
		var raw []json.RawMessage
		if json.Unmarshal(env.Answers, &raw) != nil || len(raw) == 0 {
			return nil, Usage{}, errors.New("invalid judgment answers")
		}
		for _, value := range raw {
			key, a, err := parseChoiceAnswer(value)
			if err != nil || key == "" {
				return nil, Usage{}, errors.New("invalid judgment answer")
			}
			if _, exists := answers[key]; exists {
				return nil, Usage{}, errors.New("duplicate judgment answer")
			}
			answers[key] = a
		}
	} else {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(env.Answers, &raw); err != nil || raw == nil {
			return nil, Usage{}, errors.New("invalid judgment answers")
		}
		for key, value := range raw {
			a, err := parseChoiceValue(value)
			if err != nil {
				return nil, Usage{}, errors.New("invalid judgment answer")
			}
			answers[key] = a
		}
	}
	return answers, Usage{InputTokens: *env.Usage.Input, OutputTokens: *env.Usage.Output, Known: true}, nil
}

func parseChoiceAnswer(value json.RawMessage) (string, answer, error) {
	var row struct {
		Name          string `json:"name"`
		Type          string `json:"type"`
		Choice        string `json:"choice"`
		Probabilities []struct {
			Value       string   `json:"value"`
			Probability *float64 `json:"probability"`
		} `json:"probabilities"`
		Confidence *float64 `json:"confidence"`
		Refusal    string   `json:"refusal,omitempty"`
	}
	if json.Unmarshal(value, &row) != nil {
		return "", answer{}, errors.New("invalid answer")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(value, &fields) != nil {
		return "", answer{}, errors.New("invalid answer fields")
	}
	if row.Type == "refusal" {
		if row.Name == "" || !exactFieldsOptional(fields, []string{"name", "type"}, []string{"refusal"}) {
			return "", answer{}, errors.New("invalid refusal")
		}
		return row.Name, answer{Refusal: true}, nil
	}
	if !exactFieldsOptional(fields, []string{"name", "type", "choice", "probabilities", "confidence"}, []string{"refusal"}) {
		return "", answer{}, errors.New("invalid answer fields")
	}
	if row.Type != "choice" || row.Confidence == nil || !finiteUnit(*row.Confidence) {
		return "", answer{}, errors.New("invalid answer shape")
	}
	probs := make(map[string]float64, len(row.Probabilities))
	for _, p := range row.Probabilities {
		if p.Probability == nil {
			return "", answer{}, errors.New("invalid probability")
		}
		if _, exists := probs[p.Value]; exists {
			return "", answer{}, errors.New("duplicate probability")
		}
		probs[p.Value] = *p.Probability
	}
	return row.Name, answer{Choice: row.Choice, Probabilities: probs}, nil
}

func parseChoiceValue(value json.RawMessage) (answer, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(value, &fields) != nil {
		return answer{}, errors.New("invalid answer fields")
	}
	var row struct {
		Type          string                     `json:"type"`
		Choice        string                     `json:"choice"`
		Probabilities map[string]json.RawMessage `json:"probabilities"`
		Confidence    *float64                   `json:"confidence"`
		Refusal       string                     `json:"refusal,omitempty"`
	}
	if json.Unmarshal(value, &row) != nil {
		return answer{}, errors.New("invalid answer")
	}
	if row.Type == "refusal" {
		if !exactFieldsOptional(fields, []string{"type"}, []string{"refusal"}) {
			return answer{}, errors.New("invalid refusal")
		}
		return answer{Refusal: true}, nil
	}
	if !exactFieldsOptional(fields, []string{"type", "choice", "probabilities", "confidence"}, []string{"refusal"}) {
		return answer{}, errors.New("invalid answer fields")
	}
	if row.Type != "choice" || row.Choice == "" || row.Confidence == nil || !finiteUnit(*row.Confidence) || row.Probabilities == nil {
		return answer{}, errors.New("invalid answer shape")
	}
	probs := make(map[string]float64, len(row.Probabilities))
	for key, raw := range row.Probabilities {
		var n float64
		if json.Unmarshal(raw, &n) != nil || !finiteUnit(n) {
			return answer{}, errors.New("invalid probability")
		}
		probs[key] = n
	}
	return answer{Choice: row.Choice, Probabilities: probs}, nil
}

func exactFields(m map[string]json.RawMessage, keys ...string) bool {
	return exactFieldsOptional(m, keys, nil)
}
func exactFieldsOptional(m map[string]json.RawMessage, required, optional []string) bool {
	if m == nil {
		return false
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, k := range required {
		allowed[k] = true
		if _, ok := m[k]; !ok {
			return false
		}
	}
	for _, k := range optional {
		allowed[k] = true
	}
	for k := range m {
		if !allowed[k] {
			return false
		}
	}
	return true
}
func finiteUnit(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1 }

func validateDistribution(a answer, expected []string) error {
	if a.Refusal || len(a.Probabilities) != len(expected) {
		return errors.New("incomplete judgment answer")
	}
	allowed := make(map[string]bool, len(expected))
	sum := 0.0
	for _, s := range expected {
		allowed[s] = true
	}
	for k, v := range a.Probabilities {
		if !allowed[k] || v < 0 || v > 1 || math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("invalid judgment probability")
		}
		sum += v
	}
	if !allowed[a.Choice] || math.Abs(sum-1) > probabilityTolerance {
		return errors.New("invalid judgment distribution")
	}
	return nil
}

func validateClassify(in ClassifyInput, answers map[string]answer) (ClassifyResult, error) {
	projectTags := tagsInNamespace(in.Catalog, in.Namespace)
	baseChoices := append(append([]string(nil), projectTags...), "other_project", "insufficient_context")
	if in.SubjectScope != "" || in.SubjectContext != "" {
		baseChoices = append(baseChoices, "non_project", "multiple_projects")
	}
	primary, ok := answers["primary"]
	if !ok || len(answers) != 1+len(projectTags) {
		return ClassifyResult{}, errors.New("incomplete classification response")
	}
	if err := validateDistribution(primary, baseChoices); err != nil {
		return ClassifyResult{}, err
	}
	r := ClassifyResult{Status: "decided"}
	r.SubjectDecision = primary.Choice
	switch primary.Choice {
	case "insufficient_context":
		r.Status = "abstained"
		r.MissingContextCodes = []string{"insufficient_context"}
	case "other_project":
		r.Status = "abstained"
	case "non_project", "multiple_projects":
		r.Status = "abstained"
	default:
		r.PrimaryTag = primary.Choice
		r.SubjectDecision = "known_project"
	}
	for _, tag := range projectTags {
		a, ok := answers["related_"+tag]
		if !ok {
			return ClassifyResult{}, errors.New("incomplete related-project response")
		}
		if err := validateDistribution(a, []string{"yes", "no"}); err != nil {
			return ClassifyResult{}, err
		}
		if a.Choice == "yes" {
			r.RelatedTags = append(r.RelatedTags, tag)
		}
	}
	return r, nil
}

func validateRank(in RankInput, answers map[string]answer) (RankResult, error) {
	if len(answers) != len(in.Candidates)+1 {
		return RankResult{}, errors.New("incomplete ranking response")
	}
	r := RankResult{Status: "decided", Scores: make(map[string]float64, len(in.Candidates))}
	for _, c := range in.Candidates {
		a, ok := answers[c.Alias]
		if !ok {
			return RankResult{}, errors.New("missing candidate score")
		}
		if err := validateDistribution(a, []string{"relevant", "not_relevant"}); err != nil {
			return RankResult{}, err
		}
		r.Scores[c.Alias] = a.Probabilities["relevant"]
	}
	none, ok := answers["none_relevant"]
	if !ok {
		return RankResult{}, errors.New("missing none-relevant judgment")
	}
	if err := validateDistribution(none, []string{"yes", "no"}); err != nil {
		return RankResult{}, err
	}
	r.NoneRelevant = none.Choice == "yes"
	return r, nil
}

func tagsInNamespace(s contextcatalog.Snapshot, namespace string) []string {
	out := make([]string, 0)
	for _, e := range s.Entries {
		if e.Namespace == namespace && e.Namespace == "projects" && contextcatalog.Eligible(e) {
			out = append(out, e.Tag)
		}
	}
	sort.Strings(out)
	return out
}

func validateClassifyInput(in ClassifyInput) error {
	if in.SubjectScope != "" && in.SubjectScope != "project" && in.SubjectScope != "non_project" && in.SubjectScope != "unknown" {
		return errors.New("invalid subject scope")
	}
	if len(in.SubjectContext) > 2048 || !utf8.ValidString(in.SubjectContext) {
		return errors.New("invalid subject context")
	}
	if strings.TrimSpace(in.FactText) == "" || len(in.FactText) > 8*1024 || !knownNamespaces[in.Namespace] {
		return errors.New("invalid classification input")
	}
	if err := contextcatalog.Validate(in.Catalog); err != nil {
		return errors.New("invalid context catalog")
	}
	if in.Origin != nil {
		if (in.Origin.SourceProject != "" && !safeTag(in.Origin.SourceProject)) || (in.Origin.SourceKind != "" && in.Origin.SourceKind != "user_declared" && in.Origin.SourceKind != "client_declared") || len(in.Origin.RecordedAt) > 64 {
			return errors.New("invalid classification origin")
		}
		if in.Origin.RecordedAt != "" {
			if _, err := time.Parse(time.RFC3339, in.Origin.RecordedAt); err != nil {
				return errors.New("invalid classification origin")
			}
		}
	}
	return nil
}

func decodeOne(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
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
			_, err := d.Token()
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
