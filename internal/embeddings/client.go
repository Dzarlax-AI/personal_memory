package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Default TEI batch size. TEI accepts arrays; this caps memory and keeps
// requests below most reverse-proxy body limits.
const defaultBatchSize = 32

const defaultHTTPTimeout = 30 * time.Second
const maxResponseBodyBytes int64 = 8 << 20
const maxInfoResponseBodyBytes int64 = 64 << 10

// ModelInfo describes the vector-space contract reported by TEI's /info
// endpoint. Runtime version is diagnostic; the remaining fields participate in
// the persisted collection identity.
type ModelInfo struct {
	ModelID    string `json:"model_id"`
	ModelSHA   string `json:"model_sha"`
	ModelDType string `json:"model_dtype"`
	ModelType  struct {
		Embedding struct {
			Pooling string `json:"pooling"`
		} `json:"embedding"`
	} `json:"model_type"`
	Version string `json:"version"`
}

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

// Info returns the model identity served by TEI.
func (c *Client) Info(ctx context.Context) (ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/info", nil)
	if err != nil {
		return ModelInfo{}, fmt.Errorf("create info request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ModelInfo{}, fmt.Errorf("info request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := readLimitedBody(resp.Body, maxInfoResponseBodyBytes)
	if err != nil {
		return ModelInfo{}, fmt.Errorf("read info response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return ModelInfo{}, fmt.Errorf("info failed (status %d): %s", resp.StatusCode, string(responseBody))
	}

	var info ModelInfo
	if err := json.Unmarshal(responseBody, &info); err != nil {
		return ModelInfo{}, fmt.Errorf("decode info response: %w", err)
	}
	info.ModelID = strings.TrimSpace(info.ModelID)
	info.ModelSHA = strings.TrimSpace(info.ModelSHA)
	info.ModelDType = strings.TrimSpace(info.ModelDType)
	info.ModelType.Embedding.Pooling = strings.TrimSpace(info.ModelType.Embedding.Pooling)
	info.Version = strings.TrimSpace(info.Version)
	if info.ModelID == "" {
		return ModelInfo{}, fmt.Errorf("decode info response: model_id is required")
	}
	if info.ModelSHA == "" {
		return ModelInfo{}, fmt.Errorf("decode info response: model_sha is required")
	}
	if info.ModelDType == "" {
		return ModelInfo{}, fmt.Errorf("decode info response: model_dtype is required")
	}
	if info.ModelType.Embedding.Pooling == "" {
		return ModelInfo{}, fmt.Errorf("decode info response: embedding pooling is required")
	}
	return info, nil
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

// EmbedWithPurpose embeds raw literal text using a versioned, purpose-aware
// input profile. The profile owns all model-specific prefixes.
func (c *Client) EmbedWithPurpose(ctx context.Context, rawText string, purpose Purpose, profile InputProfile, modelID string) ([]float32, error) {
	transformed, err := TransformInput(rawText, purpose, profile, modelID)
	if err != nil {
		return nil, err
	}
	vecs, err := c.embed(ctx, []string{transformed})
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

// EmbedBatchWithPurpose embeds raw literal texts with the same purpose and
// input profile, preserving input order.
func (c *Client) EmbedBatchWithPurpose(ctx context.Context, rawTexts []string, purpose Purpose, profile InputProfile, modelID string) ([][]float32, error) {
	if err := validatePurpose(purpose); err != nil {
		return nil, err
	}
	if err := ValidateInputProfile(profile, modelID); err != nil {
		return nil, err
	}
	if len(rawTexts) == 0 {
		return nil, nil
	}
	transformed := make([]string, len(rawTexts))
	for i, rawText := range rawTexts {
		input, err := TransformInput(rawText, purpose, profile, modelID)
		if err != nil {
			return nil, err
		}
		transformed[i] = input
	}

	result := make([][]float32, 0, len(rawTexts))
	for i := 0; i < len(transformed); i += defaultBatchSize {
		end := i + defaultBatchSize
		if end > len(transformed) {
			end = len(transformed)
		}
		vecs, err := c.embed(ctx, transformed[i:end])
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
