package memory

import (
	"context"
	"encoding/json"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"os"
	"testing"
	"time"
)

// TestControlledContextEmptyInstallProtocol exercises a fresh private registry
// over MCP HTTP. Qdrant and embedding backends are isolated HTTP fakes.
func TestControlledContextEmptyInstallProtocol(t *testing.T) {
	backend := &aiTestBackend{}
	s := newAIServer(t, backend)
	cfg := registryTestConfig(t)
	if err := s.ConfigureAI(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.CatalogDir); !os.IsNotExist(err) {
		t.Fatal("registry is not initially absent")
	}
	ms := server.NewMCPServer("isolated-memory", "1", server.WithToolCapabilities(true))
	s.RegisterTools(ms)
	hs := server.NewTestStreamableHTTPServer(ms)
	defer hs.Close()
	c, err := client.NewStreamableHttpClient(hs.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION, ClientInfo: mcp.Implementation{Name: "constructed-client", Version: "1"}}}); err != nil {
		t.Fatal(err)
	}
	call := func(name string, args map[string]interface{}) *mcp.CallToolResult {
		t.Helper()
		q := toolRequest(args)
		q.Params.Name = name
		r, e := c.CallTool(ctx, q)
		if e != nil || r.IsError {
			t.Fatalf("%s: %v %#v", name, e, r)
		}
		return r
	}
	checkStore := func(text string, origin bool) {
		t.Helper()
		args := map[string]interface{}{"fact": text, "namespace": "projects"}
		if origin {
			args["source_project"] = "alpha"
			args["source_kind"] = "client_declared"
		}
		r := call("store_fact", args)
		raw, e := json.Marshal(r.StructuredContent)
		if e != nil {
			t.Fatal(e)
		}
		var got StoreFactResult
		if json.Unmarshal(raw, &got) != nil || !got.Stored || got.AI != nil {
			t.Fatalf("baseline store %s", raw)
		}
		backend.mu.Lock()
		defer backend.mu.Unlock()
		if backend.stored == nil {
			t.Fatal("no point written")
		}
		p := backend.stored.Payload
		if primary, ok := p["primary_tag"]; ok && primary != "" {
			t.Fatal("origin inferred as primary")
		}
		o := originPayload(p)
		if origin && (o == nil || o.SourceProject != "alpha") {
			t.Fatal("declared origin lost")
		}
		if !origin && o != nil {
			t.Fatal("origin fabricated")
		}
	}
	life := call("store_fact", map[string]interface{}{"fact": "I enjoy long walks", "namespace": "personal", "subject_scope": "non_project"})
	lifeRaw, _ := json.Marshal(life.StructuredContent)
	var lifeResult StoreFactResult
	if json.Unmarshal(lifeRaw, &lifeResult) != nil || !lifeResult.Stored || lifeResult.SubjectDecision != "non_project" {
		t.Fatal("life fact failed without registry", string(lifeRaw))
	}
	if originPayload(backend.stored.Payload) != nil || backend.stored.Payload["primary_tag"] != "" {
		t.Fatal("life fact acquired origin or project")
	}
	checkStore("Constructed fact before project registration", false)
	if _, err = os.Stat(cfg.CatalogDir); !os.IsNotExist(err) {
		t.Fatal("legacy store created registry")
	}
	r := call("ensure_project", registrationTestArgs("fresh-install-alpha", "alpha"))
	raw, _ := json.Marshal(r.StructuredContent)
	var first contextcatalog.RegistrationResult
	if json.Unmarshal(raw, &first) != nil || first.Status != "created" {
		t.Fatalf("registration %s", raw)
	}
	r = call("ensure_project", registrationTestArgs("fresh-install-alpha", "alpha"))
	raw, _ = json.Marshal(r.StructuredContent)
	var again contextcatalog.RegistrationResult
	if json.Unmarshal(raw, &again) != nil || again.Status != "existing" || again.ProjectID != first.ProjectID {
		t.Fatal("registration not idempotent")
	}
	_, before, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	checkStore("Constructed fact after registration with no source declaration", false)
	checkStore("Constructed fact recorded from alpha with unspecified subject", true)
	_, after, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil || before != after {
		t.Fatal("store changed project catalog")
	}
	t.Log("Fresh registry: legacy write succeeds; registration created/existing; writes with and without origin succeed; origin never becomes primary; no inference keys or providers.")
}
