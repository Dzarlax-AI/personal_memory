package embeddings

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	EmbeddingGemmaSearchV1   InputProfile = "embeddinggemma-search-v1"
	GeminiEmbedding2SearchV1 InputProfile = "gemini-embedding-2-search-v1"
	GeminiEmbedding001TaskV1 InputProfile = "gemini-embedding-001-task-v1"
	OpenAITextV1             InputProfile = "openai-text-v1"
	backendMaxInputBytes                  = 1 << 20
	backendMaxBatch                       = 32
)

type BackendConfig struct {
	Provider      string       `json:"provider"`
	Model         string       `json:"model"`
	InputProfile  InputProfile `json:"input_profile"`
	Dimensions    int          `json:"dimensions"`
	URL           string       `json:"url,omitempty"`
	Revision      string       `json:"revision,omitempty"`
	WeightsSHA256 string       `json:"weights_sha256,omitempty"`
	APIKeyEnv     string       `json:"api_key_env,omitempty"`
}

type TEIIdentity struct {
	ModelID    string `json:"model_id"`
	ModelSHA   string `json:"model_sha"`
	ModelDType string `json:"model_dtype"`
	Pooling    string `json:"pooling"`
}

type Descriptor struct {
	SchemaVersion     int          `json:"schema_version"`
	Provider          string       `json:"provider"`
	Model             string       `json:"model"`
	Revision          string       `json:"revision,omitempty"`
	InputProfile      InputProfile `json:"input_profile"`
	Dimensions        int          `json:"dimensions"`
	Normalization     string       `json:"normalization"`
	WeightsSHA256     string       `json:"weights_sha256,omitempty"`
	WeightsProvenance string       `json:"weights_provenance,omitempty"`
	TEI               *TEIIdentity `json:"tei,omitempty"`
	ModelProvenance   string       `json:"model_provenance,omitempty"`
}

type Backend struct {
	cfg    BackendConfig
	client *http.Client
	apiKey string
	tei    *Client
}

func ValidateBackendConfig(c BackendConfig) error {
	if c.Provider != strings.ToLower(strings.TrimSpace(c.Provider)) {
		return errors.New("embedding provider must use its canonical name")
	}
	c.Model = strings.TrimSpace(c.Model)
	c.InputProfile = NormalizeInputProfile(c.InputProfile)
	if c.Model == "" {
		return errors.New("embedding model is required")
	}
	switch c.Provider {
	case "tei":
		if c.InputProfile != LegacyRawV1 && c.InputProfile != MultilingualE5V1 {
			return errors.New("unsupported TEI input profile")
		}
		if c.Model != multilingualE5SmallModelID {
			return errors.New("unsupported TEI model")
		}
		if c.Dimensions != 384 {
			return errors.New("TEI dimensions must be 384")
		}
		if c.URL == "" || !validHTTPServiceURL(c.URL) {
			return errors.New("TEI URL must be an HTTP service URL")
		}
		if !validRevision(c.Revision) {
			return errors.New("TEI revision must be an immutable 40-character SHA")
		}
		if c.WeightsSHA256 != "" || c.APIKeyEnv != "" {
			return errors.New("unsupported TEI configuration field")
		}
	case "llamacpp":
		if c.Model != "embeddinggemma-2" || c.InputProfile != EmbeddingGemmaSearchV1 || c.Dimensions != 768 {
			return errors.New("unsupported llama.cpp model, profile, or dimensions")
		}
		if !validHTTPServiceURL(c.URL) {
			return errors.New("llama.cpp URL must be an HTTP service URL")
		}
		if c.Revision != "" || c.APIKeyEnv != "" {
			return errors.New("unsupported llama.cpp configuration field")
		}
		if !validSHA256(c.WeightsSHA256) {
			return errors.New("llama.cpp weights SHA-256 is required")
		}
	case "openai":
		if c.URL != "" || c.Revision != "" || c.WeightsSHA256 != "" {
			return errors.New("unsupported OpenAI configuration field")
		}
		if !validEnvName(c.APIKeyEnv) {
			return errors.New("OpenAI API key environment variable name is invalid")
		}
		if c.InputProfile != OpenAITextV1 {
			return errors.New("unsupported OpenAI input profile")
		}
		switch c.Model {
		case "text-embedding-3-small":
			if c.Dimensions < 1 || c.Dimensions > 1536 {
				return errors.New("invalid OpenAI dimensions")
			}
		case "text-embedding-3-large":
			if c.Dimensions < 1 || c.Dimensions > 3072 {
				return errors.New("invalid OpenAI dimensions")
			}
		default:
			return errors.New("unsupported OpenAI model")
		}
	case "google":
		if c.URL != "" || c.Revision != "" || c.WeightsSHA256 != "" {
			return errors.New("unsupported Google configuration field")
		}
		if !validEnvName(c.APIKeyEnv) {
			return errors.New("Google API key environment variable name is invalid")
		}
		switch c.Model {
		case "gemini-embedding-2":
			if c.InputProfile != GeminiEmbedding2SearchV1 || c.Dimensions < 128 || c.Dimensions > 3072 {
				return errors.New("unsupported Gemini embedding 2 profile or dimensions")
			}
		case "gemini-embedding-001":
			if c.InputProfile != GeminiEmbedding001TaskV1 || c.Dimensions < 128 || c.Dimensions > 3072 {
				return errors.New("unsupported Gemini embedding 001 profile or dimensions")
			}
		default:
			return errors.New("unsupported Google model")
		}
	default:
		return errors.New("unsupported embedding provider")
	}
	return nil
}

func NewBackend(c BackendConfig) (*Backend, error) {
	if err := ValidateBackendConfig(c); err != nil {
		return nil, err
	}
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	c.Model = strings.TrimSpace(c.Model)
	c.InputProfile = NormalizeInputProfile(c.InputProfile)
	client := &http.Client{Timeout: defaultHTTPTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	b := &Backend{cfg: c, client: client}
	if c.Provider == "tei" {
		b.tei = NewClient(strings.TrimRight(c.URL, "/"))
		b.tei.httpClient = client
	}
	if c.Provider == "openai" || c.Provider == "google" {
		key := strings.TrimSpace(os.Getenv(c.APIKeyEnv))
		if key == "" {
			return nil, errors.New("embedding API key is unavailable")
		}
		b.apiKey = key
	}
	return b, nil
}

// newBackendWithClient keeps provider calls redirectable in synthetic tests only.
func newBackendWithClient(c BackendConfig, client *http.Client) (*Backend, error) {
	if err := ValidateBackendConfig(c); err != nil {
		return nil, err
	}
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	c.InputProfile = NormalizeInputProfile(c.InputProfile)
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	b := &Backend{cfg: c, client: client}
	if c.Provider == "tei" {
		b.tei = NewClient(strings.TrimRight(c.URL, "/"))
		b.tei.httpClient = client
	}
	return b, nil
}

func (b *Backend) Embed(ctx context.Context, text string) ([]float32, error) {
	return b.EmbedPurpose(ctx, text, RetrievalQuery)
}
func (b *Backend) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return b.EmbedBatchPurpose(ctx, texts, RetrievalQuery)
}
func (b *Backend) EmbedPurpose(ctx context.Context, text string, p Purpose) ([]float32, error) {
	v, e := b.EmbedBatchPurpose(ctx, []string{text}, p)
	if e != nil {
		return nil, e
	}
	return v[0], nil
}
func (b *Backend) EmbedBatchPurpose(ctx context.Context, texts []string, p Purpose) ([][]float32, error) {
	if err := validatePurpose(p); err != nil {
		return nil, errors.New("invalid embedding purpose")
	}
	if len(texts) == 0 {
		return nil, nil
	}
	transformed := make([]string, len(texts))
	for i, s := range texts {
		t, e := b.transform(s, p)
		if e != nil {
			return nil, fmt.Errorf("invalid embedding input at index %d", i)
		}
		transformed[i] = t
	}
	batch := backendMaxBatch
	if b.cfg.Provider == "llamacpp" || b.cfg.Provider == "google" {
		batch = 1
	}
	out := make([][]float32, 0, len(texts))
	for i := 0; i < len(transformed); i += batch {
		j := i + batch
		if j > len(transformed) {
			j = len(transformed)
		}
		v, e := b.embedRequest(ctx, transformed[i:j], p)
		if e != nil {
			return nil, e
		}
		out = append(out, v...)
	}
	return out, nil
}
func (b *Backend) transform(s string, p Purpose) (string, error) {
	if strings.TrimSpace(s) == "" || len(s) > backendMaxInputBytes || !utf8.ValidString(s) {
		return "", errors.New("invalid input")
	}
	switch b.cfg.InputProfile {
	case LegacyRawV1:
		return s, nil
	case MultilingualE5V1:
		return TransformInput(s, p, b.cfg.InputProfile, b.cfg.Model)
	case EmbeddingGemmaSearchV1:
		if p == RetrievalQuery {
			return "task: search result | query: " + s, nil
		}
		return "title: none | text: " + s, nil
	case GeminiEmbedding2SearchV1:
		if p == RetrievalQuery {
			return "task: search result | query: " + s, nil
		}
		return "title: none | text: " + s, nil
	case GeminiEmbedding001TaskV1, OpenAITextV1:
		return s, nil
	}
	return "", errors.New("unsupported profile")
}

func (b *Backend) embedRequest(ctx context.Context, inputs []string, p Purpose) ([][]float32, error) {
	if b.cfg.Provider == "tei" {
		vecs, err := b.tei.embed(ctx, inputs)
		if err != nil {
			return nil, errors.New("TEI embedding request failed")
		}
		if len(vecs) != len(inputs) {
			return nil, errors.New("TEI embedding cardinality mismatch")
		}
		return b.validateVectors(vecs)
	}
	if b.cfg.Provider == "llamacpp" {
		for _, input := range inputs {
			count, err := b.tokenCount(ctx, input)
			if err != nil {
				return nil, err
			}
			if count > 2048 {
				return nil, errors.New("llama.cpp input exceeds configured context")
			}
		}
		return b.embedLlama(ctx, inputs)
	}
	if b.cfg.Provider == "openai" {
		return b.embedOpenAI(ctx, inputs)
	}
	return b.embedGoogle(ctx, inputs, p)
}
func (b *Backend) validateVectors(vs [][]float32) ([][]float32, error) {
	if len(vs) == 0 {
		return nil, errors.New("embedding provider returned no vectors")
	}
	for i := range vs {
		if len(vs[i]) != b.cfg.Dimensions {
			return nil, errors.New("embedding provider returned wrong dimensions")
		}
		norm := 0.0
		for _, x := range vs[i] {
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				return nil, errors.New("embedding provider returned invalid vector")
			}
			norm += float64(x) * float64(x)
		}
		if norm <= 0 {
			return nil, errors.New("embedding provider returned zero vector")
		}
		if b.cfg.Provider != "tei" {
			n := math.Sqrt(norm)
			for j := range vs[i] {
				vs[i][j] = float32(float64(vs[i][j]) / n)
			}
		}
	}
	return vs, nil
}
func (b *Backend) Describe(ctx context.Context) (Descriptor, error) {
	d := Descriptor{SchemaVersion: 2, Provider: b.cfg.Provider, Model: b.cfg.Model, Revision: b.cfg.Revision, InputProfile: b.cfg.InputProfile, Dimensions: b.cfg.Dimensions, Normalization: "l2"}
	if b.cfg.Provider == "tei" {
		d.SchemaVersion = 1
		info, e := b.tei.Info(ctx)
		if e != nil {
			return Descriptor{}, errors.New("TEI identity probe failed")
		}
		if info.ModelID != b.cfg.Model || info.ModelSHA != b.cfg.Revision {
			return Descriptor{}, errors.New("TEI model identity mismatch")
		}
		d.Model = info.ModelID
		d.Revision = info.ModelSHA
		d.Normalization = "provider"
		d.TEI = &TEIIdentity{ModelID: info.ModelID, ModelSHA: info.ModelSHA, ModelDType: info.ModelDType, Pooling: info.ModelType.Embedding.Pooling}
		if _, err := b.embedRequest(ctx, []string{"synthetic identity probe"}, RetrievalQuery); err != nil {
			return Descriptor{}, errors.New("TEI dimension probe failed")
		}
		return d, nil
	}
	if b.cfg.Provider == "llamacpp" {
		if e := b.probeLlama(ctx); e != nil {
			return Descriptor{}, e
		}
		if _, e := b.probeLlamaModel(ctx); e != nil {
			return Descriptor{}, e
		}
		if _, e := b.embedRequest(ctx, []string{"task: search result | query: synthetic identity probe"}, RetrievalQuery); e != nil {
			return Descriptor{}, errors.New("llama.cpp dimension probe failed")
		}
		d.WeightsSHA256 = b.cfg.WeightsSHA256
		d.WeightsProvenance = "declared"
		return d, nil
	}
	d.ModelProvenance = "provider-model-id"
	probe := []string{"synthetic embedding backend identity probe"}
	if _, e := b.embedRequest(ctx, probe, RetrievalQuery); e != nil {
		return Descriptor{}, errors.New("embedding provider identity probe failed")
	}
	return d, nil
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
func validRevision(revision string) bool {
	if len(revision) != 40 {
		return false
	}
	_, err := hex.DecodeString(revision)
	return err == nil && strings.ToLower(revision) == revision
}

func validHTTPServiceURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil && strings.ToLower(s) == s
}
func (b *Backend) post(ctx context.Context, endpoint string, payload any, headers map[string]string) ([]byte, error) {
	body, e := json.Marshal(payload)
	if e != nil {
		return nil, errors.New("encode embedding request failed")
	}
	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	deadline, _ := opCtx.Deadline()
	var last error
	var retryAfter time.Duration
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			wait := retryAfter
			if wait <= 0 {
				wait = time.Duration(150*(1<<uint(attempt-1))+rand.Intn(100)) * time.Millisecond
			}
			retryAfter = 0
			if time.Until(deadline) <= wait {
				return nil, errors.New("embedding request deadline exceeded")
			}
			timer := time.NewTimer(wait)
			select {
			case <-opCtx.Done():
				timer.Stop()
				return nil, errors.New("embedding request canceled")
			case <-timer.C:
			}
		}
		req, e := http.NewRequestWithContext(opCtx, http.MethodPost, endpoint, bytes.NewReader(body))
		if e != nil {
			return nil, errors.New("create embedding request failed")
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, e := b.client.Do(req)
		if e != nil {
			last = e
			if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) || attempt == 2 {
				break
			}
			continue
		}
		buf, e := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
		resp.Body.Close()
		if e != nil {
			last = e
			continue
		}
		if len(buf) > 8<<20 {
			return nil, errors.New("embedding response too large")
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
			last = fmt.Errorf("transient status %d", resp.StatusCode)
			if attempt < 2 {
				continue
			}
			break
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("embedding provider rejected request (status %d)", resp.StatusCode)
		}
		return buf, nil
	}
	if last != nil {
		return nil, errors.New("embedding provider request failed")
	}
	return nil, errors.New("embedding provider request failed")
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		if seconds > 86400 {
			return 24 * time.Hour
		}
		d := time.Duration(seconds) * time.Second
		if d < 0 {
			return 24 * time.Hour
		}
		return d
	}
	if at, err := http.ParseTime(value); err == nil {
		d := time.Until(at)
		if d < 0 {
			return 0
		}

		return d
	}
	return 0
}

func (b *Backend) endpoint(path string) string { return strings.TrimRight(b.cfg.URL, "/") + path }
func (b *Backend) embedLlama(ctx context.Context, inputs []string) ([][]float32, error) {
	body, e := b.post(ctx, b.endpoint("/v1/embeddings"), map[string]any{"model": b.cfg.Model, "input": inputs}, nil)
	if e != nil {
		return nil, e
	}
	var r struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.Data) != len(inputs) || (r.Model != "" && r.Model != b.cfg.Model) {
		return nil, errors.New("invalid llama.cpp embedding response")
	}
	sort.Slice(r.Data, func(i, j int) bool { return r.Data[i].Index < r.Data[j].Index })
	v := make([][]float32, len(r.Data))
	for i, x := range r.Data {
		if x.Index != i {
			return nil, errors.New("invalid llama.cpp response ordering")
		}
		v[i] = x.Embedding
	}
	return b.validateVectors(v)
}
func (b *Backend) embedOpenAI(ctx context.Context, inputs []string) ([][]float32, error) {
	h := map[string]string{"Authorization": "Bearer " + b.apiKey}
	body, e := b.post(ctx, "https://api.openai.com/v1/embeddings", map[string]any{"model": b.cfg.Model, "input": inputs, "dimensions": b.cfg.Dimensions, "encoding_format": "float"}, h)
	if e != nil {
		return nil, e
	}
	var r struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.Data) != len(inputs) || r.Model != b.cfg.Model {
		return nil, errors.New("invalid OpenAI embedding response")
	}
	sort.Slice(r.Data, func(i, j int) bool { return r.Data[i].Index < r.Data[j].Index })
	v := make([][]float32, len(r.Data))
	for i, x := range r.Data {
		if x.Index != i {
			return nil, errors.New("invalid OpenAI response ordering")
		}
		v[i] = x.Embedding
	}
	return b.validateVectors(v)
}
func (b *Backend) embedGoogle(ctx context.Context, inputs []string, p Purpose) ([][]float32, error) {
	v := make([][]float32, 0, len(inputs))
	for _, text := range inputs {
		model := url.PathEscape(b.cfg.Model)
		config := map[string]any{"outputDimensionality": b.cfg.Dimensions, "autoTruncate": false}
		if b.cfg.Model == "gemini-embedding-001" {
			if p == RetrievalQuery {
				config["taskType"] = "RETRIEVAL_QUERY"
			} else {
				config["taskType"] = "RETRIEVAL_DOCUMENT"
			}
		}
		payload := map[string]any{"content": map[string]any{"parts": []any{map[string]string{"text": text}}}, "embedContentConfig": config}
		endpoint := "https://generativelanguage.googleapis.com/v1beta/models/" + model + ":embedContent"
		body, e := b.post(ctx, endpoint, payload, map[string]string{"x-goog-api-key": b.apiKey})
		if e != nil {
			return nil, e
		}
		var r struct {
			Embedding struct {
				Values []float32 `json:"values"`
			} `json:"embedding"`
		}
		if json.Unmarshal(body, &r) != nil {
			return nil, errors.New("invalid Google embedding response")
		}
		v = append(v, r.Embedding.Values)
	}
	return b.validateVectors(v)
}
func (b *Backend) tokenCount(ctx context.Context, text string) (int, error) {
	body, err := b.post(ctx, b.endpoint("/tokenize"), map[string]any{"content": text, "add_special": true}, nil)
	if err != nil {
		return 0, errors.New("llama.cpp tokenization probe failed")
	}
	var result struct {
		Tokens []int `json:"tokens"`
	}
	if json.Unmarshal(body, &result) != nil || result.Tokens == nil {
		return 0, errors.New("llama.cpp tokenization response invalid")
	}
	return len(result.Tokens), nil
}

func (b *Backend) probeLlama(ctx context.Context) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint("/props"), nil)
	if e != nil {
		return errors.New("llama.cpp props probe failed")
	}
	resp, e := b.client.Do(req)
	if e != nil {
		return errors.New("llama.cpp props probe failed")
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if e != nil || resp.StatusCode != 200 {
		return errors.New("llama.cpp props probe failed")
	}
	var p map[string]any
	if json.Unmarshal(raw, &p) != nil {
		return errors.New("llama.cpp props response invalid")
	}
	settings, ok := p["default_generation_settings"].(map[string]any)
	if !ok {
		return errors.New("llama.cpp context configuration unavailable")
	}
	ctxSize := numberAt(settings, "n_ctx")
	if ctxSize != 2048 {
		return errors.New("llama.cpp context configuration mismatch")
	}
	modalities, ok := p["modalities"].(map[string]any)
	if !ok {
		return errors.New("llama.cpp modality configuration unavailable")
	}
	for _, key := range []string{"vision", "video", "audio"} {
		if value, exists := modalities[key]; exists && value != false {
			return errors.New("llama.cpp runtime must be text-only")
		}
	}
	if vision, exists := modalities["vision"]; !exists || vision != false {
		return errors.New("llama.cpp runtime must be text-only")
	}
	if alias, ok := p["model_alias"].(string); ok && alias != "" && alias != b.cfg.Model {
		return errors.New("llama.cpp model alias mismatch")
	}
	if path, ok := p["model_path"].(string); ok && path != "" && !strings.Contains(strings.ToLower(path), "embeddinggemma") {
		return errors.New("llama.cpp model configuration mismatch")
	}
	return nil
}
func (b *Backend) probeLlamaModel(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint("/v1/models"), nil)
	if err != nil {
		return false, errors.New("llama.cpp model probe failed")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return false, errors.New("llama.cpp model probe failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return false, errors.New("llama.cpp model probe failed")
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return false, errors.New("llama.cpp model identity unavailable")
	}
	if resp.StatusCode != http.StatusOK {
		return false, errors.New("llama.cpp model probe failed")
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &body) != nil || len(body.Data) == 0 {
		return false, errors.New("llama.cpp model response invalid")
	}
	for _, model := range body.Data {
		if model.ID == b.cfg.Model {
			return true, nil
		}
	}
	return false, errors.New("llama.cpp model alias mismatch")
}

func numberAt(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if n, ok := m[k]; ok {
			switch v := n.(type) {
			case float64:
				return int(v)
			case string:
				i, _ := strconv.Atoi(v)
				return i
			}
		}
	}
	return 0
}
