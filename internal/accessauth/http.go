package accessauth

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

const AssertionHeader = "Cf-Access-Jwt-Assertion"

type TokenVerifier interface {
	Verify(context.Context, string, string, bool) (string, error)
}

// MCPAuth authenticates each server-to-server request independently.
func MCPAuth(verifier TokenVerifier, audience string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if hasHeader(r.Header, "Origin") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		if err := validateAccessCredentials(r); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		assertion := r.Header.Values(AssertionHeader)[0]
		if verifier == nil {
			http.Error(w, "Access verification unavailable", http.StatusServiceUnavailable)
			return
		}
		if _, err := verifier.Verify(r.Context(), assertion, audience, true); err != nil {
			if errors.Is(err, ErrUnavailable) {
				http.Error(w, "Access verification unavailable", http.StatusServiceUnavailable)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// mcp-go v0.48 still looks up a shared MCPServer session by the
		// Mcp-Session-Id header even in stateless mode. Strip it so an Access
		// request cannot attach to a native API-key/OAuth session.
		request := r.Clone(r.Context())
		request.Header = r.Header.Clone()
		request.Header.Del("Mcp-Session-Id")
		next.ServeHTTP(w, request)
	})
}

// VizAuth requires a browser-specific Access assertion. The proxy secret stays
// an independent required factor and is enforced by ProxySecretAuth outside.
func VizAuth(verifier TokenVerifier, audience string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		values := r.Header.Values(AssertionHeader)
		if len(values) != 1 || strings.TrimSpace(values[0]) != values[0] || strings.Contains(values[0], ",") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if verifier == nil {
			http.Error(w, "Access verification unavailable", http.StatusServiceUnavailable)
			return
		}
		if _, err := verifier.Verify(r.Context(), values[0], audience, false); err != nil {
			if errors.Is(err, ErrUnavailable) {
				http.Error(w, "Access verification unavailable", http.StatusServiceUnavailable)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validateAccessCredentials(r *http.Request) error {
	assertions := r.Header.Values(AssertionHeader)
	if len(assertions) != 1 || strings.TrimSpace(assertions[0]) != assertions[0] || strings.Contains(assertions[0], ",") {
		return errInvalidCredentials
	}
	if hasHeader(r.Header, "X-API-Key") {
		return errInvalidCredentials
	}
	authorization := r.Header.Values("Authorization")
	if len(authorization) > 1 || (len(authorization) == 1 && !isForwardedOpaqueAuthorization(authorization[0])) {
		return errInvalidCredentials
	}
	for key := range r.URL.Query() {
		switch strings.ToLower(key) {
		case "api_key", "api-key", "apikey", "access_token", "token", "authorization", "x-api-key":
			return errInvalidCredentials
		}
	}
	return nil
}

func isForwardedOpaqueAuthorization(value string) bool {
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, ",\r\n\t") {
		return false
	}
	space := strings.IndexByte(value, ' ')
	if space < 0 || !strings.EqualFold(value[:space], "Bearer") {
		return false
	}
	token := value[space+1:]
	return strings.HasPrefix(token, "oauth:") && len(token) > len("oauth:") && !strings.ContainsAny(token, " \t")
}

var errInvalidCredentials = &credentialError{}

type credentialError struct{}

func (*credentialError) Error() string { return "conflicting or duplicate credentials" }

func hasHeader(headers http.Header, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}
