package middleware

import (
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

func APIKeyAuth(apiKey string) func(http.Handler) http.Handler {
	return Auth(AuthConfig{APIKey: apiKey})
}

func Auth(cfg AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.APIKey == "" && !cfg.OAuthEnabled {
				next.ServeHTTP(w, r)
				return
			}
			if cfg.APIKey != "" && requestAPIKey(r) == cfg.APIKey {
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
