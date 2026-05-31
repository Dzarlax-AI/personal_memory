package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/oauth"
)

func TestAPIKeyAuth_ValidKey(t *testing.T) {
	handler := APIKeyAuth("test-key")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "test-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAPIKeyAuth_InvalidKey(t *testing.T) {
	handler := APIKeyAuth("test-key")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAPIKeyAuth_MissingKey(t *testing.T) {
	handler := APIKeyAuth("test-key")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAPIKeyAuth_EmptyConfig(t *testing.T) {
	handler := APIKeyAuth("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (no auth configured), got %d", rec.Code)
	}
}

func TestAuth_ValidOAuthBearer(t *testing.T) {
	handler := Auth(AuthConfig{
		OAuthEnabled:  true,
		OAuthResource: "https://mcp.example.com",
		OAuthScopes:   []string{"memory:mcp"},
		Verifier:      fakeVerifier{validToken: "oauth-token"},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer oauth-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAuth_OAuthUnauthorizedIncludesChallenge(t *testing.T) {
	handler := Auth(AuthConfig{
		OAuthEnabled:  true,
		OAuthResource: "https://mcp.example.com",
		OAuthScopes:   []string{"memory:mcp"},
		Verifier:      fakeVerifier{validToken: "oauth-token"},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	challenge := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(challenge, `resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`) {
		t.Fatalf("challenge missing metadata URL: %q", challenge)
	}
	if !strings.Contains(challenge, `scope="memory:mcp"`) {
		t.Fatalf("challenge missing scope: %q", challenge)
	}
}

func TestAuth_APIKeyTakesPrecedenceOverOAuth(t *testing.T) {
	handler := Auth(AuthConfig{
		APIKey:        "test-key",
		OAuthEnabled:  true,
		OAuthResource: "https://mcp.example.com",
		Verifier:      fakeVerifier{err: errors.New("oauth should not be required")},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "test-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

type fakeVerifier struct {
	validToken string
	err        error
}

func (f fakeVerifier) Verify(_ context.Context, token string) (*oauth.Claims, error) {
	if f.err != nil {
		return nil, f.err
	}
	if token != f.validToken {
		return nil, errors.New("invalid token")
	}
	return &oauth.Claims{Subject: "test-user", Scopes: []string{"memory:mcp"}}, nil
}
