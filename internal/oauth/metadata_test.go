package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProtectedResourceMetadata(t *testing.T) {
	metadata := NewProtectedResourceMetadata(MetadataConfig{
		Resource:             "https://mcp.example.com/",
		AuthorizationServers: []string{"https://auth.example.com/application/o/personal-memory/"},
		Scopes:               []string{"memory:mcp"},
	})

	if metadata.Resource != "https://mcp.example.com" {
		t.Fatalf("expected trimmed resource, got %q", metadata.Resource)
	}
	if metadata.AuthorizationServers[0] != "https://auth.example.com/application/o/personal-memory/" {
		t.Fatalf("unexpected authorization server: %q", metadata.AuthorizationServers[0])
	}
	if metadata.ScopesSupported[0] != "memory:mcp" {
		t.Fatalf("unexpected scope: %q", metadata.ScopesSupported[0])
	}
}

func TestChallenge(t *testing.T) {
	challenge := Challenge("https://mcp.example.com/", []string{"memory:mcp"})
	if !strings.Contains(challenge, `resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`) {
		t.Fatalf("challenge missing metadata URL: %q", challenge)
	}
	if !strings.Contains(challenge, `scope="memory:mcp"`) {
		t.Fatalf("challenge missing scope: %q", challenge)
	}
}

func TestMetadataHandler(t *testing.T) {
	handler := MetadataHandler(NewProtectedResourceMetadata(MetadataConfig{
		Resource:             "https://mcp.example.com",
		AuthorizationServers: []string{"https://auth.example.com/application/o/personal-memory/"},
		Scopes:               []string{"memory:mcp"},
	}))

	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got ProtectedResourceMetadata
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Resource != "https://mcp.example.com" {
		t.Fatalf("unexpected resource: %q", got.Resource)
	}
}
