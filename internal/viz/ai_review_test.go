package viz

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"github.com/Dzarlax-AI/personal-memory/internal/middleware"
)

func TestAIReviewDisabledSchedulerKeepsRetainedHistoryReadable(t *testing.T) {
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := aipolicy.Off()
	cfg.StateDir = state
	cfg.Maintenance.Scope = "catalog_only"
	h := NewHandler(nil, 0).WithAIReview(cfg, "")
	r := httptest.NewRecorder()
	h.Router().ServeHTTP(r, httptest.NewRequest("GET", "/api/ai-review/status", nil))
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status: %d %s", r.Code, r.Body.String())
	}
	var got aiReviewStatus
	if json.Unmarshal(r.Body.Bytes(), &got) != nil || got.Availability != "available" || got.History != "missing" || got.AssessmentWritable {
		t.Fatalf("unexpected status: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(state, "maintenance-runs")); !os.IsNotExist(err) {
		t.Fatalf("read created run history: %v", err)
	}
	for _, target := range []string{"/api/ai-review/runs?limit=1&limit=2", "/api/ai-review/runs?from=bad", "/api/ai-review/runs?from=2026-10-02T00:00:00Z&from=2026-10-03T00:00:00Z"} {
		bad := httptest.NewRecorder()
		h.Router().ServeHTTP(bad, httptest.NewRequest("GET", target, nil))
		if bad.Code != http.StatusBadRequest {
			t.Errorf("accepted invalid review query %q: %d", target, bad.Code)
		}
	}
}

func TestAIReviewEndpointsRequireOuterProxySecret(t *testing.T) {
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := aipolicy.Off()
	cfg.StateDir = state
	cfg.Maintenance.Scope = "catalog_only"
	h := NewHandler(nil, 0).WithAIReview(cfg, "https://mcp.example.test")
	protected := middleware.ProxySecretAuth("proxy-secret")(h.Router())
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/ai-review/status"},
		{http.MethodGet, "/api/ai-review/runs"},
		{http.MethodPut, "/api/ai-review/runs/20261014T120000000000000Z-0123456789abcdef0123456789abcdef-00000001/drafts/" + strings.Repeat("a", 64) + "/assessments/" + strings.Repeat("b", 64)},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"expected_revision":0}`))
		req.Header.Set("Origin", "https://mcp.example.test")
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		protected.ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s %s: got %d", tc.method, tc.path, resp.Code)
		}
	}
	authorized := httptest.NewRequest("GET", "/api/ai-review/status", nil)
	authorized.Header.Set(middleware.VizProxySecretHeader, "proxy-secret")
	resp := httptest.NewRecorder()
	protected.ServeHTTP(resp, authorized)
	if resp.Code != http.StatusOK {
		t.Fatalf("proxy-authorized read: %d", resp.Code)
	}
}

func TestAIReviewRejectsMalformedRunIDsBeforeHistoryLookup(t *testing.T) {
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := aipolicy.Off()
	cfg.StateDir = state
	cfg.Maintenance.Scope = "catalog_only"
	h := NewHandler(nil, 0).WithAIReview(cfg, "https://mcp.example.test")
	router := h.Router()
	digest := strings.Repeat("a", 64)
	for _, target := range []string{
		"/api/ai-review/runs/bad",
		"/api/ai-review/runs/bad/drafts/" + digest,
		"/api/ai-review/runs/bad/drafts/" + digest + "/assessments",
	} {
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, target, nil))
		if resp.Code != http.StatusBadRequest || resp.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("malformed GET %s: status=%d cache=%q", target, resp.Code, resp.Header().Get("Cache-Control"))
		}
	}
	put := httptest.NewRequest(http.MethodPut, "https://mcp.example.test/api/ai-review/runs/bad/drafts/"+digest+"/assessments/"+strings.Repeat("b", 64), strings.NewReader(`{}`))
	put.Header.Set("Origin", "https://mcp.example.test")
	put.Header.Set("Content-Type", "application/json")
	putResponse := httptest.NewRecorder()
	router.ServeHTTP(putResponse, put)
	if putResponse.Code != http.StatusBadRequest || putResponse.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("malformed PUT: status=%d cache=%q", putResponse.Code, putResponse.Header().Get("Cache-Control"))
	}
}

func TestAIReviewRunsFilteredScanClearsCursorAfterExhaustion(t *testing.T) {
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := strings.Repeat("1", 32)
	for i := 1; i <= 101; i++ {
		startedAt := time.Date(2026, 10, 14, 12, 0, i-1, 0, time.UTC)
		runID := fmt.Sprintf("%s-%s-%08d", startedAt.Format("20060102T150405000000000Z"), session, i)
		runDir := filepath.Join(state, "maintenance-runs", runID)
		if err := os.MkdirAll(filepath.Join(runDir, "events"), 0700); err != nil {
			t.Fatal(err)
		}
		started := map[string]any{"schema_version": 1, "run_id": runID, "session_id": session, "sequence": i, "started_at": startedAt.Format(time.RFC3339Nano), "scope": "catalog_only", "project_policy": "registered", "publication": "review"}
		b, _ := json.Marshal(started)
		if err := os.WriteFile(filepath.Join(runDir, "started.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := aipolicy.Off()
	cfg.StateDir = state
	h := NewHandler(nil, 0).WithAIReview(cfg, "")
	target := "/api/ai-review/runs?limit=100&from=2026-10-14T12:01:40Z&to=2026-10-14T12:01:40Z"
	resp := httptest.NewRecorder()
	h.Router().ServeHTTP(resp, httptest.NewRequest("GET", target, nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("filtered runs: %d %s", resp.Code, resp.Body.String())
	}
	var page aiReviewRunsResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 1 || page.NextCursor != "" || !page.Complete || page.Scanned != 101 {
		t.Fatalf("filtered scan reported incomplete/stale cursor: %+v", page)
	}
}

func TestAIReviewProductionShapedDraftAndAssessmentAPI(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalogDir := filepath.Join(root, "catalog")
	stateDir := filepath.Join(root, "state")
	identityDigest := sha256.Sum256([]byte("client\x00projects\x00personal-memory"))
	projectID := hex.EncodeToString(identityDigest[:])
	baseEntry := contextcatalog.Entry{Namespace: "projects", Tag: "personal-memory", Name: "Memory", Summary: "Base declaration.", Aliases: []string{}, OwnedComponents: []string{}, Boundaries: []string{}, Uses: []string{}, SharedWith: []string{}, PositiveExamples: []string{}, NegativeExamples: []string{}, EvidenceRefs: []string{}, ReviewStatus: "approved", ProjectID: projectID, ProjectKey: "personal-memory", Owner: "client", DescriptionSource: "client_declared", DeclaredSummary: "Original source declaration.", ClientEvidence: []contextcatalog.Evidence{{Kind: "client_readme", Text: "Repository context excerpt."}}}
	base := contextcatalog.Snapshot{SchemaVersion: 1, Version: "1.0.0", CreatedAt: "2026-10-10T00:00:00Z", Entries: []contextcatalog.Entry{baseEntry}}
	baseHash, err := contextcatalog.Publish(catalogDir, base, "")
	if err != nil {
		t.Fatal(err)
	}
	projected := baseEntry
	projected.Summary = "Projection from registered original context."
	all := base
	all.Entries = []contextcatalog.Entry{projected}
	original := base
	original.Entries = []contextcatalog.Entry{baseEntry}
	declaredDigest := sha256.Sum256([]byte("Original source declaration."))
	proposalEntry := contextcatalog.Entry{Namespace: "projects", Tag: "personal-memory", Name: "Memory", Summary: "Model proposed summary.", Aliases: []string{}, OwnedComponents: []string{"review adapter"}, Boundaries: []string{}, Uses: []string{}, SharedWith: []string{}, PositiveExamples: []string{}, NegativeExamples: []string{}, EvidenceRefs: []string{"c000", "/Users/private/file:" + strings.Repeat("f", 64)}, ReviewStatus: "draft"}
	proposal := contextcatalog.Snapshot{SchemaVersion: 1, Version: "1.0.0", ParentHash: baseHash, CreatedAt: base.CreatedAt, Entries: []contextcatalog.Entry{proposalEntry}}
	artifact := map[string]any{"schema_version": 1, "created_at": "2026-10-14T12:00:00Z", "provider_protocol": "fake", "model": "test-model", "maintenance_project_policy": "registered", "relationship_instructions": "fixture", "base_catalog_hash": baseHash, "binding_hash": strings.Repeat("b", 64), "proposal": map[string]any{"schema_version": 1, "base_catalog_hash": baseHash, "catalog": proposal, "missing_evidence": []string{}}, "dependencies": []any{}, "catalog_dependencies": []any{map[string]any{"alias": "c000", "project_id": projectID, "owner": "client", "project_key": "personal-memory", "namespace": "projects", "tag": "personal-memory", "kind": "user_declared", "digest": hex.EncodeToString(declaredDigest[:])}}, "grouping_manifest": map[string]any{}, "catalog_original": original, "catalog_original_context": all, "catalog_published": []any{}}
	raw, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	filename := "proposal-" + digest + ".json"
	proposalDir := filepath.Join(stateDir, "maintenance", "proposals")
	if err := os.MkdirAll(proposalDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proposalDir, filename), raw, 0600); err != nil {
		t.Fatal(err)
	}
	runID := "20261014T120000000000000Z-0123456789abcdef0123456789abcdef-00000001"
	runDir := filepath.Join(stateDir, "maintenance-runs", runID)
	eventDir := filepath.Join(runDir, "events")
	if err := os.MkdirAll(eventDir, 0700); err != nil {
		t.Fatal(err)
	}
	started := map[string]any{"schema_version": 1, "run_id": runID, "session_id": "0123456789abcdef0123456789abcdef", "sequence": 1, "started_at": "2026-10-14T12:00:00Z", "scope": "catalog_only", "project_policy": "registered", "publication": "review", "catalog_hash": baseHash}
	event := map[string]any{"schema_version": 1, "run_id": runID, "session_id": "0123456789abcdef0123456789abcdef", "sequence": 1, "at": "2026-10-14T12:00:01Z", "type": "draft_saved", "filename": filename, "sha256": digest, "draft_status": "created"}
	for path, value := range map[string]any{filepath.Join(runDir, "started.json"): started, filepath.Join(eventDir, "0001.json"): event} {
		b, _ := json.Marshal(value)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	current := base
	current.ParentHash = baseHash
	current.Version = "2.0.0"
	current.Entries = []contextcatalog.Entry{baseEntry}
	current.Entries[0].Summary = "Current manual edit."
	currentHash, err := contextcatalog.Publish(catalogDir, current, baseHash)
	if err != nil {
		t.Fatal(err)
	}
	cfg := aipolicy.Config{StateDir: stateDir, CatalogDir: catalogDir, Maintenance: aipolicy.Maintenance{Scope: "catalog_only", CatalogPublish: "review"}}
	h := NewHandler(nil, 0).WithAIReview(cfg, "https://mcp.example.test")
	router := h.Router()
	get := httptest.NewRecorder()
	router.ServeHTTP(get, httptest.NewRequest("GET", fmt.Sprintf("/api/ai-review/runs/%s/drafts/%s", runID, digest), nil))
	if get.Code != 200 {
		t.Fatalf("draft %d %s", get.Code, get.Body.String())
	}
	var draft aiReviewDraft
	if err := json.Unmarshal(get.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	if len(draft.Projects) != 1 {
		t.Fatalf("projects: %+v", draft.Projects)
	}
	p := draft.Projects[0]
	if p.ProjectID != projectID || p.Base.Summary != "Base declaration." || p.Original == nil || p.Original.Summary != "Base declaration." || p.Proposed.Summary != "Model proposed summary." || len(p.Proposed.EvidenceRefs) != 2 || p.Proposed.EvidenceRefs[0].Ref != "c000" || !p.Proposed.EvidenceRefs[0].Available || p.Proposed.EvidenceRefs[1].Ref != "unavailable" || p.Proposed.EvidenceRefs[1].Available || p.Current == nil || p.Current.Summary != "Current manual edit." || !p.Drift {
		t.Fatalf("bad comparison: %+v", p)
	}

	// A source artifact with changed client evidence must not leak its source
	// excerpt or appear as a verified original, even when the project ID agrees.
	var mismatched map[string]any
	if err := json.Unmarshal(raw, &mismatched); err != nil {
		t.Fatal(err)
	}
	originalWithMismatch := original
	originalWithMismatch.Entries = append([]contextcatalog.Entry(nil), original.Entries...)
	originalWithMismatch.Entries[0].ClientEvidence = []contextcatalog.Evidence{{Kind: "client_readme", Text: "Different registered source excerpt."}}
	mismatched["catalog_original"] = originalWithMismatch
	mismatchRaw, err := json.Marshal(mismatched)
	if err != nil {
		t.Fatal(err)
	}
	mismatchSum := sha256.Sum256(mismatchRaw)
	mismatchDigest := hex.EncodeToString(mismatchSum[:])
	mismatchFilename := "proposal-" + mismatchDigest + ".json"
	if err := os.WriteFile(filepath.Join(proposalDir, mismatchFilename), mismatchRaw, 0600); err != nil {
		t.Fatal(err)
	}
	event["filename"], event["sha256"] = mismatchFilename, mismatchDigest
	b, _ := json.Marshal(event)
	if err := os.WriteFile(filepath.Join(eventDir, "0001.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	mismatchRunID := runID
	mismatchResponse := httptest.NewRecorder()
	router.ServeHTTP(mismatchResponse, httptest.NewRequest("GET", fmt.Sprintf("/api/ai-review/runs/%s/drafts/%s", mismatchRunID, mismatchDigest), nil))
	if mismatchResponse.Code != http.StatusOK {
		_, _, loadErr := h.loadAIDraft(mismatchRunID, mismatchDigest)
		t.Fatalf("mismatch draft %d %s (%v)", mismatchResponse.Code, mismatchResponse.Body.String(), loadErr)
	}
	var mismatchDraft aiReviewDraft
	if err := json.Unmarshal(mismatchResponse.Body.Bytes(), &mismatchDraft); err != nil {
		t.Fatal(err)
	}
	if len(mismatchDraft.Projects) != 1 || mismatchDraft.Projects[0].Original != nil || mismatchDraft.Projects[0].IdentityStatus != "mismatch" || len(mismatchDraft.Evidence) != 0 {
		t.Fatalf("unverified source was exposed: %+v", mismatchDraft)
	}
	event["filename"], event["sha256"] = filename, digest
	b, _ = json.Marshal(event)
	if err := os.WriteFile(filepath.Join(eventDir, "0001.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(assessmentRequest{ExpectedRevision: 0, Verdict: "acceptable", Reason: "none", Note: "reviewed", BaseEntryHash: p.BaseEntryHash, ProposedEntryHash: p.ProposedEntryHash, CurrentEntryHash: p.CurrentEntryHash})
	putURL := fmt.Sprintf("https://mcp.example.test/api/ai-review/runs/%s/drafts/%s/assessments/%s", runID, digest, projectID)
	for _, tc := range []struct{ name, origin, contentType string }{
		{name: "missing_origin", contentType: "application/json"},
		{name: "wrong_origin", origin: "https://evil.example", contentType: "application/json"},
		{name: "wrong_content_type", origin: "https://mcp.example.test", contentType: "text/plain"},
	} {
		req := httptest.NewRequest(http.MethodPut, putURL, strings.NewReader(string(encoded)))
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Content-Type", tc.contentType)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusForbidden {
			t.Errorf("%s: got %d want 403", tc.name, resp.Code)
		}
	}
	for _, tc := range []struct{ name, body string }{
		{name: "unknown_field", body: strings.TrimSuffix(string(encoded), "}") + `,"unexpected":true}`},
		{name: "body_too_large", body: strings.Repeat(" ", 8193)},
	} {
		req := httptest.NewRequest(http.MethodPut, putURL, strings.NewReader(tc.body))
		req.Header.Set("Origin", "https://mcp.example.test")
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d want 400", tc.name, resp.Code)
		}
	}
	for _, path := range []string{
		fmt.Sprintf("https://mcp.example.test/api/ai-review/runs/%s/drafts/%s/assessments/%s", "20261014T120000000000000Z-0123456789abcdef0123456789abcdef-00000099", digest, projectID),
		fmt.Sprintf("https://mcp.example.test/api/ai-review/runs/%s/drafts/%s/assessments/%s", runID, strings.Repeat("c", 64), projectID),
		fmt.Sprintf("https://mcp.example.test/api/ai-review/runs/%s/drafts/%s/assessments/%s", runID, digest, strings.Repeat("d", 64)),
	} {
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(string(encoded)))
		req.Header.Set("Origin", "https://mcp.example.test")
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusNotFound {
			t.Errorf("unlinked assessment path: got %d want 404", resp.Code)
		}
	}
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-2] ^= 1
	if err := os.WriteFile(filepath.Join(proposalDir, filename), tampered, 0600); err != nil {
		t.Fatal(err)
	}
	tamperedResponse := httptest.NewRecorder()
	router.ServeHTTP(tamperedResponse, httptest.NewRequest("GET", fmt.Sprintf("/api/ai-review/runs/%s/drafts/%s", runID, digest), nil))
	if tamperedResponse.Code != http.StatusNotFound {
		t.Fatalf("tampered artifact: %d", tamperedResponse.Code)
	}
	if err := os.WriteFile(filepath.Join(proposalDir, filename), raw, 0600); err != nil {
		t.Fatal(err)
	}
	put := httptest.NewRequest(http.MethodPut, putURL, strings.NewReader(string(encoded)))
	put.Header.Set("Origin", "https://mcp.example.test")
	put.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, put)
	if response.Code != 200 {
		t.Fatalf("save %d %s", response.Code, response.Body.String())
	}
	unchanged, unchangedHash, err := contextcatalog.LoadActive(catalogDir)
	if err != nil || unchangedHash != currentHash || unchanged.Entries[0].Summary != "Current manual edit." {
		t.Fatalf("assessment changed catalog: %s %v", unchangedHash, err)
	}
	var saved map[string]any
	if json.Unmarshal(response.Body.Bytes(), &saved) != nil || saved["revision"] != float64(1) {
		t.Fatalf("saved: %s", response.Body.String())
	}
	basePath := filepath.Join(catalogDir, "snapshots", baseHash+".json")
	baseBytes, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	assertUnavailableBase := func(label string) {
		t.Helper()
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, httptest.NewRequest("GET", fmt.Sprintf("/api/ai-review/runs/%s/drafts/%s", runID, digest), nil))
		var got aiReviewDraft
		if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &got) != nil || got.AssessmentWritable || !hasReviewCode(got.Missing, "base_snapshot_unavailable") {
			t.Fatalf("%s baseline state: %d %+v", label, resp.Code, got)
		}
	}
	if err := os.Remove(basePath); err != nil {
		t.Fatal(err)
	}
	assertUnavailableBase("missing")
	if err := os.WriteFile(basePath, []byte("corrupt snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	assertUnavailableBase("corrupt")
	if err := os.WriteFile(basePath, baseBytes, 0600); err != nil {
		t.Fatal(err)
	}
	drifted := current
	drifted.ParentHash = currentHash
	drifted.Version = "3.0.0"
	drifted.Entries = append([]contextcatalog.Entry(nil), current.Entries...)
	drifted.Entries[0].Summary = "Later catalog change."
	if _, err := contextcatalog.Publish(catalogDir, drifted, currentHash); err != nil {
		t.Fatal(err)
	}
	assessmentGet := httptest.NewRecorder()
	router.ServeHTTP(assessmentGet, httptest.NewRequest("GET", fmt.Sprintf("/api/ai-review/runs/%s/drafts/%s/assessments", runID, digest), nil))
	if assessmentGet.Code != 200 || !strings.Contains(assessmentGet.Body.String(), "\"drift\":true") {
		t.Fatalf("assessment read: %d %s", assessmentGet.Code, assessmentGet.Body.String())
	}
	for _, secret := range []string{"ProjectKey", "client\"", "/Users/"} {
		if strings.Contains(get.Body.String(), secret) {
			t.Fatalf("draft leaked %q", secret)
		}
	}
	noOpProposal := map[string]any{"schema_version": 1, "base_catalog_hash": baseHash, "catalog": nil, "missing_evidence": []string{}}
	noOpArtifact := make(map[string]any, len(artifact))
	for k, v := range artifact {
		noOpArtifact[k] = v
	}
	noOpArtifact["proposal"] = noOpProposal
	noOpRaw, _ := json.Marshal(noOpArtifact)
	noOpSum := sha256.Sum256(noOpRaw)
	noOpDigest := hex.EncodeToString(noOpSum[:])
	noOpFilename := "proposal-" + noOpDigest + ".json"
	if err := os.WriteFile(filepath.Join(proposalDir, noOpFilename), noOpRaw, 0600); err != nil {
		t.Fatal(err)
	}
	noOpRunID := "20261014T120001000000000Z-0123456789abcdef0123456789abcdef-00000002"
	noOpRunDir := filepath.Join(stateDir, "maintenance-runs", noOpRunID)
	noOpEvents := filepath.Join(noOpRunDir, "events")
	if err := os.MkdirAll(noOpEvents, 0700); err != nil {
		t.Fatal(err)
	}
	noOpStarted := map[string]any{"schema_version": 1, "run_id": noOpRunID, "session_id": "0123456789abcdef0123456789abcdef", "sequence": 2, "started_at": "2026-10-14T12:00:01Z", "scope": "catalog_only", "project_policy": "registered", "publication": "review", "catalog_hash": baseHash}
	noOpEvent := map[string]any{"schema_version": 1, "run_id": noOpRunID, "session_id": "0123456789abcdef0123456789abcdef", "sequence": 1, "at": "2026-10-14T12:00:02Z", "type": "draft_saved", "filename": noOpFilename, "sha256": noOpDigest, "draft_status": "created"}
	for path, value := range map[string]any{filepath.Join(noOpRunDir, "started.json"): noOpStarted, filepath.Join(noOpEvents, "0001.json"): noOpEvent} {
		b, _ := json.Marshal(value)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	noOpGet := httptest.NewRecorder()
	router.ServeHTTP(noOpGet, httptest.NewRequest("GET", fmt.Sprintf("/api/ai-review/runs/%s/drafts/%s", noOpRunID, noOpDigest), nil))
	var noOpDraft aiReviewDraft
	if noOpGet.Code != 200 || json.Unmarshal(noOpGet.Body.Bytes(), &noOpDraft) != nil || noOpDraft.DraftStatus != "no_changes" || len(noOpDraft.Projects) != 0 {
		t.Fatalf("no-op draft: %d %s", noOpGet.Code, noOpGet.Body.String())
	}
	approvedProposal := proposal
	approvedProposal.Entries = append([]contextcatalog.Entry(nil), proposal.Entries...)
	approvedProposal.Entries[0].ReviewStatus = "approved"
	for _, invalid := range []map[string]any{
		{"schema_version": 1, "base_catalog_hash": baseHash, "catalog": nil, "missing_evidence": []string{"not_a_worker_code"}},
		{"schema_version": 1, "base_catalog_hash": baseHash, "catalog": nil, "grouping": []any{map[string]any{"alias": "f000"}}, "missing_evidence": []string{}},
		{"schema_version": 1, "base_catalog_hash": baseHash, "catalog": approvedProposal, "missing_evidence": []string{}},
	} {
		badArtifact := make(map[string]any, len(artifact))
		for k, v := range artifact {
			badArtifact[k] = v
		}
		badArtifact["proposal"] = invalid
		badRaw, _ := json.Marshal(badArtifact)
		badSum := sha256.Sum256(badRaw)
		badDigest := hex.EncodeToString(badSum[:])
		badFilename := "proposal-" + badDigest + ".json"
		if err := os.WriteFile(filepath.Join(proposalDir, badFilename), badRaw, 0600); err != nil {
			t.Fatal(err)
		}
		badEvent := map[string]any{"schema_version": 1, "run_id": noOpRunID, "session_id": "0123456789abcdef0123456789abcdef", "sequence": 1, "at": "2026-10-14T12:00:02Z", "type": "draft_saved", "filename": badFilename, "sha256": badDigest, "draft_status": "created"}
		badEventBytes, _ := json.Marshal(badEvent)
		if err := os.WriteFile(filepath.Join(noOpEvents, "0001.json"), badEventBytes, 0600); err != nil {
			t.Fatal(err)
		}
		badResponse := httptest.NewRecorder()
		router.ServeHTTP(badResponse, httptest.NewRequest("GET", fmt.Sprintf("/api/ai-review/runs/%s/drafts/%s", noOpRunID, badDigest), nil))
		if badResponse.Code != http.StatusNotFound {
			t.Errorf("accepted invalid no-change proposal: %d %s", badResponse.Code, badResponse.Body.String())
		}
	}
	noOpEventBytes, _ := json.Marshal(noOpEvent)
	if err := os.WriteFile(filepath.Join(noOpEvents, "0001.json"), noOpEventBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(proposalDir, noOpFilename)); err != nil {
		t.Fatal(err)
	}
	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest("GET", fmt.Sprintf("/api/ai-review/runs/%s/drafts/%s", noOpRunID, noOpDigest), nil))
	if missing.Code != 404 || missing.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing linked artifact: %d %s", missing.Code, missing.Body.String())
	}
}

func TestReviewConfiguredOriginRejectsProxyBasedAndNonLocalHTTP(t *testing.T) {
	for _, origin := range []string{"", "http://example.com", "https://example.com/path", "https://user@example.com", "http://[::1]"} {
		if validConfiguredReviewOrigin(origin) {
			t.Errorf("accepted origin %q", origin)
		}
	}
	for _, origin := range []string{"https://mcp.example.com", "http://127.0.0.1:8443", "http://localhost:8080"} {
		if !validConfiguredReviewOrigin(origin) {
			t.Errorf("rejected origin %q", origin)
		}
	}
	r := httptest.NewRequest("PUT", "https://mcp.example.com/x", nil)
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("X-Forwarded-Host", "mcp.example.com")
	if validReviewOrigin(r, "https://mcp.example.com") {
		t.Fatal("trusted forwarded headers over Origin")
	}
}

func TestSafeReviewReferencesRejectPrivatePaths(t *testing.T) {
	refs := safeReviewReferences([]string{"c000", "client_readme:" + strings.Repeat("a", 64), "/Users/private/file:" + strings.Repeat("b", 64), "f001"}, map[string]reviewEvidence{
		"c000": {Alias: "c000", Kind: "client_readme", Digest: strings.Repeat("a", 64), Available: true},
	})
	if len(refs) != 4 || refs[0].Ref != "c000" || !refs[0].Available || refs[1].Ref != "client_readme:"+strings.Repeat("a", 64) || !refs[1].Available || refs[2].Ref != "unavailable" || refs[2].Available || refs[3].Ref != "unavailable" {
		t.Fatalf("unsafe or unresolved references: %+v", refs)
	}
}

func hasReviewCode(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
