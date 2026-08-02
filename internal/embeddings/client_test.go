package embeddings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestNewClientHasBoundedHTTPTimeout(t *testing.T) {
	client := NewClient("http://example.test")
	if client.httpClient.Timeout != defaultHTTPTimeout || client.httpClient.Timeout <= 0 {
		t.Fatalf("HTTP timeout = %s, want %s", client.httpClient.Timeout, defaultHTTPTimeout)
	}
}

func TestReadLimitedBodyRejectsOversizedResponse(t *testing.T) {
	_, err := readLimitedBody(strings.NewReader("too large"), 3)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized response error, got %v", err)
	}
}

func TestInfoReturnsModelIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/info" {
			t.Fatalf("request = %s %s, want GET /info", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model_id":" intfloat/multilingual-e5-small ","model_sha":" 614241f ","model_dtype":" float32 ","model_type":{"embedding":{"pooling":" mean "}},"version":" 1.8.3 "}`))
	}))
	defer server.Close()

	info, err := NewClient(server.URL).Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.ModelID != "intfloat/multilingual-e5-small" || info.ModelSHA != "614241f" {
		t.Fatalf("Info = %#v, want trimmed model identity", info)
	}
	if info.ModelDType != "float32" || info.ModelType.Embedding.Pooling != "mean" || info.Version != "1.8.3" {
		t.Fatalf("Info = %#v, want complete trimmed vector contract", info)
	}
}

func TestInfoRejectsNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := NewClient(server.URL).Info(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("Info error = %v, want status 503", err)
	}
}

func TestInfoRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", int(maxInfoResponseBodyBytes)+1)))
	}))
	defer server.Close()

	_, err := NewClient(server.URL).Info(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Info error = %v, want oversized response error", err)
	}
}

func TestInfoRejectsMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model_id":`))
	}))
	defer server.Close()

	_, err := NewClient(server.URL).Info(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decode info response") {
		t.Fatalf("Info error = %v, want JSON decode error", err)
	}
}

func TestInfoRequiresCompleteIdentity(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "empty model id", body: `{"model_id":" ","model_sha":"revision","model_dtype":"float32","model_type":{"embedding":{"pooling":"mean"}}}`, want: "model_id is required"},
		{name: "empty model sha", body: `{"model_id":"model","model_sha":"\t","model_dtype":"float32","model_type":{"embedding":{"pooling":"mean"}}}`, want: "model_sha is required"},
		{name: "empty dtype", body: `{"model_id":"model","model_sha":"revision","model_dtype":" ","model_type":{"embedding":{"pooling":"mean"}}}`, want: "model_dtype is required"},
		{name: "missing pooling", body: `{"model_id":"model","model_sha":"revision","model_dtype":"float32","model_type":{}}`, want: "embedding pooling is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			_, err := NewClient(server.URL).Info(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Info error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEmbedWithPurposeAppliesInputProfile(t *testing.T) {
	tests := []struct {
		name    string
		modelID string
		profile InputProfile
		purpose Purpose
		rawText string
		want    string
	}{
		{
			name:    "legacy retrieval query remains raw",
			modelID: "any/model",
			profile: LegacyRawV1,
			purpose: RetrievalQuery,
			rawText: "find this",
			want:    "find this",
		},
		{
			name:    "legacy accepts query prefix literally",
			modelID: "any/model",
			profile: LegacyRawV1,
			purpose: RetrievalQuery,
			rawText: "query: literal",
			want:    "query: literal",
		},
		{
			name:    "legacy accepts passage prefix literally",
			modelID: "any/model",
			profile: LegacyRawV1,
			purpose: FactPassage,
			rawText: "passage: literal",
			want:    "passage: literal",
		},
		{
			name:    "e5 retrieval query",
			modelID: multilingualE5SmallModelID,
			profile: MultilingualE5V1,
			purpose: RetrievalQuery,
			rawText: "find this",
			want:    "query: find this",
		},
		{
			name:    "e5 fact passage",
			modelID: multilingualE5SmallModelID,
			profile: MultilingualE5V1,
			purpose: FactPassage,
			rawText: "remember this",
			want:    "passage: remember this",
		},
		{
			name:    "e5 chunk passage",
			modelID: multilingualE5SmallModelID,
			profile: MultilingualE5V1,
			purpose: ChunkPassage,
			rawText: "document body",
			want:    "passage: document body",
		},
		{
			name:    "e5 folder passage",
			modelID: multilingualE5SmallModelID,
			profile: MultilingualE5V1,
			purpose: FolderPassage,
			rawText: "folder summary",
			want:    "passage: folder summary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotInputs []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Inputs []string `json:"inputs"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				gotInputs = body.Inputs
				_, _ = w.Write([]byte(`[[1,2,3]]`))
			}))
			defer server.Close()

			got, err := NewClient(server.URL).EmbedWithPurpose(
				context.Background(),
				tt.rawText,
				tt.purpose,
				tt.profile,
				tt.modelID,
			)
			if err != nil {
				t.Fatalf("EmbedWithPurpose: %v", err)
			}
			if !reflect.DeepEqual(got, []float32{1, 2, 3}) {
				t.Fatalf("vector = %#v", got)
			}
			if !reflect.DeepEqual(gotInputs, []string{tt.want}) {
				t.Fatalf("inputs = %#v, want %#v", gotInputs, []string{tt.want})
			}
		})
	}
}

func TestEmbedBatchWithPurposeTransformsEveryRawInput(t *testing.T) {
	var gotInputs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Inputs []string `json:"inputs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotInputs = append(gotInputs, body.Inputs...)
		_, _ = w.Write([]byte(`[[1],[2]]`))
	}))
	defer server.Close()

	got, err := NewClient(server.URL).EmbedBatchWithPurpose(
		context.Background(),
		[]string{"first", "second"},
		ChunkPassage,
		MultilingualE5V1,
		multilingualE5SmallModelID,
	)
	if err != nil {
		t.Fatalf("EmbedBatchWithPurpose: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("vectors = %#v", got)
	}
	wantInputs := []string{"passage: first", "passage: second"}
	if !reflect.DeepEqual(gotInputs, wantInputs) {
		t.Fatalf("inputs = %#v, want %#v", gotInputs, wantInputs)
	}
}

func TestEmbedWithPurposeRejectsReservedE5PrefixesBeforeRequest(t *testing.T) {
	tests := []struct {
		name    string
		purpose Purpose
		rawText string
	}{
		{name: "same query prefix", purpose: RetrievalQuery, rawText: "query: secret-query"},
		{name: "cross passage prefix for query", purpose: RetrievalQuery, rawText: "passage: secret-passage"},
		{name: "cross query prefix for passage", purpose: FactPassage, rawText: "query: secret-query"},
		{name: "same passage prefix", purpose: ChunkPassage, rawText: "passage: secret-passage"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				http.Error(w, "unexpected request", http.StatusInternalServerError)
			}))
			defer server.Close()

			_, err := NewClient(server.URL).EmbedWithPurpose(
				context.Background(),
				tt.rawText,
				tt.purpose,
				MultilingualE5V1,
				multilingualE5SmallModelID,
			)
			if err == nil || !strings.Contains(err.Error(), "reserved embedding input prefix") {
				t.Fatalf("error = %v, want reserved-prefix error", err)
			}
			if strings.Contains(err.Error(), "secret-") {
				t.Fatalf("error leaked input content: %v", err)
			}
			if requests != 0 {
				t.Fatalf("TEI requests = %d, want 0", requests)
			}
		})
	}
}

func TestEmbedBatchWithPurposeRejectsReservedE5PrefixesBeforeRequest(t *testing.T) {
	tests := []struct {
		name    string
		purpose Purpose
		rawText string
	}{
		{name: "same purpose prefix", purpose: FolderPassage, rawText: "passage: secret-passage"},
		{name: "cross purpose prefix", purpose: FolderPassage, rawText: "query: secret-query"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				http.Error(w, "unexpected request", http.StatusInternalServerError)
			}))
			defer server.Close()

			_, err := NewClient(server.URL).EmbedBatchWithPurpose(
				context.Background(),
				[]string{"valid first input", tt.rawText},
				tt.purpose,
				MultilingualE5V1,
				multilingualE5SmallModelID,
			)
			if err == nil || !strings.Contains(err.Error(), "embedding input at index 1") || !strings.Contains(err.Error(), "reserved embedding input prefix") {
				t.Fatalf("error = %v, want safe indexed reserved-prefix error", err)
			}
			if strings.Contains(err.Error(), "secret-") {
				t.Fatalf("error leaked input content: %v", err)
			}
			if requests != 0 {
				t.Fatalf("TEI requests = %d, want 0", requests)
			}
		})
	}
}

func TestEmbeddingProfileValidationRejectsUnsafeCombinations(t *testing.T) {
	tests := []struct {
		name    string
		modelID string
		profile InputProfile
		purpose Purpose
		want    string
	}{
		{name: "unknown profile", modelID: multilingualE5SmallModelID, profile: InputProfile("future-v9"), purpose: RetrievalQuery, want: "unknown embedding input profile"},
		{name: "unsupported model", modelID: "example/other-model", profile: MultilingualE5V1, purpose: RetrievalQuery, want: "does not support"},
		{name: "unknown purpose", modelID: multilingualE5SmallModelID, profile: MultilingualE5V1, purpose: Purpose(99), want: "unknown embedding purpose"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient("http://not-called.invalid")
			_, err := client.EmbedWithPurpose(context.Background(), "text", tt.purpose, tt.profile, tt.modelID)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}

	t.Run("empty batch still validates profile", func(t *testing.T) {
		client := NewClient("http://not-called.invalid")
		_, err := client.EmbedBatchWithPurpose(
			context.Background(),
			nil,
			RetrievalQuery,
			InputProfile("future-v9"),
			multilingualE5SmallModelID,
		)
		if err == nil || !strings.Contains(err.Error(), "unknown embedding input profile") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestLegacyEmbedWrappersSendRawInputs(t *testing.T) {
	var gotInputs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Inputs []string `json:"inputs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotInputs = append(gotInputs, body.Inputs...)
		if len(body.Inputs) == 1 {
			_, _ = w.Write([]byte(`[[1]]`))
			return
		}
		_, _ = w.Write([]byte(`[[1],[2]]`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	if _, err := client.Embed(context.Background(), "query: unchanged"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if _, err := client.EmbedBatch(context.Background(), []string{"passage: unchanged", "raw"}); err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	want := []string{"query: unchanged", "passage: unchanged", "raw"}
	if !reflect.DeepEqual(gotInputs, want) {
		t.Fatalf("inputs = %#v, want %#v", gotInputs, want)
	}
}
