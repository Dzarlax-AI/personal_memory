package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/internal/oauth"
)

type AuthConfig struct {
	APIKey        string
	OAuthEnabled  bool
	OAuthResource string
	OAuthScopes   []string
	Verifier      oauth.TokenVerifier
}

const VizProxySecretHeader = "X-Personal-Memory-Proxy-Secret"

func APIKeyAuth(apiKey string) func(http.Handler) http.Handler {
	return Auth(AuthConfig{APIKey: apiKey})
}

func Auth(cfg AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.APIKey == "" && !cfg.OAuthEnabled {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if cfg.APIKey != "" && constantTimeEqual(requestAPIKey(r), cfg.APIKey) {
				next.ServeHTTP(w, r)
				return
			}

			if cfg.OAuthEnabled && cfg.Verifier != nil {
				token := bearerToken(r.Header.Get("Authorization"))
				if token != "" {
					if _, err := cfg.Verifier.Verify(r.Context(), token); err == nil {
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			if cfg.OAuthEnabled && cfg.OAuthResource != "" {
				w.Header().Set("WWW-Authenticate", oauth.Challenge(cfg.OAuthResource, cfg.OAuthScopes))
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}

// ProxySecretAuth protects routes whose public authentication is delegated to
// a trusted reverse proxy. The proxy must overwrite (not merely forward) this
// header after it authenticates the request.
func ProxySecretAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if secret == "" || !constantTimeEqual(r.Header.Get(VizProxySecretHeader), secret) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func constantTimeEqual(got, want string) bool {
	gotHash := sha256.Sum256([]byte(got))
	wantHash := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotHash[:], wantHash[:]) == 1
}

func requestAPIKey(r *http.Request) string {
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}
	return bearerToken(r.Header.Get("Authorization"))
}

func bearerToken(authHeader string) string {
	if len(authHeader) > 7 && strings.EqualFold(authHeader[:7], "Bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	return ""
}
