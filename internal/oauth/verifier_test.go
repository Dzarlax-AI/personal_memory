package oauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
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

func TestNewJWTVerifierPreservesIssuerAndSetsTimeout(t *testing.T) {
	verifier, err := NewJWTVerifier(JWTVerifierConfig{
		Issuer:   "https://auth.example.com/application/o/personal-memory/",
		Audience: "https://mcp.example.com",
		JWKSURL:  "https://auth.example.com/jwks/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if verifier.issuer != "https://auth.example.com/application/o/personal-memory/" {
		t.Fatalf("issuer should be preserved exactly, got %q", verifier.issuer)
	}
	if verifier.client == http.DefaultClient {
		t.Fatal("verifier should not use http.DefaultClient")
	}
	if verifier.client.Timeout == 0 {
		t.Fatal("verifier http client should have a timeout")
	}
}

func TestJWTVerifierRequiresExpirationClaim(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{rsaJWK("test-key", &key.PublicKey)},
		})
	}))
	defer jwks.Close()

	verifier, err := NewJWTVerifier(JWTVerifierConfig{
		Issuer:   "https://auth.example.com/application/o/personal-memory/",
		Audience: "https://mcp.example.com",
		JWKSURL:  jwks.URL,
		Scopes:   []string{"memory:mcp"},
	})
	if err != nil {
		t.Fatal(err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":   "https://auth.example.com/application/o/personal-memory/",
		"aud":   "https://mcp.example.com",
		"sub":   "user",
		"scope": "memory:mcp",
		"iat":   time.Now().Unix(),
	})
	token.Header["kid"] = "test-key"
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}

	_, err = verifier.Verify(context.Background(), signed)
	if err == nil {
		t.Fatal("expected token without exp to be rejected")
	}
	if !strings.Contains(err.Error(), "exp") {
		t.Fatalf("expected exp-related error, got %v", err)
	}
}

func rsaJWK(kid string, key *rsa.PublicKey) map[string]string {
	exponent := bigEndianInt(key.E)
	return map[string]string{
		"kid": kid,
		"kty": "RSA",
		"use": "sig",
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(exponent),
	}
}

func bigEndianInt(v int) []byte {
	var out []byte
	for v > 0 {
		out = append([]byte{byte(v)}, out...)
		v >>= 8
	}
	return out
}
