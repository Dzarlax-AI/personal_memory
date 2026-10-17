package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/accessauth"
	"github.com/Dzarlax-AI/personal-memory/internal/config"
	"github.com/Dzarlax-AI/personal-memory/internal/middleware"
	"github.com/Dzarlax-AI/personal-memory/internal/oauth"
	"github.com/go-chi/chi/v5"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestNewEmbeddingCollectionsFollowsRAGFeatureGate(t *testing.T) {
	tests := []struct {
		name      string
		enableRAG bool
		want      []string
	}{
		{name: "memory only", want: []string{"memory"}},
		{name: "memory and RAG", enableRAG: true, want: []string{"memory", "custom_chunks", "custom_folders"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				QdrantURL:            "http://qdrant.test",
				EnableRAG:            tt.enableRAG,
				RAGCollectionChunks:  "custom_chunks",
				RAGCollectionFolders: "custom_folders",
			}
			memoryClient, chunksClient, foldersClient, all := newEmbeddingCollections(cfg)
			if memoryClient == nil {
				t.Fatal("memory client is nil")
			}
			if !tt.enableRAG && (chunksClient != nil || foldersClient != nil) {
				t.Fatal("disabled RAG created document collection clients")
			}
			if tt.enableRAG && (chunksClient == nil || foldersClient == nil) {
				t.Fatal("enabled RAG did not create document collection clients")
			}
			if got := collectionNames(all); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("collection names = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShutdownHTTPServerWaitsForActiveRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "ok")
	}))
	ts.Start()
	defer ts.Close()

	requestDone := make(chan error, 1)
	go func() {
		resp, err := ts.Client().Get(ts.URL)
		if err == nil {
			_ = resp.Body.Close()
		}
		requestDone <- err
	}()
	<-started

	shutdownDone := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { shutdownDone <- shutdownHTTPServer(ctx, ts.Config) }()

	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before active request completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
}

func TestServerTimeoutsAreBounded(t *testing.T) {
	if httpReadHeaderTimeout <= 0 || httpReadTimeout <= 0 || httpIdleTimeout <= 0 || shutdownTimeout <= 0 {
		t.Fatalf("server timeouts must be positive: read_header=%s read=%s idle=%s shutdown=%s", httpReadHeaderTimeout, httpReadTimeout, httpIdleTimeout, shutdownTimeout)
	}
	if mcpRequestBodyLimit <= 0 {
		t.Fatalf("MCP request body limit must be positive: %d", mcpRequestBodyLimit)
	}
}

func TestMemoryAuthRequiredOnlyBypassesForExplicitEmptyDevelopmentMode(t *testing.T) {
	for _, tt := range []struct {
		name          string
		apiKey        string
		oauth         bool
		allowInsecure bool
		want          bool
	}{
		{name: "default empty config stays protected", want: true},
		{name: "explicit insecure empty config", allowInsecure: true, want: false},
		{name: "api key still applies in insecure mode", apiKey: "key", allowInsecure: true, want: true},
		{name: "oauth still applies in insecure mode", oauth: true, allowInsecure: true, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := memoryAuthRequired(tt.apiKey, tt.oauth, tt.allowInsecure); got != tt.want {
				t.Fatalf("memoryAuthRequired()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestMCPEndpointHandlerAcceptsHeadWithoutCallingMCP(t *testing.T) {
	called := false
	handler := mcpEndpointHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	}))

	req := httptest.NewRequest(http.MethodHead, "/memory", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want %d", rec.Code, http.StatusOK)
	}
	if called {
		t.Fatal("HEAD probe reached the MCP handler")
	}
}

func TestMCPEndpointHandlerDelegatesMCPMethods(t *testing.T) {
	called := false
	handler := mcpEndpointHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodPost, "/memory", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if !called {
		t.Fatal("POST did not reach the MCP handler")
	}
}

func TestMemoryRouteHandlerReservesAccessPath(t *testing.T) {
	nativeCalls, operationalCalls, accessCalls := 0, 0, 0
	native := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nativeCalls++ })
	operational := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { operationalCalls++ })
	access := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { accessCalls++ })
	handler := memoryRouteHandler(native, operational, access, true)
	for _, path := range []string{"/memory/OAuth", "/memory/oauth/", "/memory/oauth/child", "/memory/%6fauth", "/memory%2foauth", "/memory/%256fauth", "/memory/unknown", "/memory/oauthx", "/memory/operational%2f"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, rec.Code)
		}
	}
	if nativeCalls != 0 || operationalCalls != 0 || accessCalls != 0 {
		t.Fatalf("path variants reached handlers: native=%d operational=%d access=%d", nativeCalls, operationalCalls, accessCalls)
	}
	req := httptest.NewRequest(http.MethodPost, "/memory/oauth", nil)
	rec := httptest.NewRecorder()
	memoryRouteHandler(native, operational, access, false).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled route status = %d, want 404", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/memory", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	for _, path := range []string{"/memory/"} {
		req = httptest.NewRequest(http.MethodPost, path, nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
	if nativeCalls != 2 {
		t.Fatalf("native route calls = %d, want 2", nativeCalls)
	}
	req = httptest.NewRequest(http.MethodPost, "/memory/operational", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet || operationalCalls != 0 {
		t.Fatalf("operational POST status=%d allow=%q calls=%d", rec.Code, rec.Header().Get("Allow"), operationalCalls)
	}
	req = httptest.NewRequest(http.MethodGet, "/memory/operational", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if operationalCalls != 1 {
		t.Fatalf("operational GET calls = %d, want 1", operationalCalls)
	}
}

func TestNativeOperationalRouteEnforcesRequestBodyLimit(t *testing.T) {
	called := false
	operational := middleware.RequestBodyLimit(mcpRequestBodyLimit)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	handler := memoryRouteHandler(http.NotFoundHandler(), operational, http.NotFoundHandler(), false)
	body := strings.NewReader(strings.Repeat("x", mcpRequestBodyLimit+1))
	req := httptest.NewRequest(http.MethodGet, "/memory/operational", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized operational request status = %d, want 413", rec.Code)
	}
	if called {
		t.Fatal("oversized operational request reached handler")
	}
}

type accessFixtureVerifier struct{}

func (accessFixtureVerifier) Verify(_ context.Context, assertion, audience string, membership bool) (string, error) {
	wantMembership := audience == "mcp-audience"
	if assertion != "signed-fixture" || (audience != "mcp-audience" && audience != "viz-audience") || membership != wantMembership {
		return "", context.Canceled
	}
	return "owner@example.test", nil
}

func TestAccessTransportInitializeAndToolsListAreStateless(t *testing.T) {
	mcpServer := server.NewMCPServer("access-fixture", "1.0.0", server.WithToolCapabilities(true))
	mcpServer.AddTool(mcp.NewTool("fixture_tool"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Text: "ok"}}}, nil
	})
	transport := server.NewStreamableHTTPServer(mcpServer, server.WithStateLess(true))
	access := accessauth.MCPAuth(accessFixtureVerifier{}, "mcp-audience", transport)
	routes := memoryRouteHandler(http.NotFoundHandler(), http.NotFoundHandler(), access, true)

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}
	for _, body := range requests {
		req := httptest.NewRequest(http.MethodPost, "/memory/oauth", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set(accessauth.AssertionHeader, "signed-fixture")
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("MCP request status = %d: %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Mcp-Session-Id") != "" {
			t.Fatal("Access transport returned a local session id")
		}
		var response struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode MCP response: %v; body=%s", err, rec.Body.String())
		}
		if strings.Contains(body, `"method":"tools/list"`) && (len(response.Result.Tools) != 1 || response.Result.Tools[0].Name != "fixture_tool") {
			t.Fatalf("tools/list result = %#v", response.Result.Tools)
		}
	}
}

func TestRegisteredMemoryRoutesKeepNativeAPIKeyIndependent(t *testing.T) {
	mcpServer := server.NewMCPServer("native-fixture", "1.0.0", server.WithToolCapabilities(true))
	mcpServer.AddTool(mcp.NewTool("native_tool"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Text: "ok"}}}, nil
	})
	native := middleware.Auth(middleware.AuthConfig{APIKey: "native-key", OAuthEnabled: true, Verifier: failingOAuthVerifier{}})(
		mcpEndpointHandler(server.NewStreamableHTTPServer(mcpServer)))
	accessCalls := 0
	access := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		accessCalls++
		http.Error(w, "JWKS unavailable", http.StatusServiceUnavailable)
	})
	handler := chi.NewRouter()
	registerMemoryRoutes(handler, memoryRouteHandler(native, http.NotFoundHandler(), access, true))

	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`
	request := func(path, body, session string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-API-Key", "native-key")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	initialized := request("/memory", initialize, "")
	if initialized.Code != http.StatusOK {
		t.Fatalf("native initialize status=%d body=%s", initialized.Code, initialized.Body.String())
	}
	session := initialized.Header().Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("stateful native initialize returned no session id")
	}
	listed := request("/memory", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, session)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"name":"native_tool"`) {
		t.Fatalf("native tools/list status=%d body=%s", listed.Code, listed.Body.String())
	}
	canonicalAccess := request("/memory/oauth", initialize, "")
	if canonicalAccess.Code != http.StatusServiceUnavailable || accessCalls != 1 {
		t.Fatalf("canonical Access route status=%d calls=%d, want 503 and one Access call", canonicalAccess.Code, accessCalls)
	}
	for _, path := range []string{"/memory/%6fauth", "/memory%2foauth", "/memory/%256fauth", "/memory/OAuth", "/memory/oauth/child"} {
		response := request(path, initialize, "")
		if response.Code != http.StatusNotFound {
			t.Errorf("noncanonical Access route %s status=%d, want 404", path, response.Code)
		}
	}
	if accessCalls != 1 {
		t.Fatalf("noncanonical Access routes changed Access calls to %d, want only canonical route", accessCalls)
	}
}

func TestAccessTransportCannotAttachToNativeMCPSession(t *testing.T) {
	mcpServer := server.NewMCPServer("shared-fixture", "1.0.0", server.WithToolCapabilities(true))
	mcpServer.AddTool(mcp.NewTool("client_info"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		session := server.ClientSessionFromContext(ctx)
		name := ""
		if client, ok := session.(server.SessionWithClientInfo); ok {
			name = client.GetClientInfo().Name
		}
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Text: name}}}, nil
	})
	native := middleware.APIKeyAuth("native-key")(mcpEndpointHandler(server.NewStreamableHTTPServer(mcpServer)))
	access := accessauth.MCPAuth(accessFixtureVerifier{}, "mcp-audience", server.NewStreamableHTTPServer(mcpServer, server.WithStateLess(true)))
	routes := chi.NewRouter()
	registerMemoryRoutes(routes, memoryRouteHandler(native, http.NotFoundHandler(), access, true))

	initialize := httptest.NewRequest(http.MethodPost, "/memory", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"native-client","version":"1"}}}`))
	initialize.Header.Set("Content-Type", "application/json")
	initialize.Header.Set("Accept", "application/json")
	initialize.Header.Set("X-API-Key", "native-key")
	initialized := httptest.NewRecorder()
	routes.ServeHTTP(initialized, initialize)
	if initialized.Code != http.StatusOK {
		t.Fatalf("native initialize status=%d body=%s", initialized.Code, initialized.Body.String())
	}
	sessionID := initialized.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("native initialize returned no session id")
	}

	request := httptest.NewRequest(http.MethodPost, "/memory/oauth", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"client_info","arguments":{}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set(accessauth.AssertionHeader, "signed-fixture")
	request.Header.Set("Mcp-Session-Id", sessionID)
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Access tool call status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "native-client") {
		t.Fatalf("Access request attached to native MCP session: %s", response.Body.String())
	}
}

type failingOAuthVerifier struct{}

func (failingOAuthVerifier) Verify(context.Context, string) (*oauth.Claims, error) {
	return nil, errors.New("fixture verifier unavailable")
}

func TestAccessVizRequiresAssertionAndProxySecret(t *testing.T) {
	called := false
	endpoint := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	handler := middleware.ProxySecretAuth("proxy-secret")(
		accessauth.VizAuth(accessFixtureVerifier{}, "viz-audience", endpoint),
	)
	for _, tc := range []struct {
		name      string
		assertion string
		secret    string
		want      int
	}{
		{name: "missing assertion", secret: "proxy-secret", want: http.StatusUnauthorized},
		{name: "missing proxy secret", assertion: "signed-fixture", want: http.StatusUnauthorized},
		{name: "both factors", assertion: "signed-fixture", secret: "proxy-secret", want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(http.MethodGet, "/viz", nil)
			if tc.assertion != "" {
				req.Header.Set(accessauth.AssertionHeader, tc.assertion)
			}
			if tc.secret != "" {
				req.Header.Set(middleware.VizProxySecretHeader, tc.secret)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want || called != (tc.want == http.StatusOK) {
				t.Fatalf("status=%d called=%v, want status=%d", rec.Code, called, tc.want)
			}
		})
	}
}

func TestVizMountProtectsBarePathAndDescendants(t *testing.T) {
	called := false
	endpoint := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	protected := middleware.ProxySecretAuth("proxy-secret")(
		accessauth.VizAuth(accessFixtureVerifier{}, "viz-audience", endpoint))
	router := chi.NewRouter()
	router.Mount("/viz", protected)
	for _, path := range []string{"/viz", "/viz/", "/viz/api/facts"} {
		for _, headers := range []map[string]string{
			{},
			{accessauth.AssertionHeader: "signed-fixture"},
			{middleware.VizProxySecretHeader: "proxy-secret"},
		} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			for key, value := range headers {
				req.Header.Set(key, value)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code < 400 || rec.Code >= 500 {
				t.Errorf("%s with headers %v status=%d, want 4xx", path, headers, rec.Code)
			}
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(accessauth.AssertionHeader, "signed-fixture")
		req.Header.Set(middleware.VizProxySecretHeader, "proxy-secret")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code >= 400 || !called {
			t.Errorf("%s with both factors status=%d called=%t", path, rec.Code, called)
		}
		called = false
	}
}
