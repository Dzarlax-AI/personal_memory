package accessauth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	maxAssertionBytes   = 16 << 10
	jwksRefreshCooldown = 30 * time.Second
	jwksFetchTimeout    = 4 * time.Second
)

var ErrUnavailable = errors.New("Cloudflare Access verification unavailable")

type Config struct {
	Issuer        string
	JWKSURL       string
	AllowedEmails []string
}

type Verifier struct {
	issuer   string
	jwksURL  string
	allowed  map[string]struct{}
	client   *http.Client
	now      func() time.Time
	cacheTTL time.Duration

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	fetched     time.Time
	lastRefresh time.Time
	hasRefresh  bool
	refreshErr  error
}

type accessClaims struct {
	Email string `json:"email"`
	Type  string `json:"type"`
	jwt.RegisteredClaims
}

func NewVerifier(cfg Config) (*Verifier, error) {
	issuer, err := validateHTTPSURL("ACCESS_ISSUER", cfg.Issuer)
	if err != nil {
		return nil, err
	}
	issuerURL, _ := url.Parse(issuer)
	if issuerURL.Path != "" || issuerURL.RawQuery != "" || issuerURL.Fragment != "" {
		return nil, errors.New("ACCESS_ISSUER must be an HTTPS origin")
	}
	if _, err := validateHTTPSURL("ACCESS_JWKS_URL", cfg.JWKSURL); err != nil {
		return nil, err
	}
	if len(cfg.AllowedEmails) == 0 {
		return nil, errors.New("ACCESS_ALLOWED_EMAILS must contain at least one email")
	}
	allowed := make(map[string]struct{}, len(cfg.AllowedEmails))
	for _, email := range cfg.AllowedEmails {
		if email == "" || strings.TrimSpace(email) != email || strings.ContainsAny(email, ",\r\n") {
			return nil, errors.New("ACCESS_ALLOWED_EMAILS entries must be exact non-empty values")
		}
		if _, duplicate := allowed[email]; duplicate {
			return nil, errors.New("ACCESS_ALLOWED_EMAILS must not contain duplicates")
		}
		allowed[email] = struct{}{}
	}
	return &Verifier{
		issuer:   strings.TrimSuffix(issuer, "/"),
		jwksURL:  cfg.JWKSURL,
		allowed:  allowed,
		client:   &http.Client{Timeout: jwksFetchTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		now:      time.Now,
		cacheTTL: 10 * time.Minute,
	}, nil
}

// Verify requires the configured audience in the signed audience list.
// Callers can additionally require it to be the sole audience.
func (v *Verifier) Verify(ctx context.Context, raw, audience string, allowAdditionalAudiences bool) (string, error) {
	if raw == "" || len(raw) > maxAssertionBytes || strings.TrimSpace(raw) != raw || strings.Contains(raw, ",") {
		return "", errors.New("invalid Access assertion format")
	}
	if audience == "" {
		return "", errors.New("Access audience is required")
	}
	claims := &accessClaims{}
	parsed, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() || !ok || kid == "" {
			return nil, errors.New("invalid Access signing method or key id")
		}
		return v.key(ctx, kid)
	}, jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}), jwt.WithIssuer(v.issuer), jwt.WithAudience(audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return "", fmt.Errorf("%w", ErrUnavailable)
		}
		return "", errors.New("invalid Access assertion")
	}
	if !parsed.Valid || claims.Type != "app" || claims.Email == "" || claims.IssuedAt == nil || claims.NotBefore == nil || claims.ExpiresAt == nil {
		return "", errors.New("invalid Access assertion claims")
	}
	if !allowAdditionalAudiences && (len(claims.Audience) != 1 || claims.Audience[0] != audience) {
		return "", errors.New("invalid Access assertion audience")
	}
	if claims.IssuedAt.Time.After(v.now()) {
		return "", errors.New("future Access assertion")
	}
	if _, ok := v.allowed[claims.Email]; !ok {
		return "", errors.New("unmapped Access identity")
	}
	return claims.Email, nil
}

func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	keys, err := v.cachedKeys(ctx)
	if err != nil {
		return nil, err
	}
	if key := keys[kid]; key != nil {
		return key, nil
	}
	if err := v.refresh(ctx); err != nil {
		return nil, err
	}
	keys, err = v.cachedKeys(ctx)
	if err != nil {
		return nil, err
	}
	if key := keys[kid]; key != nil {
		return key, nil
	}
	return nil, errors.New("Access signing key not found")
}

func (v *Verifier) cachedKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.keys) > 0 && v.now().Sub(v.fetched) < v.cacheTTL {
		return v.keys, nil
	}
	if v.inRefreshCooldown(v.now()) {
		if v.refreshErr != nil {
			return nil, v.refreshErr
		}
		return nil, fmt.Errorf("%w: key refresh cooldown", ErrUnavailable)
	}
	if err := v.fetchLocked(ctx); err != nil {
		return nil, err
	}
	return v.keys, nil
}

func (v *Verifier) refresh(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.fetchLocked(ctx)
}

func (v *Verifier) fetchLocked(ctx context.Context) error {
	now := v.now()
	if v.inRefreshCooldown(now) {
		return v.refreshErr
	}
	v.lastRefresh = now
	v.hasRefresh = true
	// The result is shared, so one caller cannot cancel it for every waiter.
	// The independent deadline still bounds network work after a disconnect.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jwksFetchTimeout)
	defer cancel()
	err := v.fetchOnceLocked(fetchCtx)
	v.refreshErr = err
	return err
}

func (v *Verifier) inRefreshCooldown(now time.Time) bool {
	return v.hasRefresh && now.Sub(v.lastRefresh) < jwksRefreshCooldown
}

func (v *Verifier) fetchOnceLocked(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("fetch Access keys: %w", err)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: fetch keys", ErrUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: key endpoint status %d", ErrUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return fmt.Errorf("%w: invalid key response", ErrUnavailable)
	}
	var doc struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("%w: invalid key response", ErrUnavailable)
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, jwk := range doc.Keys {
		if jwk.Kty != "RSA" || jwk.Kid == "" || (jwk.Use != "" && jwk.Use != "sig") || (jwk.Alg != "" && jwk.Alg != "RS256") {
			continue
		}
		if _, duplicate := keys[jwk.Kid]; duplicate {
			return fmt.Errorf("%w: duplicate key id", ErrUnavailable)
		}
		key, err := publicKey(jwk.N, jwk.E)
		if err != nil {
			return fmt.Errorf("%w: invalid signing key", ErrUnavailable)
		}
		keys[jwk.Kid] = key
	}
	if len(keys) == 0 {
		return fmt.Errorf("%w: no usable signing keys", ErrUnavailable)
	}
	v.keys, v.fetched = keys, v.now()
	return nil
}

func publicKey(nRaw, eRaw string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nRaw)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eRaw)
	if err != nil {
		return nil, err
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	n := new(big.Int).SetBytes(nBytes)
	if e < 3 || e%2 == 0 || len(eBytes) > 4 || n.BitLen() < 2048 || n.BitLen() > 8192 {
		return nil, errors.New("invalid RSA parameters")
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}

func validateHTTPSURL(name, raw string) (string, error) {
	if strings.TrimSpace(raw) != raw || raw == "" {
		return "", fmt.Errorf("%s must be an exact HTTPS URL", name)
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("%s must be an exact HTTPS URL", name)
	}
	return raw, nil
}
