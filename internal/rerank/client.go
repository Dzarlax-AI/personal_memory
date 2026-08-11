package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const MaxCandidates = 100

type Candidate struct {
	ID   string
	Text string
}

type Ranked struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

type Reranker interface {
	Rerank(context.Context, string, []Candidate) ([]Ranked, error)
}

// Client is a bounded TEI-compatible /rerank client. ModelID is explicit
// experiment identity; it is never inferred from the embedding service.
type Client struct {
	endpoint string
	modelID  string
	http     *http.Client
	cap      int
}

func NewClient(baseURL, modelID string, timeout time.Duration, candidateCap int) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("reranker URL must be an absolute URL without credentials")
	}
	if strings.TrimSpace(modelID) == "" {
		return nil, fmt.Errorf("reranker model ID is required")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("reranker timeout must be positive")
	}
	if candidateCap < 1 || candidateCap > MaxCandidates {
		return nil, fmt.Errorf("reranker candidate cap must be between 1 and %d", MaxCandidates)
	}
	return &Client{
		endpoint: strings.TrimRight(parsed.String(), "/") + "/rerank",
		modelID:  strings.TrimSpace(modelID), http: &http.Client{Timeout: timeout}, cap: candidateCap,
	}, nil
}

func (c *Client) ModelID() string { return c.modelID }

func (c *Client) Rerank(ctx context.Context, query string, candidates []Candidate) ([]Ranked, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("rerank query is required")
	}
	if len(candidates) == 0 || len(candidates) > c.cap {
		return nil, fmt.Errorf("rerank candidates must contain between 1 and %d items", c.cap)
	}
	texts := make([]string, len(candidates))
	for i, candidate := range candidates {
		if strings.TrimSpace(candidate.ID) == "" || strings.TrimSpace(candidate.Text) == "" {
			return nil, fmt.Errorf("rerank candidate ID and text are required")
		}
		texts[i] = candidate.Text
	}
	body, err := json.Marshal(map[string]any{
		"query": query, "texts": texts, "truncate": true, "raw_scores": false, "return_text": false,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build rerank request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("rerank returned HTTP %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var ranked []Ranked
	if err := decoder.Decode(&ranked); err != nil {
		return nil, fmt.Errorf("decode rerank response: %w", err)
	}
	if len(ranked) != len(candidates) {
		return nil, fmt.Errorf("rerank response contains %d of %d candidates", len(ranked), len(candidates))
	}
	seen := make(map[int]bool, len(ranked))
	for _, item := range ranked {
		if item.Index < 0 || item.Index >= len(candidates) || seen[item.Index] {
			return nil, fmt.Errorf("rerank response contains invalid index")
		}
		seen[item.Index] = true
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score == ranked[j].Score {
			return candidates[ranked[i].Index].ID < candidates[ranked[j].Index].ID
		}
		return ranked[i].Score > ranked[j].Score
	})
	return ranked, nil
}

// ApplyFailOpen returns the exact input order when the reranker is disabled or
// fails. The reason is a bounded stable code suitable for privacy-safe traces.
func ApplyFailOpen(ctx context.Context, service Reranker, query string, candidates []Candidate) ([]Candidate, string) {
	if service == nil || len(candidates) == 0 {
		return append([]Candidate(nil), candidates...), "reranker_disabled"
	}
	ranked, err := service.Rerank(ctx, query, candidates)
	if err != nil {
		return append([]Candidate(nil), candidates...), "reranker_fallback"
	}
	result := make([]Candidate, len(ranked))
	for i, item := range ranked {
		result[i] = candidates[item.Index]
	}
	return result, "reranker_applied"
}
