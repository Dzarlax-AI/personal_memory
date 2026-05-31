package oauth

import (
	"reflect"
	"testing"
)

func TestNormalizeScopes(t *testing.T) {
	got := normalizeScopes("openid memory:mcp", []any{"memory:mcp", "profile"})
	want := []string{"openid", "memory:mcp", "profile"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %#v, got %#v", want, got)
	}
}

func TestMissingScopes(t *testing.T) {
	got := missingScopes([]string{"memory:mcp", "profile"}, []string{"memory:mcp"})
	want := []string{"profile"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %#v, got %#v", want, got)
	}
}

func TestNewJWTVerifierRequiresConfig(t *testing.T) {
	if _, err := NewJWTVerifier(JWTVerifierConfig{}); err == nil {
		t.Fatal("expected missing issuer error")
	}
	if _, err := NewJWTVerifier(JWTVerifierConfig{Issuer: "https://auth.example.com"}); err == nil {
		t.Fatal("expected missing audience error")
	}
	if _, err := NewJWTVerifier(JWTVerifierConfig{
		Issuer:   "https://auth.example.com",
		Audience: "https://mcp.example.com",
	}); err == nil {
		t.Fatal("expected missing jwks url error")
	}
}
