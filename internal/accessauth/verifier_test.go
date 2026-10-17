package accessauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
	var fetches atomic.Int32
	verifier.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		fetches.Add(1)
		return nil, errors.New("fixture outage")
	})}
	now := time.Now().UTC().Truncate(time.Second)
	verifier.now = func() time.Time { return now }
	for i := 0; i < 12; i++ {
		token := signAccessTokenWithKid(t, key, fmt.Sprintf("missing-%d", i), testIssuer, []string{testMCPAud}, "owner@example.net", "app", now.Add(-time.Minute), now.Add(-time.Minute), now.Add(time.Minute))
		_, err = verifier.Verify(context.Background(), token, testMCPAud, true)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Verify() error = %v, want ErrUnavailable", err)
		}
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("JWKS fetches after repeated outage checks = %d, want 1", got)
	}
}

func TestVerifierRefreshCooldownBoundsUnknownKidsAndAllowsRotation(t *testing.T) {
	oldKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rotatedKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(Config{Issuer: testIssuer, JWKSURL: testJWKS, AllowedEmails: []string{"owner@example.net"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	verifier.now = func() time.Time { return now }
	body := jwksForKeys(t, map[string]*rsa.PrivateKey{"old-key": oldKey})
	var fetches atomic.Int32
	verifier.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		fetches.Add(1)
		if r.URL.String() != testJWKS {
			return nil, errors.New("unexpected JWKS URL")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})}
	validFor := func(key *rsa.PrivateKey, kid string) string {
		return signAccessTokenWithKid(t, key, kid, testIssuer, []string{testMCPAud}, "owner@example.net", "app", now.Add(-time.Minute), now.Add(-time.Minute), now.Add(time.Minute))
	}
	if _, err := verifier.Verify(context.Background(), validFor(oldKey, "old-key"), testMCPAud, true); err != nil {
		t.Fatalf("initial key verification failed: %v", err)
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("initial JWKS fetches = %d, want 1", got)
	}

	const misses = 32
	var wg sync.WaitGroup
	for i := 0; i < misses; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token := validFor(oldKey, fmt.Sprintf("attacker-kid-%d", i))
			if _, err := verifier.Verify(context.Background(), token, testMCPAud, true); err == nil {
				t.Errorf("unknown kid %d was accepted", i)
			}
		}(i)
	}
	wg.Wait()
	if got := fetches.Load(); got != 1 {
		t.Fatalf("JWKS fetches after concurrent unknown kids = %d, want 1", got)
	}
	if _, err := verifier.Verify(context.Background(), validFor(oldKey, "old-key"), testMCPAud, true); err != nil {
		t.Fatalf("cached valid key failed during cooldown: %v", err)
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("cached valid key triggered %d JWKS fetches, want 1", got)
	}

	now = now.Add(jwksRefreshCooldown)
	body = jwksForKeys(t, map[string]*rsa.PrivateKey{"old-key": oldKey, "rotated-key": rotatedKey})
	rotatedResult := make(chan error, 1)
	for i := 0; i < misses; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i == 0 {
				_, err := verifier.Verify(context.Background(), validFor(rotatedKey, "rotated-key"), testMCPAud, true)
				rotatedResult <- err
				return
			}
			token := validFor(oldKey, fmt.Sprintf("rotation-race-%d", i))
			if _, err := verifier.Verify(context.Background(), token, testMCPAud, true); err == nil {
				t.Errorf("unknown rotation-race kid %d was accepted", i)
			}
		}(i)
	}
	wg.Wait()
	if err := <-rotatedResult; err != nil {
		t.Fatalf("rotated key was not accepted after cooldown: %v", err)
	}
	if got := fetches.Load(); got != 2 {
		t.Fatalf("JWKS fetches after concurrent rotation misses = %d, want 2", got)
	}
	if _, err := verifier.Verify(context.Background(), validFor(rotatedKey, "rotated-key"), testMCPAud, true); err != nil {
		t.Fatalf("cached rotated key verification failed: %v", err)
	}
	if got := fetches.Load(); got != 2 {
		t.Fatalf("cached rotated key triggered %d JWKS fetches, want 2", got)
	}
}

func TestVerifierDoesNotUseExpiredKeysDuringRefreshCooldown(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(Config{Issuer: testIssuer, JWKSURL: testJWKS, AllowedEmails: []string{"owner@example.net"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	verifier.now = func() time.Time { return now }
	verifier.cacheTTL = time.Second
	var fetches atomic.Int32
	body := jwksForKeys(t, map[string]*rsa.PrivateKey{"test-key": key})
	verifier.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		fetches.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})}
	token := signAccessToken(t, key, testIssuer, []string{testMCPAud}, "owner@example.net", "app", now.Add(-time.Minute), now.Add(-time.Minute), now.Add(time.Hour))
	if _, err := verifier.Verify(context.Background(), token, testMCPAud, true); err != nil {
		t.Fatalf("initial key verification failed: %v", err)
	}
	now = now.Add(2 * time.Second)
	if _, err := verifier.Verify(context.Background(), token, testMCPAud, true); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expired cached key error = %v, want ErrUnavailable", err)
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("expired key caused %d fetches during cooldown, want 1", got)
	}
	now = now.Add(jwksRefreshCooldown)
	if _, err := verifier.Verify(context.Background(), token, testMCPAud, true); err != nil {
		t.Fatalf("key was not refreshed after cooldown: %v", err)
	}
	if got := fetches.Load(); got != 2 {
		t.Fatalf("fetch count after cooldown = %d, want 2", got)
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

func TestCallerCancellationCannotPoisonSharedJWKSRefresh(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, stale := range []bool{false, true} {
		for _, before := range []bool{false, true} {
			t.Run(fmt.Sprintf("expired=%v/cancel-before=%v", stale, before), func(t *testing.T) {
				v := testVerifier(t, key)
				now := time.Now().UTC().Truncate(time.Second)
				token := signAccessToken(t, key, testIssuer, []string{testMCPAud}, "owner@example.net", "app", now.Add(-time.Minute), now.Add(-time.Minute), now.Add(time.Hour))
				v.now = func() time.Time { return now }
				if stale {
					if _, err := v.Verify(context.Background(), token, testMCPAud, true); err != nil {
						t.Fatal(err)
					}
					now = now.Add(v.cacheTTL)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if before {
					cancel()
				}
				body := jwksForKeys(t, map[string]*rsa.PrivateKey{"test-key": key})
				var fetches atomic.Int32
				v.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					fetches.Add(1)
					if !before {
						cancel()
					}
					if err := req.Context().Err(); err != nil {
						return nil, err
					}
					deadline, ok := req.Context().Deadline()
					if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
						t.Error("shared JWKS fetch must have its own short deadline")
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
				})}
				if _, err := v.Verify(ctx, token, testMCPAud, true); err != nil {
					t.Fatalf("caller cancellation poisoned shared refresh: %v", err)
				}
				if _, err := v.Verify(context.Background(), token, testMCPAud, true); err != nil {
					t.Fatalf("subsequent caller failed during cooldown: %v", err)
				}
				if fetches.Load() != 1 {
					t.Fatalf("JWKS fetch count = %d, want one shared fetch", fetches.Load())
				}
			})
		}
	}
}

func testVerifier(t *testing.T, key *rsa.PrivateKey) *Verifier {
	t.Helper()
	verifier, err := NewVerifier(Config{Issuer: testIssuer, JWKSURL: testJWKS, AllowedEmails: []string{"owner@example.net"}})
	if err != nil {
		t.Fatal(err)
	}
	body := jwksForKeys(t, map[string]*rsa.PrivateKey{"test-key": key})
	verifier.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != testJWKS {
			return nil, errors.New("unexpected JWKS URL")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})}
	return verifier
}

func jwksForKeys(t *testing.T, keys map[string]*rsa.PrivateKey) []byte {
	t.Helper()
	entries := make([]map[string]string, 0, len(keys))
	for kid, key := range keys {
		n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
		entries = append(entries, map[string]string{"kid": kid, "kty": "RSA", "use": "sig", "alg": "RS256", "n": n, "e": e})
	}
	body, err := json.Marshal(map[string]any{"keys": entries})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func signAccessToken(t *testing.T, key *rsa.PrivateKey, issuer string, audience []string, email, tokenType string, iat, nbf, exp time.Time) string {
	return signAccessTokenWithKid(t, key, "test-key", issuer, audience, email, tokenType, iat, nbf, exp)
}

func signAccessTokenWithKid(t *testing.T, key *rsa.PrivateKey, kid, issuer string, audience []string, email, tokenType string, iat, nbf, exp time.Time) string {
	t.Helper()
	return signClaimsWithKid(t, key, kid, jwt.MapClaims{"iss": issuer, "aud": audience, "iat": iat.Unix(), "nbf": nbf.Unix(), "exp": exp.Unix(), "email": email, "type": tokenType})
}

func signClaims(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	return signClaimsWithKid(t, key, "test-key", claims)
}

func signClaimsWithKid(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
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
