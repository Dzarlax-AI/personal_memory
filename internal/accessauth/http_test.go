package accessauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fixtureVerifier struct {
	gotToken    string
	gotAudience string
	allowMulti  bool
	err         error
}

func (v *fixtureVerifier) Verify(_ context.Context, token, audience string, allowMulti bool) (string, error) {
	v.gotToken, v.gotAudience, v.allowMulti = token, audience, allowMulti
	return "owner@example.net", v.err
}

func TestMCPAuthMethodOriginAndCredentialRules(t *testing.T) {
	verifier := &fixtureVerifier{}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("X-Forwarded-Authorization", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	})
	handler := MCPAuth(verifier, testMCPAud, next)

	tests := []struct {
		name       string
		method     string
		path       string
		origin     bool
		assertions []string
		authorize  []string
		apiKey     string
		want       int
		wantAllow  string
	}{
		{name: "valid with opaque authorization", method: http.MethodPost, path: "/memory/oauth", assertions: []string{"signed-assertion"}, authorize: []string{"Bearer oauth:opaque-client-credential"}, want: http.StatusOK},
		{name: "origin rejected", method: http.MethodPost, path: "/memory/oauth", origin: true, assertions: []string{"signed-assertion"}, want: http.StatusForbidden},
		{name: "duplicate assertion rejected", method: http.MethodPost, path: "/memory/oauth", assertions: []string{"one", "two"}, want: http.StatusUnauthorized},
		{name: "combined assertion rejected", method: http.MethodPost, path: "/memory/oauth", assertions: []string{"one,two"}, want: http.StatusUnauthorized},
		{name: "duplicate authorization rejected", method: http.MethodPost, path: "/memory/oauth", assertions: []string{"signed-assertion"}, authorize: []string{"Bearer one", "Bearer two"}, want: http.StatusUnauthorized},
		{name: "native bearer rejected", method: http.MethodPost, path: "/memory/oauth", assertions: []string{"signed-assertion"}, authorize: []string{"Bearer native-api-key"}, want: http.StatusUnauthorized},
		{name: "basic authorization rejected", method: http.MethodPost, path: "/memory/oauth", assertions: []string{"signed-assertion"}, authorize: []string{"Basic opaque"}, want: http.StatusUnauthorized},
		{name: "combined API key and authorization rejected", method: http.MethodPost, path: "/memory/oauth", assertions: []string{"signed-assertion"}, authorize: []string{"Bearer opaque"}, apiKey: "api-key", want: http.StatusUnauthorized},
		{name: "API key header rejected", method: http.MethodPost, path: "/memory/oauth", assertions: []string{"signed-assertion"}, apiKey: "api-key", want: http.StatusUnauthorized},
		{name: "query credential rejected", method: http.MethodPost, path: "/memory/oauth?api_key=key", assertions: []string{"signed-assertion"}, want: http.StatusUnauthorized},
		{name: "unsupported method", method: http.MethodGet, path: "/memory/oauth", want: http.StatusMethodNotAllowed, wantAllow: http.MethodPost},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader("{}"))
			for _, assertion := range tt.assertions {
				req.Header.Add(AssertionHeader, assertion)
			}
			for _, authorization := range tt.authorize {
				req.Header.Add("Authorization", authorization)
			}
			if tt.apiKey != "" {
				req.Header.Set("X-API-Key", tt.apiKey)
			}
			if tt.origin {
				req.Header.Set("Origin", "https://browser.example.org")
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if got := rec.Header().Get("Allow"); got != tt.wantAllow {
				t.Fatalf("Allow = %q, want %q", got, tt.wantAllow)
			}
			if called != (tt.want == http.StatusOK) {
				t.Fatalf("next handler called = %v, want %v", called, tt.want == http.StatusOK)
			}
			if tt.name == "valid with opaque authorization" && rec.Header().Get("X-Forwarded-Authorization") != "Bearer oauth:opaque-client-credential" {
				t.Fatal("Access middleware changed or dropped opaque Authorization transport")
			}
		})
	}
	if verifier.gotToken != "signed-assertion" || verifier.gotAudience != testMCPAud || !verifier.allowMulti {
		t.Fatalf("verifier call = token %q audience %q allowMulti %v", verifier.gotToken, verifier.gotAudience, verifier.allowMulti)
	}
}

func TestVizAuthRequiresExclusiveAudienceAndPreservesOrigin(t *testing.T) {
	verifier := &fixtureVerifier{}
	called := false
	handler := VizAuth(verifier, testVizAud, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if got := r.Header.Get("Origin"); got != "https://wrong.example.org" {
			t.Errorf("Origin changed: %q", got)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	req := httptest.NewRequest(http.MethodPatch, "/viz/api/facts/id/tags", strings.NewReader(`{"tags":[]}`))
	req.Header.Set(AssertionHeader, "browser-assertion")
	req.Header.Set("Origin", "https://wrong.example.org")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted || !called || verifier.allowMulti {
		t.Fatalf("status=%d called=%v allowMulti=%v", rec.Code, called, verifier.allowMulti)
	}
}

func TestVizAuthRejectsMissingAssertionAndJWKSOutage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verifier *fixtureVerifier
		assert   string
		want     int
	}{
		{name: "missing assertion", verifier: &fixtureVerifier{}, want: http.StatusUnauthorized},
		{name: "JWKS unavailable", verifier: &fixtureVerifier{err: ErrUnavailable}, assert: "browser-assertion", want: http.StatusServiceUnavailable},
		{name: "invalid assertion", verifier: &fixtureVerifier{err: errors.New("bad token")}, assert: "browser-assertion", want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/viz", nil)
			if tc.assert != "" {
				req.Header.Set(AssertionHeader, tc.assert)
			}
			rec := httptest.NewRecorder()
			VizAuth(tc.verifier, testVizAud, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
