package embeddings

import (
	"context"
	"net/http"
	"net/http/httptest"
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
