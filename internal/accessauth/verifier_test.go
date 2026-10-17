package accessauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer = "https://access.example.org"
	testJWKS   = "https://access.example.org/cdn-cgi/access/certs"
	testMCPAud = "mcp-audience"
	testVizAud = "viz-audience"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVerifierAccessClaimsAndAudiencePolicy(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	verifier := testVerifier(t, key)
	verifier.now = func() time.Time { return now }

	validDual := signAccessToken(t, key, testIssuer, []string{testMCPAud, "other-audience"}, "owner@example.net", "app", now.Add(-time.Minute), now.Add(-time.Minute), now.Add(time.Minute))
	if _, err := verifier.Verify(context.Background(), validDual, testMCPAud, true); err != nil {
		t.Fatalf("MCP audience membership rejected valid token: %v", err)
	}
	if _, err := verifier.Verify(context.Background(), validDual, testMCPAud, false); err == nil {
		t.Fatal("browser profile accepted token with additional audiences")
	}
	foreign := signAccessToken(t, key, testIssuer, []string{testVizAud}, "owner@example.net", "app", now.Add(-time.Minute), now.Add(-time.Minute), now.Add(time.Minute))
	if _, err := verifier.Verify(context.Background(), foreign, testMCPAud, true); err == nil {
		t.Fatal("accepted foreign audience")
	}

	tests := []struct {
		name  string
		token string
	}{
		{name: "bad signature", token: tamperSignature(t, validDual)},
		{name: "bad issuer", token: signAccessToken(t, key, "https://other.example.org", []string{testMCPAud}, "owner@example.net", "app", now.Add(-time.Minute), now.Add(-time.Minute), now.Add(time.Minute))},
		{name: "bad type", token: signAccessToken(t, key, testIssuer, []string{testMCPAud}, "owner@example.net", "service", now.Add(-time.Minute), now.Add(-time.Minute), now.Add(time.Minute))},
		{name: "missing time claims", token: signClaims(t, key, jwt.MapClaims{"iss": testIssuer, "aud": testMCPAud, "exp": now.Add(time.Minute).Unix(), "type": "app", "email": "owner@example.net"})},
		{name: "future issued at", token: signAccessToken(t, key, testIssuer, []string{testMCPAud}, "owner@example.net", "app", now.Add(time.Minute), now.Add(-time.Minute), now.Add(time.Minute))},
		{name: "expired", token: signAccessToken(t, key, testIssuer, []string{testMCPAud}, "owner@example.net", "app", now.Add(-2*time.Minute), now.Add(-2*time.Minute), now.Add(-time.Minute))},
		{name: "unmapped email", token: signAccessToken(t, key, testIssuer, []string{testMCPAud}, "other@example.com", "app", now.Add(-time.Minute), now.Add(-time.Minute), now.Add(time.Minute))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := verifier.Verify(context.Background(), tt.token, testMCPAud, true); err == nil {
				t.Fatal("accepted invalid Access token")
			}
		})
	}
}

func TestVerifierJWKSOutageIsUnavailable(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(Config{Issuer: testIssuer, JWKSURL: testJWKS, AllowedEmails: []string{"owner@example.net"}})
	if err != nil {
		t.Fatal(err)
	}
	verifier.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("fixture outage")
	})}
	token := signAccessToken(t, key, testIssuer, []string{testMCPAud}, "owner@example.net", "app", time.Now().Add(-time.Minute), time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	_, err = verifier.Verify(context.Background(), token, testMCPAud, true)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Verify() error = %v, want ErrUnavailable", err)
	}
}

func TestNewVerifierRejectsNonHTTPSAndMalformedAllowlist(t *testing.T) {
	for _, cfg := range []Config{
		{Issuer: "http://access.example.org", JWKSURL: testJWKS, AllowedEmails: []string{"owner@example.net"}},
		{Issuer: testIssuer, JWKSURL: "http://access.example.org/certs", AllowedEmails: []string{"owner@example.net"}},
		{Issuer: testIssuer, JWKSURL: testJWKS, AllowedEmails: []string{" owner@example.net"}},
	} {
		if _, err := NewVerifier(cfg); err == nil {
			t.Fatalf("NewVerifier accepted invalid config: %#v", cfg)
		}
	}
}

func testVerifier(t *testing.T, key *rsa.PrivateKey) *Verifier {
	t.Helper()
	verifier, err := NewVerifier(Config{Issuer: testIssuer, JWKSURL: testJWKS, AllowedEmails: []string{"owner@example.net"}})
	if err != nil {
		t.Fatal(err)
	}
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	body, _ := json.Marshal(map[string]any{"keys": []map[string]string{{"kid": "test-key", "kty": "RSA", "use": "sig", "alg": "RS256", "n": n, "e": e}}})
	verifier.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != testJWKS {
			return nil, errors.New("unexpected JWKS URL")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})}
	return verifier
}

func signAccessToken(t *testing.T, key *rsa.PrivateKey, issuer string, audience []string, email, tokenType string, iat, nbf, exp time.Time) string {
	t.Helper()
	return signClaims(t, key, jwt.MapClaims{"iss": issuer, "aud": audience, "iat": iat.Unix(), "nbf": nbf.Unix(), "exp": exp.Unix(), "email": email, "type": tokenType})
}

func signClaims(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func tamperSignature(t *testing.T, raw string) string {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatal("unexpected signed token shape")
	}
	parts[2] = strings.Repeat("A", len(parts[2]))
	return strings.Join(parts, ".")
}
