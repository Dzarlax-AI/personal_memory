package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registrationTestArgs(key, tag string) map[string]interface{} {
	return map[string]interface{}{"namespace": "projects", "project_key": key, "name": "Synthetic " + tag, "tag": tag, "summary": "Synthetic project context", "evidence": []map[string]any{{"kind": "client_readme", "text": "A synthetic service with bounded project evidence."}}}
}
func registryTestConfig(t *testing.T) aipolicy.Config {
	t.Helper()
	cfg := aipolicy.Off()
	cfg.Registration.Mode = "client_declared"
	cfg.CatalogDir = filepath.Join(aiPrivateRoot(t), "registry")
	return cfg
}
func ensureTestProject(t *testing.T, s *Server, args map[string]interface{}) contextcatalog.RegistrationResult {
	t.Helper()
	r, err := s.ensureProject(context.Background(), toolRequest(args))
	if err != nil || r.IsError {
		t.Fatalf("registration: %v %#v", err, r)
	}
	out, ok := r.StructuredContent.(contextcatalog.RegistrationResult)
	if !ok {
		t.Fatalf("registration response %T", r.StructuredContent)
	}
	return out
}
func TestRegistrationCapabilityOffAndRegistryOnly(t *testing.T) {
	s := newAIServer(t, &aiTestBackend{})
	tools := server.NewMCPServer("test", "1")
	s.RegisterTools(tools)
	if tools.GetTool("ensure_project") != nil {
		t.Fatal("off exposes registration")
	}
	if got := ensureTestProject(t, s, nil); got.Status != "disabled" {
		t.Fatal(got)
	}
	cfg := registryTestConfig(t)
	cfg.Profiles = map[string]aipolicy.Profile{"unused": {Endpoint: "bad", KeyFile: "/missing"}}
	if err := s.ConfigureAI(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	a := s.aiState()
	if a.budget != nil || a.jobs != nil || a.read != nil || a.write != nil || cfg.Active() {
		t.Fatal("registry started inference")
	}
	if _, err := os.Stat(cfg.CatalogDir); !os.IsNotExist(err) {
		t.Fatal("startup created registry")
	}
	tools = server.NewMCPServer("test", "1")
	s.RegisterTools(tools)
	if tools.GetTool("ensure_project") == nil {
		t.Fatal("registration capability missing")
	}
	first := ensureTestProject(t, s, registrationTestArgs("synthetic-identity", "alpha"))
	if first.Status != "created" {
		t.Fatal(first)
	}
	repeat := ensureTestProject(t, s, registrationTestArgs("synthetic-identity", "alpha"))
	if repeat.Status != "existing" || repeat.ProjectID != first.ProjectID {
		t.Fatal(repeat)
	}
	changed := registrationTestArgs("synthetic-identity", "alpha")
	changed["summary"] = "Changed purpose"
	if got := ensureTestProject(t, s, changed); got.Status != "proposed_update" {
		t.Fatal(got)
	}
}
func TestRegistrationInputStrictAndUnavailableBaseline(t *testing.T) {
	s := newAIServer(t, &aiTestBackend{})
	cfg := registryTestConfig(t)
	if err := s.ConfigureAI(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(map[string]interface{}){
		func(a map[string]interface{}) { a["owner"] = "other" },
		func(a map[string]interface{}) { a["namespace"] = "work" },
		func(a map[string]interface{}) { a["summary"] = strings.Repeat("x", 2049) },
		func(a map[string]interface{}) { a["evidence"] = "[]" },
		func(a map[string]interface{}) {
			a["evidence"] = []map[string]any{{"kind": "client_readme", "text": "safe", "unknown": "bad"}}
		},
		func(a map[string]interface{}) { a["name"] = 1 },
		func(a map[string]interface{}) { a["summary"] = "/Users/example/private-project" },
	} {
		args := registrationTestArgs("synthetic-identity", "alpha")
		change(args)
		r, err := s.ensureProject(context.Background(), toolRequest(args))
		if err != nil || !r.IsError {
			t.Fatalf("invalid input accepted: %#v %#v", args, r)
		}
	}
	if _, err := os.Stat(cfg.CatalogDir); !os.IsNotExist(err) {
		t.Fatal("invalid input wrote registry")
	}
	if err := os.WriteFile(cfg.CatalogDir, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ensureTestProject(t, s, registrationTestArgs("synthetic-identity", "alpha")); got.Status != "unavailable" {
		t.Fatal(got)
	}
	if got := aiRecallResult(t, s, map[string]interface{}{"query": "baseline"}); got.Count != 0 || got.AI != nil {
		t.Fatal(got)
	}
}
func TestRegistrationConcurrentMCPHandlers(t *testing.T) {
	s := newAIServer(t, &aiTestBackend{})
	cfg := registryTestConfig(t)
	if err := s.ConfigureAI(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.ensureProject(context.Background(), toolRequest(registrationTestArgs("same-identity", "alpha")))
			if err != nil || r.IsError {
				t.Errorf("concurrent ensure %v %#v", err, r)
			}
		}()
	}
	wg.Wait()
	snap, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil || len(snap.Entries) != 1 {
		t.Fatalf("lost/idempotency %v %#v", err, snap)
	}
}
func TestRecallProjectContextScopeCacheAndEgress(t *testing.T) {
	b := &aiTestBackend{points: []qdrant.Point{aiPoint("1", .9, "", false), aiPoint("2", .8, "", false)}}
	b.points[1].Payload["tags"] = []string{"beta"}
	s := newAIServer(t, b)
	cfg := aiTestConfig(t, "off", "on")
	cfg.Registration.Mode = "client_declared"
	cfg.Egress.AllowedProjectTags = append(cfg.Egress.AllowedProjectTags, "gamma")
	provider := &fakeAIProvider{}
	provider.rank = func(ctx context.Context, in aijudgment.RankInput) (aijudgment.RankResult, aijudgment.Usage, error) {
		if in.ProjectContext == nil || in.ProjectContext.Tag != "gamma" || in.ProjectContext.ReviewStatus != "declared" {
			t.Errorf("missing declared context %#v", in.ProjectContext)
		}
		if len(in.Candidates) != 2 || in.Candidates[1].Tags[0] != "beta" {
			t.Error("context filtered candidate pool")
		}
		raw, _ := json.Marshal(in)
		for _, forbidden := range []string{"project_key", "project_id", "client_evidence", "A synthetic service"} {
			if strings.Contains(string(raw), forbidden) {
				t.Errorf("context leaked %s", forbidden)
			}
		}
		return aijudgment.RankResult{Status: "decided", Scores: map[string]float64{"c1": .1, "c2": .9}}, aijudgment.Usage{Known: true, InputTokens: 5}, nil
	}
	if err := configureAIForTest(t, s, context.Background(), cfg, nil, provider); err != nil {
		t.Fatal(err)
	}
	registered := ensureTestProject(t, s, registrationTestArgs("gamma-identity", "gamma"))
	args := map[string]interface{}{"query": "synthetic", "namespace": "projects", "project_context_id": registered.ProjectID}
	got := aiRecallResult(t, s, args)
	if got.Facts[0].PointID != "2" || got.AI == nil || !got.AI.Applied {
		t.Fatal(got)
	}
	current := LifecycleRecallOptions{Mode: RecallLifecycleCurrent}
	if err := s.resolveRecallProject(args, &current); err != nil {
		t.Fatal(err)
	}
	key := recallFactsCacheKey("q", "projects", nil, 5, current)
	if key == recallFactsCacheKey("q", "projects", nil, 5, LifecycleRecallOptions{}) {
		t.Fatal("context cache collision")
	}
	current.CatalogHash = "different"
	if key == recallFactsCacheKey("q", "projects", nil, 5, current) {
		t.Fatal("catalog cache collision")
	}
	foreign, err := contextcatalog.EnsureProject(cfg.CatalogDir, "foreign", contextcatalog.RegistrationInput{ProjectKey: "foreign-identity", Namespace: "projects", Tag: "foreign", Name: "Foreign", Summary: "Different owner", Evidence: []contextcatalog.Evidence{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []any{foreign.ProjectID, strings.Repeat("0", 64), 1, "short"} {
		r, err := s.recallFacts(context.Background(), toolRequest(map[string]interface{}{"query": "q", "project_context_id": id}))
		if err != nil || !r.IsError {
			t.Fatalf("bad context accepted %#v %v %#v", id, err, r)
		}
	}
	// A scoped context excluded by egress must never be sent to a model.
	snap, hash, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	filtered := snap
	filtered.Entries = nil
	for _, e := range snap.Entries {
		if e.Owner != "foreign" {
			filtered.Entries = append(filtered.Entries, e)
		}
	}
	filtered.ParentHash = hash
	filtered.Version = "owner-filtered"
	if _, err := contextcatalog.Publish(cfg.CatalogDir, filtered, hash); err != nil {
		t.Fatal(err)
	}
	cfg.Egress.AllowedProjectTags = []string{"alpha", "beta"}
	if err := configureAIForTest(t, s, context.Background(), cfg, nil, provider); err != nil {
		t.Fatal(err)
	}
	before := provider.reads.Load()
	got = aiRecallResult(t, s, args)
	if provider.reads.Load() != before || got.AI == nil || got.AI.Applied || got.Facts[0].PointID != "1" {
		t.Fatal("disallowed context escaped baseline", got)
	}
}

func TestMCPProjectRegistrationProtocolAndLegacyRecall(t *testing.T) {
	b := &aiTestBackend{points: []qdrant.Point{aiPoint("1", .9, "", false)}}
	s := newAIServer(t, b)
	cfg := registryTestConfig(t)
	if err := s.ConfigureAI(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	mcpServer := server.NewMCPServer("isolated-memory", "1", server.WithToolCapabilities(true))
	s.RegisterTools(mcpServer)
	httpServer := server.NewTestStreamableHTTPServer(mcpServer)
	defer httpServer.Close()
	c, err := client.NewStreamableHttpClient(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION, ClientInfo: mcp.Implementation{Name: "synthetic-client", Version: "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "ensure_project" {
			found = true
			if tool.Annotations.IdempotentHint == nil || !*tool.Annotations.IdempotentHint {
				t.Error("missing idempotency hint")
			}
		}
	}
	if !found {
		t.Fatal("registration not advertised")
	}
	request := toolRequest(registrationTestArgs("protocol-identity", "alpha"))
	request.Params.Name = "ensure_project"
	result, err := c.CallTool(ctx, request)
	if err != nil || result.IsError {
		t.Fatalf("protocol ensure %v %#v", err, result)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	var registration contextcatalog.RegistrationResult
	if json.Unmarshal(raw, &registration) != nil || registration.Status != "created" {
		t.Fatalf("wire registration %s", raw)
	}
	before, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]interface{}{{"query": "synthetic", "namespace": "projects", "project_context_id": registration.ProjectID}, {"query": "synthetic", "namespace": "projects"}} {
		recall := toolRequest(args)
		recall.Params.Name = "recall_facts"
		result, err = c.CallTool(ctx, recall)
		if err != nil || result.IsError {
			t.Fatalf("protocol recall %v %#v", err, result)
		}
		raw, _ = json.Marshal(result.StructuredContent)
		var got RecallFactsResult
		if json.Unmarshal(raw, &got) != nil || got.Count != 1 || got.AI != nil {
			t.Fatalf("wire baseline %s", raw)
		}
	}
	after, _, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("recall mutated registry")
	}
}

func TestShadowReadContextAndCatalogCacheIsolation(t *testing.T) {
	b := &aiTestBackend{points: []qdrant.Point{aiPoint("1", .9, "", false)}}
	s := newAIServer(t, b)
	cfg := aiTestConfig(t, "off", "shadow")
	cfg.Registration.Mode = "client_declared"
	cfg.Egress.AllowedProjectTags = append(cfg.Egress.AllowedProjectTags, "gamma", "delta")
	provider := &fakeAIProvider{}
	seen := make(chan string, 4)
	provider.rank = func(ctx context.Context, in aijudgment.RankInput) (aijudgment.RankResult, aijudgment.Usage, error) {
		if in.ProjectContext == nil {
			t.Error("missing shadow context")
		} else {
			seen <- in.ProjectContext.Tag
		}
		return aijudgment.RankResult{Status: "decided", Scores: map[string]float64{"c1": .9}}, aijudgment.Usage{Known: true, InputTokens: 5}, nil
	}
	if err := configureAIForTest(t, s, context.Background(), cfg, nil, provider); err != nil {
		t.Fatal(err)
	}
	first := ensureTestProject(t, s, registrationTestArgs("gamma-identity", "gamma"))
	second := ensureTestProject(t, s, registrationTestArgs("delta-identity", "delta"))
	for _, project := range []contextcatalog.RegistrationResult{first, second} {
		got := aiRecallResult(t, s, map[string]interface{}{"query": "same query", "namespace": "projects", "project_context_id": project.ProjectID})
		if got.AI != nil || got.Facts[0].PointID != "1" {
			t.Fatal("shadow changed baseline")
		}
		select {
		case tag := <-seen:
			if tag != project.Tag {
				t.Fatalf("mixed shadow context %s %s", tag, project.Tag)
			}
		case <-time.After(time.Second):
			t.Fatal("shadow context request missing")
		}
	}
	// New publication forces a fresh baseline operation for the same context.
	snap, hash, err := contextcatalog.LoadActive(cfg.CatalogDir)
	if err != nil {
		t.Fatal(err)
	}
	snap.Version = "changed-generation"
	snap.ParentHash = hash
	if _, err := contextcatalog.Publish(cfg.CatalogDir, snap, hash); err != nil {
		t.Fatal(err)
	}
	aiRecallResult(t, s, map[string]interface{}{"query": "same query", "namespace": "projects", "project_context_id": second.ProjectID})
	select {
	case tag := <-seen:
		if tag != "delta" {
			t.Fatal(tag)
		}
	case <-time.After(time.Second):
		t.Fatal("new generation reused shadow cache")
	}
	b.mu.Lock()
	searches := b.searches
	b.mu.Unlock()
	if searches != 3 {
		t.Fatalf("context cache mixed searches=%d", searches)
	}
}

func TestRecallContextPreservesFiltersAndProtectedVisibility(t *testing.T) {
	b := &aiTestBackend{points: []qdrant.Point{aiPoint("1", .9, "", false), aiPoint("2", .8, lifecycle.Historical, false), aiPoint("3", .7, "", false), aiPoint("4", .6, "", false)}}
	b.points[2].Payload["valid_until"] = "2020-01-01"
	b.points[3].Payload["maintenance_status"] = "quarantined"
	s := newAIServer(t, b)
	cfg := aiTestConfig(t, "off", "on")
	cfg.Registration.Mode = "client_declared"
	cfg.Egress.AllowedProjectTags = append(cfg.Egress.AllowedProjectTags, "gamma")
	provider := &fakeAIProvider{}
	provider.rank = func(ctx context.Context, in aijudgment.RankInput) (aijudgment.RankResult, aijudgment.Usage, error) {
		if len(in.Candidates) != 1 || in.Candidates[0].Text != "synthetic fact 1" {
			t.Errorf("context changed visibility %#v", in.Candidates)
		}
		return aijudgment.RankResult{Status: "decided", Scores: map[string]float64{"c1": .8}}, aijudgment.Usage{Known: true, InputTokens: 1}, nil
	}
	if err := configureAIForTest(t, s, context.Background(), cfg, nil, provider); err != nil {
		t.Fatal(err)
	}
	registered := ensureTestProject(t, s, registrationTestArgs("gamma-identity", "gamma"))
	var gotFilter map[string]interface{}
	qs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points/search") {
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			gotFilter, _ = body["filter"].(map[string]interface{})
		}
		b.handle(w, r)
	}))
	defer qs.Close()
	s.qdrant = qdrant.NewClient(qs.URL, "memory")
	got := aiRecallResult(t, s, map[string]interface{}{"query": "synthetic", "namespace": "projects", "tags": "alpha", "project_context_id": registered.ProjectID})
	if got.Count != 1 || got.Facts[0].PointID != "1" {
		t.Fatal(got)
	}
	expected := lifecycleRecallFilters(s.buildFilters([]string{"alpha"}, "projects"), RecallLifecycleCurrent)
	rawExpected, _ := json.Marshal(expected)
	rawGot, _ := json.Marshal(gotFilter)
	if string(rawExpected) != string(rawGot) {
		t.Fatalf("context changed filters\nwant %s\ngot %s", rawExpected, rawGot)
	}
	if strings.Contains(string(rawGot), "gamma") || strings.Contains(string(rawGot), registered.ProjectID) {
		t.Fatal("project context leaked into semantic filter")
	}
}
