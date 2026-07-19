package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Default TEI batch size. TEI accepts arrays; this caps memory and keeps
// requests below most reverse-proxy body limits.
const defaultBatchSize = 32

const defaultHTTPTimeout = 30 * time.Second
const maxResponseBodyBytes int64 = 8 << 20

type Client struct {
	url        string
	httpClient *http.Client
}

func NewClient(url string) *Client {
	return &Client{
		url:        url,
		httpClient: &http.Client{Timeout: defaultHTTPTimeout},
	}
}

func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := c.embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("empty embed response")
	}
	return vecs[0], nil
}

// EmbedBatch embeds many texts in one or more HTTP calls (chunked by
// defaultBatchSize). Preserves input order.
func (c *Client) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	result := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += defaultBatchSize {
		end := i + defaultBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := c.embed(ctx, texts[i:end])
		if err != nil {
			return nil, err
		}
		if len(vecs) != end-i {
			return nil, fmt.Errorf("embed batch size mismatch: asked %d, got %d", end-i, len(vecs))
		}
		result = append(result, vecs...)
	}
	return result, nil
}

// embed POSTs one batch to TEI and returns the resulting vectors.
func (c *Client) embed(ctx context.Context, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]interface{}{"inputs": inputs})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := readLimitedBody(resp.Body, maxResponseBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read embed response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed failed (status %d): %s", resp.StatusCode, string(responseBody))
	}

	var result [][]float32
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}
	return result, nil
}

func readLimitedBody(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return body, nil
}
