package config

import (
	"os"
	"reflect"
	"testing"
)

func TestLoadOAuthConfigDefaultsResourceFromMemoryDomain(t *testing.T) {
	t.Setenv("OAUTH_ENABLED", "true")
	t.Setenv("OAUTH_ISSUER", "https://auth.example.com/application/o/personal-memory")
	t.Setenv("OAUTH_SCOPES", "")
	t.Setenv("OAUTH_RESOURCE", "")
	t.Setenv("OAUTH_AUDIENCE", "")
	t.Setenv("OAUTH_AUTHORIZATION_SERVERS", "")

	cfg := loadOAuthConfig("example.com")

	if !cfg.Enabled {
		t.Fatal("expected OAuth enabled")
	}
	if cfg.Resource != "https://mcp.example.com" {
		t.Fatalf("unexpected resource: %q", cfg.Resource)
	}
	if cfg.Audience != "https://mcp.example.com" {
		t.Fatalf("unexpected audience: %q", cfg.Audience)
	}
	if !reflect.DeepEqual(cfg.Scopes, []string{"memory:mcp"}) {
		t.Fatalf("unexpected scopes: %#v", cfg.Scopes)
	}
	if !reflect.DeepEqual(cfg.AuthorizationServers, []string{"https://auth.example.com/application/o/personal-memory"}) {
		t.Fatalf("unexpected authorization servers: %#v", cfg.AuthorizationServers)
	}
}

func TestLoadOAuthConfigCSV(t *testing.T) {
	t.Setenv("OAUTH_ENABLED", "true")
	t.Setenv("OAUTH_RESOURCE", "https://mcp.example.com")
	t.Setenv("OAUTH_AUDIENCE", "personal-memory")
	t.Setenv("OAUTH_SCOPES", "memory:read, memory:write")
	t.Setenv("OAUTH_AUTHORIZATION_SERVERS", "https://auth1.example.com, https://auth2.example.com")

	cfg := loadOAuthConfig("")

	if cfg.Audience != "personal-memory" {
		t.Fatalf("unexpected audience: %q", cfg.Audience)
	}
	if !reflect.DeepEqual(cfg.Scopes, []string{"memory:read", "memory:write"}) {
		t.Fatalf("unexpected scopes: %#v", cfg.Scopes)
	}
	if !reflect.DeepEqual(cfg.AuthorizationServers, []string{"https://auth1.example.com", "https://auth2.example.com"}) {
		t.Fatalf("unexpected authorization servers: %#v", cfg.AuthorizationServers)
	}
}

func TestEnvCSVEmpty(t *testing.T) {
	key := "TEST_EMPTY_CSV"
	_ = os.Unsetenv(key)
	if got := envCSV(key); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}
