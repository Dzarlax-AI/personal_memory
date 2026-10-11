package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestAIWriteAdmissionBypassesBeforeQueueAndBudget(t *testing.T) {
	for _, mode := range []string{"shadow", "on", "off"} {
		for _, variant := range []string{"omitted", "false", "explicit", "personal", "mismatch", "duplicate", "forbidden-catalog", "forbidden-origin", "forbidden-unassigned", "off-opt-in"} {
			if (variant == "off-opt-in" && mode != "off") || (variant == "duplicate" && mode == "on") {
				continue
			}
			t.Run(mode+"/"+variant, func(t *testing.T) {
				cfg := aiTestConfig(t, mode, "off")
				cfg.Write.Admission = "client_opt_in"
				b := &aiTestBackend{}
				args := map[string]interface{}{"fact": "synthetic scoped assertion", "namespace": "projects", "allow_ai_processing": true}
				switch variant {
				case "omitted":
					delete(args, "allow_ai_processing")
				case "false":
					args["allow_ai_processing"] = false
				case "explicit":
					args["tags"] = "alpha"
				case "personal":
					args["namespace"] = "personal"
					args["subject_scope"] = "non_project"
				case "mismatch":
					args["subject_scope"] = "non_project"
				case "duplicate":
					b.points = []qdrant.Point{aiPoint("1", .99, "", false)}
				case "forbidden-catalog":
					cfg.Egress.AllowedProjectTags = []string{"alpha"}
				case "forbidden-origin":
					args["source_project"] = "denied"
					args["source_kind"] = "client_declared"
				case "forbidden-unassigned":
					cfg.Egress.UnassignedNamespaces = nil
				}
				s := newAIServer(t, b)
				p := &fakeAIProvider{}
				if err := configureAIForTest(t, s, context.Background(), cfg, p, nil); err != nil {
					t.Fatal(err)
				}
				result := aiStoredResult(t, s, args)
				// Drain queued refusals before observing zero provider dispatches.
				if a := s.aiState(); a != nil && cfg.Write.Active() {
					drained := make(chan struct{})
					a.enqueue(func(context.Context) { close(drained) })
					select {
					case <-drained:
					case <-time.After(time.Second):
						t.Fatal("queue did not drain")
					}
				}
				if p.writes.Load() != 0 {
					t.Fatal("denied assertion dispatched")
				}
				if _, err := os.Stat(filepath.Join(cfg.StateDir, "budget.json")); !os.IsNotExist(err) {
					t.Fatalf("budget created for bypass: %v", err)
				}
				if variant == "omitted" || variant == "false" || variant == "off-opt-in" || variant == "explicit" || variant == "personal" || variant == "mismatch" {
					if result.AI != nil {
						t.Fatalf("bypass returned AI outcome: %+v", result.AI)
					}
				}
			})
		}
	}
}

func TestAIWriteAdmissionRejectsInvalidTypesBeforeStorage(t *testing.T) {
	for _, raw := range []any{"true", float64(1), nil, []any{}, map[string]any{}} {
		b := &aiTestBackend{}
		s := newAIServer(t, b)
		r, err := s.storeFact(context.Background(), toolRequest(map[string]interface{}{"fact": "synthetic invalid permission", "namespace": "projects", "allow_ai_processing": raw}))
		if err != nil || !r.IsError || b.upserts != 0 || b.gets != 0 || b.searches != 0 {
			t.Fatalf("invalid permission reached storage: %v %+v", err, b)
		}
	}
}

// This exercises tool discovery and real MCP HTTP serialization with isolated
// Qdrant/embedding HTTP fakes and an in-process recording judgment provider.
func TestMCPScopedWriteShadowAdmission(t *testing.T) {
	cfg := aiTestConfig(t, "shadow", "off")
	cfg.Write.Admission = "client_opt_in"
	b := &aiTestBackend{}
	s := newAIServer(t, b)
	p := &fakeAIProvider{classify: func(_ context.Context, in aijudgment.ClassifyInput) (aijudgment.ClassifyResult, aijudgment.Usage, error) {
		if in.Origin != nil || in.FactText != "synthetic admitted MCP assertion" || in.SubjectContext != "Synthetic alpha component" {
			t.Error("unexpected provider input")
		}
		if len(in.Catalog.Entries) != 2 {
			t.Error("catalog truncated")
		}
		return aijudgment.ClassifyResult{Status: "decided", PrimaryTag: "alpha"}, aijudgment.Usage{Known: true, InputTokens: 10}, nil
	}}
	if err := configureAIForTest(t, s, context.Background(), cfg, p, nil); err != nil {
		t.Fatal(err)
	}
	ms := server.NewMCPServer("isolated-admission", "1", server.WithToolCapabilities(true))
	s.RegisterTools(ms)
	hs := server.NewTestStreamableHTTPServer(ms)
	defer hs.Close()
	c, err := client.NewStreamableHttpClient(hs.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION, ClientInfo: mcp.Implementation{Name: "synthetic-admission", Version: "1"}}}); err != nil {
		t.Fatal(err)
	}
	tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "store_fact" {
			prop, ok := tool.InputSchema.Properties["allow_ai_processing"]
			raw, _ := json.Marshal(prop)
			found = ok && strings.Contains(string(raw), `"type":"boolean"`)
		}
		if tool.Name == "update_fact" || tool.Name == "import_facts" {
			if _, ok := tool.InputSchema.Properties["allow_ai_processing"]; ok {
				t.Fatal("permission exposed on another tool")
			}
		}
	}
	if !found {
		t.Fatal("permission not advertised")
	}
	call := func(args map[string]interface{}) StoreFactResult {
		t.Helper()
		req := toolRequest(args)
		req.Params.Name = "store_fact"
		r, e := c.CallTool(ctx, req)
		if e != nil || r.IsError {
			t.Fatalf("MCP store: %v %+v", e, r)
		}
		raw, e := json.Marshal(r.StructuredContent)
		if e != nil {
			t.Fatal(e)
		}
		var out StoreFactResult
		if e = json.Unmarshal(raw, &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	denied := call(map[string]interface{}{"fact": "synthetic denied MCP assertion", "namespace": "projects"})
	if !denied.Stored || denied.AI != nil || p.writes.Load() != 0 {
		t.Fatal("missing permission did not bypass")
	}
	deniedFalse := call(map[string]interface{}{"fact": "synthetic false MCP assertion", "namespace": "projects", "allow_ai_processing": false})
	if !deniedFalse.Stored || deniedFalse.AI != nil || p.writes.Load() != 0 {
		t.Fatal("false permission did not bypass")
	}
	invalid := toolRequest(map[string]interface{}{"fact": "synthetic invalid MCP assertion", "namespace": "projects", "allow_ai_processing": "true"})
	invalid.Params.Name = "store_fact"
	if r, e := c.CallTool(ctx, invalid); e != nil || !r.IsError {
		t.Fatal("MCP accepted non-boolean permission")
	}
	admitted := call(map[string]interface{}{"fact": "synthetic admitted MCP assertion", "namespace": "projects", "allow_ai_processing": true, "subject_scope": "unknown", "subject_context": "Synthetic alpha component", "source_project": "beta", "source_kind": "client_declared"})
	if !admitted.Stored || admitted.AI == nil || admitted.AI.Applied {
		t.Fatal("shadow store failed")
	}
	a := s.aiState()
	drained := make(chan struct{})
	a.enqueue(func(context.Context) { close(drained) })
	select {
	case <-drained:
	case <-ctx.Done():
		t.Fatal("shadow did not complete")
	}
	if p.writes.Load() != 1 {
		t.Fatal("expected one provider dispatch")
	}
	b.mu.Lock()
	payload := b.stored.Payload
	b.mu.Unlock()
	if payload["primary_tag"] != "" || len(tagsParamFromPayload(payload["tags"])) != 0 {
		t.Fatal("shadow changed grouping")
	}
	if _, ok := payload["allow_ai_processing"]; ok {
		t.Fatal("permission persisted")
	}
	if origin := originPayload(payload); origin == nil || origin.SourceProject != "beta" {
		t.Fatal("origin changed")
	}
	files, err := filepath.Glob(filepath.Join(cfg.StateDir, "write-audits", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal("shadow journal missing")
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var journal aiWriteJournal
	if err = json.Unmarshal(raw, &journal); err != nil || journal.Mode != "shadow" || journal.Status != "decided" || journal.After.Primary != "alpha" || journal.Before.Primary != "" {
		t.Fatal("invalid shadow proposal")
	}
	b.mu.Lock()
	b.points = []qdrant.Point{*b.stored}
	b.points[0].Score = .99
	b.mu.Unlock()
	recallReq := toolRequest(map[string]interface{}{"query": "synthetic admitted MCP assertion", "namespace": "projects", "limit": 1})
	recallReq.Params.Name = "recall_facts"
	recallResult, e := c.CallTool(ctx, recallReq)
	if e != nil || recallResult.IsError {
		t.Fatal("MCP recall failed")
	}
	recallRaw, e := json.Marshal(recallResult.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	var recalled RecallFactsResult
	if e = json.Unmarshal(recallRaw, &recalled); e != nil || len(recalled.Facts) != 1 || recalled.Facts[0].PrimaryTag != "" || recalled.Facts[0].Text != "synthetic admitted MCP assertion" {
		t.Fatal("recall changed shadow fact")
	}
	b.mu.Lock()
	b.points = nil
	b.mu.Unlock()
	cfg.Write.Mode = "off"
	if err = configureAIForTest(t, s, context.Background(), cfg, nil, nil); err != nil {
		t.Fatal(err)
	}
	off := call(map[string]interface{}{"fact": "synthetic off MCP assertion", "namespace": "projects", "allow_ai_processing": true})
	if !off.Stored || off.AI != nil || p.writes.Load() != 1 {
		t.Fatal("off mode dispatched")
	}
}

func TestAIWriteAdmissionLegacyCompatibility(t *testing.T) {
	for _, admission := range []string{"", "operator_policy", "client_opt_in"} {
		t.Run(admission, func(t *testing.T) {
			cfg := aiTestConfig(t, "on", "off")
			cfg.Write.Admission = admission
			b := &aiTestBackend{}
			s := newAIServer(t, b)
			p := &fakeAIProvider{}
			if err := configureAIForTest(t, s, context.Background(), cfg, p, nil); err != nil {
				t.Fatal(err)
			}
			result := aiStoredResult(t, s, map[string]interface{}{"fact": "synthetic legacy permission", "namespace": "projects", "allow_ai_processing": admission == "client_opt_in"})
			if !result.Stored || result.AI == nil || !result.AI.Applied || p.writes.Load() != 1 {
				t.Fatal("legacy operator policy changed")
			}
		})
	}
}
