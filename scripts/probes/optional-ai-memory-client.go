package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/Dzarlax-AI/personal-memory/internal/memory"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func check(e error) {
	if e != nil {
		panic(e)
	}
}
func assert(v bool, s string) {
	if !v {
		panic(s)
	}
}
func main() {
	root, e := os.MkdirTemp("", "memory-release-client-")
	check(e)
	defer os.RemoveAll(root)
	root, e = filepath.EvalSymlinks(root)
	check(e)
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/info" {
			fmt.Fprint(w, `{"model_id":"isolated-protocol-probe","model_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","model_dtype":"float32","model_type":{"embedding":{"pooling":"mean"}},"version":"synthetic"}`)
			return
		}
		var b struct {
			Inputs []string `json:"inputs"`
		}
		check(json.NewDecoder(r.Body).Decode(&b))
		out := [][]float32{}
		for _, s := range b.Inputs {
			h := sha256.Sum256([]byte(s))
			v := make([]float32, 32)
			for i, x := range h {
				v[i] = float32(x)/127.5 - 1
			}
			out = append(out, v)
		}
		check(json.NewEncoder(w).Encode(out))
	}))
	defer embed.Close()
	cfg := aipolicy.Off()
	cfg.Registration.Mode = "client_declared"
	cfg.CatalogDir = filepath.Join(root, "catalog")
	raw, e := json.Marshal(cfg)
	check(e)
	cf := filepath.Join(root, "ai.json")
	check(os.WriteFile(cf, raw, 0600))
	l, e := net.Listen("tcp", "127.0.0.1:0")
	check(e)
	port := l.Addr().(*net.TCPAddr).Port
	check(l.Close())
	url := "http://127.0.0.1:" + strconv.Itoa(port)
	log, e := os.Create(filepath.Join(root, "server.log"))
	check(e)
	defer log.Close()
	cmd := exec.Command(os.Getenv("PROBE_SERVER_BINARY"))
	cmd.Env = []string{"PATH=/usr/bin:/bin", "API_KEY=isolated-protocol-test", "QDRANT_URL=" + os.Getenv("PROBE_QDRANT_URL"), "EMBED_URL=" + embed.URL, "EMBED_MODEL=isolated-protocol-probe", "EMBED_MODEL_REVISION=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "MEMORY_USER=isolated-release", "MCP_PORT=" + strconv.Itoa(port), "MEMORY_AI_CONFIG_FILE=" + cf}
	cmd.Stdout = log
	cmd.Stderr = log
	check(cmd.Start())
	defer func() {
		cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	}()
	ready := false
	for i := 0; i < 100; i++ {
		r, e := http.Get(url + "/health")
		if e == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		v, _ := os.ReadFile(filepath.Join(root, "server.log"))
		panic(string(v))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, e := client.NewStreamableHttpClient(url+"/memory", transport.WithHTTPHeaders(map[string]string{"X-API-Key": "isolated-protocol-test"}))
	check(e)
	check(c.Start(ctx))
	defer c.Close()
	_, e = c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION, ClientInfo: mcp.Implementation{Name: "isolated-release-client", Version: "1"}}})
	check(e)
	ts, e := c.ListTools(ctx, mcp.ListToolsRequest{})
	check(e)
	discovered := map[string]bool{}
	for _, t := range ts.Tools {
		discovered[t.Name] = true
	}
	for _, n := range []string{"store_fact", "recall_facts", "ensure_project"} {
		assert(discovered[n], "tool missing: "+n)
	}
	call := func(name string, args map[string]interface{}, out any) {
		req := mcp.CallToolRequest{}
		req.Params.Name = name
		req.Params.Arguments = args
		r, e := c.CallTool(ctx, req)
		check(e)
		assert(!r.IsError, "tool error: "+name)
		v, e := json.Marshal(r.StructuredContent)
		check(e)
		check(json.Unmarshal(v, out))
	}
	lifeText := "Synthetic user enjoys quiet walks"
	var life memory.StoreFactResult
	call("store_fact", map[string]interface{}{"fact": lifeText, "namespace": "personal", "subject_scope": "non_project"}, &life)
	assert(life.Stored && life.AI == nil && life.SubjectDecision == "non_project", "fresh life write")
	_, e = os.Stat(cfg.CatalogDir)
	assert(os.IsNotExist(e), "life write created registry")
	args := map[string]interface{}{"namespace": "projects", "project_key": "isolated-probe-alpha", "name": "Synthetic Alpha", "tag": "alpha", "summary": "An isolated protocol probe", "evidence": []map[string]interface{}{{"kind": "client_readme", "text": "Synthetic project evidence for isolated protocol testing."}}}
	var first, second contextcatalog.RegistrationResult
	call("ensure_project", args, &first)
	call("ensure_project", args, &second)
	assert(first.Status == "created" && second.Status == "existing" && first.ProjectID == second.ProjectID, "registration idempotency")
	text := "Synthetic component stores assertions"
	var project memory.StoreFactResult
	call("store_fact", map[string]interface{}{"fact": text, "namespace": "projects", "subject_scope": "unknown", "subject_context": "The component stores bounded assertions", "source_project": "alpha", "source_kind": "client_declared"}, &project)
	assert(project.Stored && project.AI == nil, "registry-only project write")
	for _, tc := range []struct {
		ns, text string
		origin   bool
	}{{"personal", lifeText, false}, {"projects", text, true}} {
		var recall memory.RecallFactsResult
		call("recall_facts", map[string]interface{}{"namespace": tc.ns, "query": tc.text, "limit": 1}, &recall)
		assert(len(recall.Facts) == 1, "recall missing")
		f := recall.Facts[0]
		assert(f.Text == tc.text && f.Namespace == tc.ns && f.PrimaryTag == "" && f.Subject != nil, "recall metadata changed")
		if tc.origin {
			assert(f.Origin != nil && f.Origin.SourceProject == "alpha" && f.Subject.Scope == "unknown", "project origin or subject missing")
		} else {
			assert(f.Origin == nil && f.Subject.Scope == "non_project", "life origin fabricated")
		}
	}
	v, e := json.MarshalIndent(map[string]any{"status": "passed", "checks": []string{"server binary health", "authenticated MCP initialization and tool discovery", "life write before registry", "registration created/existing", "registry-only project write", "life and project recall metadata"}, "storage": "isolated real Qdrant", "embeddings": "deterministic synthetic HTTP stub", "external_model_calls": 0, "production_mutations": 0, "client": "Go MCP HTTP client; not proprietary client installation"}, "", "  ")
	check(e)
	check(os.WriteFile("eval-results/optional-ai-memory-v2/release-preparation/client-path.json", v, 0600))
	fmt.Println(string(v))
}
