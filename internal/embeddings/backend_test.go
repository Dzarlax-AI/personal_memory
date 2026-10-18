package embeddings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testHTTPClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn, Timeout: defaultHTTPTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func testConfig(provider, model string, profile InputProfile, dims int) BackendConfig {
	c := BackendConfig{Provider: provider, Model: model, InputProfile: profile, Dimensions: dims, URL: "http://127.0.0.1:8000"}
	if provider == "llamacpp" {
		c.WeightsSHA256 = strings.Repeat("a", 64)
	}
	if provider == "tei" {
		c.Revision = strings.Repeat("b", 40)
	}
	return c
}
func testVector(n int, first, second float32) []float32 {
	v := make([]float32, n)
	v[0] = first
	if n > 1 {
		v[1] = second
	}
	return v
}
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestValidateBackendConfig(t *testing.T) {
	cases := []struct {
		name string
		c    BackendConfig
		ok   bool
	}{
		{"TEI", testConfig("tei", multilingualE5SmallModelID, LegacyRawV1, 384), true},
		{"TEI missing revision", func() BackendConfig {
			c := testConfig("tei", multilingualE5SmallModelID, LegacyRawV1, 384)
			c.Revision = ""
			return c
		}(), false},
		{"noncanonical provider", func() BackendConfig {
			c := testConfig("openai", "text-embedding-3-small", OpenAITextV1, 256)
			c.Provider = "OpenAI"
			c.APIKeyEnv = "KEY"
			c.URL = ""
			return c
		}(), false},
		{"invalid env var", func() BackendConfig {
			c := testConfig("openai", "text-embedding-3-small", OpenAITextV1, 256)
			c.APIKeyEnv = "BAD NAME"
			c.URL = ""
			return c
		}(), false},
		{"Gemini dimensions below minimum", func() BackendConfig {
			c := testConfig("google", "gemini-embedding-2", GeminiEmbedding2SearchV1, 127)
			c.APIKeyEnv = "KEY"
			c.URL = ""
			return c
		}(), false},
		{"Gemma", testConfig("llamacpp", "embeddinggemma-2", EmbeddingGemmaSearchV1, 768), true},
		{"OpenAI", func() BackendConfig {
			c := testConfig("openai", "text-embedding-3-small", OpenAITextV1, 256)
			c.APIKeyEnv = "KEY"
			c.URL = ""
			c.WeightsSHA256 = ""
			return c
		}(), true},
		{"Google", func() BackendConfig {
			c := testConfig("google", "gemini-embedding-2", GeminiEmbedding2SearchV1, 128)
			c.APIKeyEnv = "KEY"
			c.URL = ""
			c.WeightsSHA256 = ""
			return c
		}(), true},
		{"Gemma mismatched model", testConfig("llamacpp", "embeddinggemma-2-Q8_0", EmbeddingGemmaSearchV1, 768), false},
		{"unknown provider", testConfig("custom", "model", LegacyRawV1, 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := ValidateBackendConfig(tc.c)
			if (e == nil) != tc.ok {
				t.Fatalf("ValidateBackendConfig() error = %v", e)
			}
		})
	}
}

func TestGemmaPurposeTransformAndBatchBound(t *testing.T) {
	c := testConfig("llamacpp", "embeddinggemma-2", EmbeddingGemmaSearchV1, 768)
	var got []string
	b, e := newBackendWithClient(c, testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/tokenize") {
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["add_special"] != true {
				t.Fatalf("tokenize request: %+v", req)
			}
			return response(200, `{"tokens":[1,2,3]}`), nil
		}
		var in struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			t.Fatal("invalid request")
		}
		if in.Model != "embeddinggemma-2" {
			t.Fatalf("model %q", in.Model)
		}
		got = append(got, in.Input...)
		encoded, _ := json.Marshal(map[string]any{"model": "embeddinggemma-2", "data": []any{map[string]any{"index": 0, "embedding": testVector(768, 3, 4)}}})
		return response(200, string(encoded)), nil
	}))
	if e != nil {
		t.Fatal(e)
	}
	v, e := b.EmbedPurpose(context.Background(), "question", RetrievalQuery)
	if e != nil {
		t.Fatal(e)
	}
	if got[0] != "task: search result | query: question" {
		t.Fatalf("query transform: %q", got[0])
	}
	if v[0] != .6 || v[1] != .8 {
		t.Fatalf("not normalized: %v", v)
	}
	_, e = b.EmbedPurpose(context.Background(), "passage", FactPassage)
	if e != nil {
		t.Fatal(e)
	}
	if got[1] != "title: none | text: passage" {
		t.Fatalf("passage transform: %q", got[1])
	}
}

func TestOpenAIRequestContractAndOrdering(t *testing.T) {
	c := testConfig("openai", "text-embedding-3-small", OpenAITextV1, 2)
	c.URL = ""
	c.WeightsSHA256 = ""
	c.APIKeyEnv = "TEST_OPENAI_KEY"
	t.Setenv(c.APIKeyEnv, "synthetic")
	b, e := NewBackend(c)
	if e != nil {
		t.Fatal(e)
	}
	b.client = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.openai.com/v1/embeddings" {
			t.Fatalf("unexpected endpoint %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Fatal("missing auth")
		}
		var in struct {
			Model      string   `json:"model"`
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
			Encoding   string   `json:"encoding_format"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			t.Fatal("bad request")
		}
		if in.Model != c.Model || in.Dimensions != 2 || in.Encoding != "float" || len(in.Input) != 2 {
			t.Fatalf("bad request fields: %+v", in)
		}
		return response(200, `{"model":"text-embedding-3-small","data":[{"index":1,"embedding":[0,2]},{"index":0,"embedding":[3,0]}]}`), nil
	})
	vs, e := b.EmbedBatchPurpose(context.Background(), []string{"a", "b"}, FactPassage)
	if e != nil {
		t.Fatal(e)
	}
	if vs[0][0] != 1 || vs[1][1] != 1 {
		t.Fatalf("ordering/normalization: %v", vs)
	}
}

func TestGooglePurposeWireAndRejectsWrongCardinality(t *testing.T) {
	c := testConfig("google", "gemini-embedding-001", GeminiEmbedding001TaskV1, 128)
	c.URL = ""
	c.WeightsSHA256 = ""
	c.APIKeyEnv = "TEST_GOOGLE_KEY"
	t.Setenv(c.APIKeyEnv, "synthetic")
	b, e := NewBackend(c)
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	b.client = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://generativelanguage.googleapis.com/v1beta/models/gemini-embedding-001:embedContent" {
			t.Fatalf("endpoint %s", r.URL)
		}
		if r.Header.Get("x-goog-api-key") != "synthetic" {
			t.Fatal("missing API key")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("bad JSON")
		}
		config, _ := body["embedContentConfig"].(map[string]any)
		if config["taskType"] != "RETRIEVAL_QUERY" || config["outputDimensionality"] != float64(128) || config["autoTruncate"] != false {
			t.Fatalf("wire config: %+v", body)
		}
		raw, _ := json.Marshal(map[string]any{"embedding": map[string]any{"values": testVector(128, 1, 0)}})
		return response(200, string(raw)), nil
	})
	_, e = b.EmbedPurpose(context.Background(), "query", RetrievalQuery)
	if e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
	b.client = testHTTPClient(func(*http.Request) (*http.Response, error) {
		return response(200, `{"embedding":{"values":[1,0,3]}}`), nil
	})
	if _, e = b.EmbedPurpose(context.Background(), "query", RetrievalQuery); e == nil {
		t.Fatal("expected dimension rejection")
	}
}

func TestGemini2UsesSearchPrefixesWithoutTaskType(t *testing.T) {
	c := testConfig("google", "gemini-embedding-2", GeminiEmbedding2SearchV1, 128)
	c.URL = ""
	c.APIKeyEnv = "TEST_GEMINI2_KEY"
	t.Setenv(c.APIKeyEnv, "synthetic")
	b, err := NewBackend(c)
	if err != nil {
		t.Fatal(err)
	}
	b.client = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("bad request")
		}
		if _, ok := body["taskType"]; ok {
			t.Fatalf("deprecated top-level taskType sent: %+v", body)
		}
		cfg := body["embedContentConfig"].(map[string]any)
		if _, ok := cfg["taskType"]; ok {
			t.Fatalf("taskType sent for model 2: %+v", cfg)
		}
		content := body["content"].(map[string]any)
		part := content["parts"].([]any)[0].(map[string]any)
		if part["text"] != "task: search result | query: synthetic query" {
			t.Fatalf("input profile not applied: %+v", part)
		}
		if cfg["outputDimensionality"] != float64(128) {
			t.Fatalf("output dims config: %+v", cfg)
		}
		return response(200, `{"embedding":{"values":[1]}}`), nil
	})
	// Wrong cardinality is deliberately rejected after verifying the wire request.
	if _, err = b.EmbedPurpose(context.Background(), "synthetic query", RetrievalQuery); err == nil {
		t.Fatal("expected dimension rejection")
	}
}

func TestProviderErrorsRedactBodyAndInput(t *testing.T) {
	c := testConfig("llamacpp", "embeddinggemma-2", EmbeddingGemmaSearchV1, 768)
	b, e := newBackendWithClient(c, testHTTPClient(func(*http.Request) (*http.Response, error) {
		return response(400, "secret-input-and-provider-body"), nil
	}))
	if e != nil {
		t.Fatal(e)
	}
	_, e = b.EmbedPurpose(context.Background(), "private-input", RetrievalQuery)
	if e == nil || strings.Contains(e.Error(), "secret-input") || strings.Contains(e.Error(), "private-input") {
		t.Fatalf("unredacted error: %v", e)
	}
}

func TestBackendRetryBoundAndRedirectDisabled(t *testing.T) {
	c := testConfig("llamacpp", "embeddinggemma-2", EmbeddingGemmaSearchV1, 768)
	calls := 0
	b, e := newBackendWithClient(c, testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/tokenize" {
			return response(200, `{"tokens":[1,2,3]}`), nil
		}
		calls++
		if calls < 3 {
			return response(503, "retry"), nil
		}
		encoded, _ := json.Marshal(map[string]any{"model": "embeddinggemma-2", "data": []any{map[string]any{"index": 0, "embedding": testVector(768, 1, 0)}}})
		return response(200, string(encoded)), nil
	}))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.Embed(context.Background(), "q"); e != nil {
		t.Fatal(e)
	}
	if calls != 3 {
		t.Fatalf("calls %d", calls)
	}
	calls = 0
	b.client = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/tokenize" {
			return response(200, `{"tokens":[1]}`), nil
		}
		calls++
		return nil, errors.New("unexpected transport")
	})
	_, e = b.Embed(context.Background(), "q")
	if e == nil || calls != 3 {
		t.Fatalf("transport retries should be bounded at three attempts: calls=%d err=%v", calls, e)
	}
}

func TestConstructorReadsSecretOnlyWhenConstructed(t *testing.T) {
	c := testConfig("openai", "text-embedding-3-small", OpenAITextV1, 8)
	c.URL = ""
	c.WeightsSHA256 = ""
	c.APIKeyEnv = "MISSING_BACKEND_KEY"
	os.Unsetenv(c.APIKeyEnv)
	if _, e := NewBackend(c); e == nil {
		t.Fatal("expected missing key")
	}
	t.Setenv(c.APIKeyEnv, "private")
	b, e := NewBackend(c)
	if e != nil {
		t.Fatal(e)
	}
	if b.apiKey != "private" {
		t.Fatal("constructor did not load key")
	}
}

func TestTEIDescriptorRequiresServedImmutableRevision(t *testing.T) {
	c := testConfig("tei", multilingualE5SmallModelID, LegacyRawV1, 384)
	calls := 0
	b, err := newBackendWithClient(c, testHTTPClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path == "/info" {
			return response(200, `{"model_id":"intfloat/multilingual-e5-small","model_sha":"`+strings.Repeat("b", 40)+`","model_dtype":"float32","model_type":{"embedding":{"pooling":"mean"}}}`), nil
		}
		raw, _ := json.Marshal([][]float32{testVector(384, 1, 0)})
		return response(200, string(raw)), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	d, err := b.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.SchemaVersion != 1 || d.Revision != c.Revision || d.TEI == nil || d.TEI.Pooling != "mean" {
		t.Fatalf("descriptor: %+v", d)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
	b.tei.httpClient = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		return response(200, `{"model_id":"intfloat/multilingual-e5-small","model_sha":"`+strings.Repeat("c", 40)+`","model_dtype":"float32","model_type":{"embedding":{"pooling":"mean"}}}`), nil
	})
	if _, err = b.Describe(context.Background()); err == nil {
		t.Fatal("expected served revision mismatch")
	}
}

func TestGemmaContextProbeAndNoSilentTruncation(t *testing.T) {
	c := testConfig("llamacpp", "embeddinggemma-2", EmbeddingGemmaSearchV1, 768)
	vector, _ := json.Marshal(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": testVector(768, 1, 0)}}})
	b, err := newBackendWithClient(c, testHTTPClient(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/props":
			return response(200, `{"default_generation_settings":{"n_ctx":2048},"model_alias":"embeddinggemma-2","model_path":"/models/embeddinggemma-2.gguf","modalities":{"vision":false,"video":false,"audio":false}}`), nil
		case "/v1/models":
			return response(200, `{"data":[{"id":"embeddinggemma-2"}]}`), nil
		case "/tokenize":
			return response(200, `{"tokens":[1,2,3]}`), nil
		case "/v1/embeddings":
			return response(200, string(vector)), nil
		}
		return response(404, `{}`), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	d, err := b.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.SchemaVersion != 2 || d.WeightsProvenance != "declared" || d.WeightsSHA256 != c.WeightsSHA256 {
		t.Fatalf("descriptor: %+v", d)
	}
	// The full transformed input receives a synthetic token count over the configured context.
	b.client = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/tokenize" {
			tokens := make([]int, 2049)
			raw, _ := json.Marshal(map[string]any{"tokens": tokens})
			return response(200, string(raw)), nil
		}
		t.Fatal("oversized input reached embedding endpoint")
		return nil, nil
	})
	if _, err = b.Embed(context.Background(), "oversized synthetic input"); err == nil {
		t.Fatal("expected context overflow rejection")
	}
	b.client = testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/props" {
			return response(200, `{"n_ctx_train":2048,"modalities":{"vision":false}}`), nil
		}
		t.Fatal("unexpected probe request")
		return nil, nil
	})
	if err = b.probeLlama(context.Background()); err == nil {
		t.Fatal("training context must not satisfy runtime context check")
	}
}

func TestNormalizationDoesNotOverflowFloat32(t *testing.T) {
	c := testConfig("llamacpp", "embeddinggemma-2", EmbeddingGemmaSearchV1, 768)
	b, err := NewBackend(c)
	if err != nil {
		t.Fatal(err)
	}
	v := testVector(768, 3.4e38, 3.4e38)
	result, err := b.validateVectors([][]float32{v})
	if err != nil {
		t.Fatal(err)
	}
	if result[0][0] < .70 || result[0][0] > .71 {
		t.Fatalf("normalization overflow: %v", result[0][0])
	}
}
func TestRetryAfterPreservesProviderBackoff(t *testing.T) {
	if got := parseRetryAfter("20"); got != 20*time.Second {
		t.Fatalf("backoff=%v", got)
	}
}
func TestGemmaIdentityRequiresModelListing(t *testing.T) {
	c := testConfig("llamacpp", "embeddinggemma-2", EmbeddingGemmaSearchV1, 768)
	b, err := newBackendWithClient(c, testHTTPClient(func(r *http.Request) (*http.Response, error) { return response(404, `{}`), nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.probeLlamaModel(context.Background()); err == nil {
		t.Fatal("accepted unverified model alias")
	}
}
