package viz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/go-chi/chi/v5"
)

func TestGraphAndDuplicatesRejectInvalidBoundsBeforeLoadingPoints(t *testing.T) {
	h := NewHandler(nil, 0.65)
	tests := []string{
		"/api/graph?threshold=NaN",
		"/api/graph?threshold=-0.1",
		"/api/graph?max_edges=0",
		"/api/graph?max_edges=5001",
		"/api/graph?max_nodes=0",
		"/api/duplicates?threshold=Infinity",
		"/api/duplicates?max_nodes=5001",
		"/api/duplicates?max_pairs=-1",
	}
	for _, target := range tests {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("GET %s: got %d (%s), want 400", target, rr.Code, rr.Body.String())
		}
	}
}

func TestGraphAndDuplicatesRejectDatasetsOverMaxNodes(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[`)
		for i := 0; i < 3; i++ {
			if i > 0 {
				mustWriteTestResponse(t, w, ",")
			}
			mustWriteTestResponse(t, w, `{"id":"%d","vector":[1,0],"payload":{"text":"fact %d"}}`, i, i)
		}
		mustWriteTestResponse(t, w, `],"next_page_offset":null}}`)
	}))
	defer backend.Close()

	h := NewHandler(qdrant.NewClient(backend.URL, "memory"), 0.65)
	for _, target := range []string{"/api/graph?max_nodes=2", "/api/duplicates?max_nodes=2"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, req)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("GET %s: got %d (%s), want 422", target, rr.Code, rr.Body.String())
		}
	}
}

func mustWriteTestResponse(t *testing.T, w http.ResponseWriter, format string, args ...interface{}) {
	t.Helper()
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		t.Fatalf("write test response: %v", err)
	}
}

func TestFactListGraphAndDuplicateSummariesHidePayload(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[{"id":"one","vector":[1,0],"payload":{"text":"visible fact","namespace":"projects","tags":["personal-memory"],"primary_tag":"personal-memory","created_at":"2026-07-20T00:00:00Z","permanent":true,"recall_count":4,"secret":"must not leave the summary"}},{"id":"two","vector":[1,0],"payload":{"text":"second fact","secret":"must not leave the summary"}}],"next_page_offset":null}}`)
	}))
	defer backend.Close()

	h := NewHandler(qdrant.NewClient(backend.URL, "memory"), 0.65)
	for _, target := range []string{
		"/api/facts",
		"/api/graph?threshold=0.5",
		"/api/duplicates?threshold=0.5",
	} {
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s: got %d (%s), want 200", target, rr.Code, rr.Body.String())
		}
		assertJSONOmitsKeys(t, rr.Body.Bytes(), "payload", "payload_keys", "text_source", "secret")
		assertJSONFactSummariesHaveLegacyLifecycle(t, rr.Body.Bytes(), target)
	}
}

func TestFactSummaryAndDetailJSONContracts(t *testing.T) {
	payload := map[string]interface{}{
		"text": "visible fact", "namespace": "projects", "tags": []interface{}{"personal-memory"},
		"primary_tag": "personal-memory", "created_at": "2026-07-20T00:00:00Z",
		"permanent": true, "recall_count": float64(4), "secret": "detail only",
	}
	summaryBody, err := json.Marshal(pointToSummary(qdrant.ScrollPoint{ID: "fact-1", Payload: payload}))
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	assertJSONOmitsKeys(t, summaryBody, "payload", "payload_keys", "text_source", "secret")
	var summary map[string]interface{}
	if err := json.Unmarshal(summaryBody, &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if len(summary) != 10 {
		t.Fatalf("summary keys = %#v, want exactly the 10 approved fields", summary)
	}

	detailBody, err := json.Marshal(pointToDetail(qdrant.Point{ID: "fact-1", Payload: payload}))
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	var detail map[string]interface{}
	if err := json.Unmarshal(detailBody, &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if _, ok := detail["payload"].(map[string]interface{}); !ok {
		t.Fatalf("detail payload = %#v, want object", detail["payload"])
	}
	if _, ok := detail["payload_keys"].([]interface{}); !ok {
		t.Fatalf("detail payload_keys = %#v, want array", detail["payload_keys"])
	}
	if lifecycleView, ok := detail["lifecycle"].(map[string]interface{}); !ok || lifecycleView["state"] != "current" || lifecycleView["legacy"] != true {
		t.Fatalf("detail lifecycle = %#v, want normalized legacy current", detail["lifecycle"])
	}
}

func TestFactSummaryNormalizesLifecycleMetadata(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		payload map[string]interface{}
		want    map[string]interface{}
	}{
		{
			name:    "legacy missing state",
			id:      "legacy-id",
			payload: map[string]interface{}{"text": "legacy"},
			want: map[string]interface{}{
				"state": "current", "legacy": true, "canonical": false,
				"supersedes": []interface{}{}, "superseded_by": []interface{}{}, "valid": true,
			},
		},
		{
			name: "explicit canonical current",
			id:   "current-id",
			payload: map[string]interface{}{
				"lifecycle_state": "current", "canonical": true,
				"provenance":  map[string]interface{}{"source": "user", "reference": "decision-7"},
				"verified_at": "2026-07-21T08:30:00Z",
				"supersedes":  []interface{}{"old-id", float64(42)},
			},
			want: map[string]interface{}{
				"state": "current", "legacy": false, "canonical": true,
				"provenance":  map[string]interface{}{"source": "user", "reference": "decision-7"},
				"verified_at": "2026-07-21T08:30:00Z", "supersedes": []interface{}{"old-id", "42"},
				"superseded_by": []interface{}{}, "valid": true,
			},
		},
		{
			name: "malformed explicit metadata",
			id:   "invalid-id",
			payload: map[string]interface{}{
				"text": "private fact text must not appear in reason", "lifecycle_state": "current", "canonical": "yes",
			},
			want: map[string]interface{}{
				"state": "current", "legacy": false, "canonical": false,
				"supersedes": []interface{}{}, "superseded_by": []interface{}{}, "valid": false,
				"invalid_reason": "canonical must be a boolean",
			},
		},
		{
			name:    "unknown explicit state",
			id:      "unknown-state-id",
			payload: map[string]interface{}{"lifecycle_state": "unknown"},
			want: map[string]interface{}{
				"state": "unknown", "legacy": false, "canonical": false,
				"supersedes": []interface{}{}, "superseded_by": []interface{}{}, "valid": false,
				"invalid_reason": "lifecycle_state must be current, historical, superseded, or disputed",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(pointToSummary(qdrant.ScrollPoint{ID: test.id, Payload: test.payload}))
			if err != nil {
				t.Fatalf("marshal summary: %v", err)
			}
			var summary map[string]interface{}
			if err := json.Unmarshal(body, &summary); err != nil {
				t.Fatalf("decode summary: %v", err)
			}
			if got := summary["lifecycle"]; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("lifecycle = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestFactDetailAddsLifecycleWithoutMutatingRawPayload(t *testing.T) {
	payload := map[string]interface{}{
		"text": "selected detail", "lifecycle_state": "current", "canonical": true,
		"provenance": map[string]interface{}{"source": "import"}, "secret": "detail only",
	}
	wantPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal input payload: %v", err)
	}

	body, err := json.Marshal(pointToDetail(qdrant.Point{ID: "fact-id", Payload: payload}))
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	var detail map[string]interface{}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	gotPayload, err := json.Marshal(detail["payload"])
	if err != nil {
		t.Fatalf("marshal returned payload: %v", err)
	}
	if string(gotPayload) != string(wantPayload) {
		t.Fatalf("raw payload changed: got %s, want %s", gotPayload, wantPayload)
	}
	if lifecycleView, ok := detail["lifecycle"].(map[string]interface{}); !ok || lifecycleView["valid"] != true {
		t.Fatalf("detail lifecycle = %#v, want valid normalized block", detail["lifecycle"])
	}
}

func TestFactDetailReturnsPayloadForSelectedLegacyNumericID(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/collections/memory/points/42"; got != want {
			t.Fatalf("request path = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"id":42,"vector":[1,0],"payload":{"text":"legacy fact","secret":"selected detail only"}}}`)
	}))
	defer backend.Close()

	h := NewHandler(qdrant.NewClient(backend.URL, "memory"), 0.65)
	rr := httptest.NewRecorder()
	h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/facts/42", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET detail: got %d (%s), want 200", rr.Code, rr.Body.String())
	}
	var detail map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail["id"] != "42" {
		t.Fatalf("id = %#v, want legacy numeric id as string", detail["id"])
	}
	if _, ok := detail["payload"].(map[string]interface{}); !ok {
		t.Fatalf("detail payload = %#v, want object", detail["payload"])
	}
	if _, ok := detail["payload_keys"].([]interface{}); !ok {
		t.Fatalf("detail payload_keys = %#v, want array", detail["payload_keys"])
	}
}

func TestFactDetailReturnsPayloadForSelectedStringUUID(t *testing.T) {
	const id = "de305d54-75b4-431b-adb2-eb6b9e546014"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/collections/memory/points/"+id; got != want {
			t.Fatalf("request path = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"id":"`+id+`","payload":{"text":"string ID detail","secret":"selected detail only"}}}`)
	}))
	defer backend.Close()

	h := NewHandler(qdrant.NewClient(backend.URL, "memory"), 0.65)
	rr := httptest.NewRecorder()
	h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/facts/"+id, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET detail: got %d (%s), want 200", rr.Code, rr.Body.String())
	}
	var detail map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail["id"] != id {
		t.Fatalf("id = %#v, want %q", detail["id"], id)
	}
}

func TestDocumentsResponseUsesRelativePathsAndCachesFullScan(t *testing.T) {
	var scanCalls, countCalls int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/collections/doc_chunks/points/count":
			countCalls++
			mustWriteTestResponse(t, w, `{"result":{"count":2}}`)
		case "/collections/doc_chunks/points/scroll":
			scanCalls++
			mustWriteTestResponse(t, w, `{"result":{"points":[{"id":"a","payload":{"file_path":"/srv/private-documents/notes/a.md","folder_path":"/srv/private-documents/notes","chunk_index":0,"heading":"A","indexed_at":"2026-07-20T00:00:00Z"}},{"id":"b","payload":{"file_path":"/srv/private-documents/notes/a.md","folder_path":"/srv/private-documents/notes","chunk_index":1,"indexed_at":"2026-07-20T00:00:00Z"}}],"next_page_offset":null}}`)
		default:
			t.Fatalf("unexpected Qdrant request %s", r.URL.Path)
		}
	}))
	defer backend.Close()

	h := NewHandler(nil, 0.65).WithDocumentRAG(qdrant.NewClient(backend.URL, "doc_chunks"), "/srv/private-documents")
	h.documentsCacheTTL = time.Hour
	// Status must remain a direct count operation, not a full 78k-style scan.
	status := httptest.NewRecorder()
	h.Router().ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/documents/status", nil))
	if status.Code != http.StatusOK || scanCalls != 0 || countCalls != 1 {
		t.Fatalf("status code=%d scans=%d counts=%d, want 200/0/1", status.Code, scanCalls, countCalls)
	}

	for i := 0; i < 2; i++ {
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/documents", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("documents request %d: got %d (%s)", i, rr.Code, rr.Body.String())
		}
		assertJSONOmitsKeys(t, rr.Body.Bytes(), "documents_dir", "path", "file_path", "folder_path")
		if strings.Contains(rr.Body.String(), "/srv/private-documents") {
			t.Fatalf("documents response leaked absolute root: %s", rr.Body.String())
		}
	}
	if scanCalls != 1 {
		t.Fatalf("full document scans = %d, want 1 while cache is fresh", scanCalls)
	}

	status = httptest.NewRecorder()
	h.Router().ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/documents/status", nil))
	if !strings.Contains(status.Body.String(), `"cached":true`) {
		t.Fatalf("cached status = %s, want cached true", status.Body.String())
	}
}

func TestDocumentsForcedRefreshBypassesFreshCache(t *testing.T) {
	var scanCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scanCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[],"next_page_offset":null}}`)
	}))
	defer backend.Close()

	h := NewHandler(nil, 0.65).WithDocumentRAG(qdrant.NewClient(backend.URL, "doc_chunks"), "/srv/documents")
	targets := []string{"/api/documents", "/api/documents", "/api/documents?refresh=1"}
	expiryStrings := make([]string, 0, len(targets))
	expiries := make([]time.Time, 0, len(targets))
	for i, target := range targets {
		if i == 2 {
			// Ensure the forced refresh has an observably later wall-clock expiry.
			time.Sleep(2 * time.Millisecond)
		}
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s: got %d (%s), want 200", target, rr.Code, rr.Body.String())
		}
		var response documentsResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode GET %s: %v", target, err)
		}
		if response.CacheExpiresAt == "" {
			t.Fatalf("GET %s omitted cache_expires_at", target)
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, response.CacheExpiresAt)
		if err != nil {
			t.Fatalf("GET %s cache_expires_at %q is not RFC3339: %v", target, response.CacheExpiresAt, err)
		}
		if !strings.HasSuffix(response.CacheExpiresAt, "Z") {
			t.Fatalf("GET %s cache_expires_at %q is not UTC", target, response.CacheExpiresAt)
		}
		expiryStrings = append(expiryStrings, response.CacheExpiresAt)
		expiries = append(expiries, expiresAt)
	}
	if got := scanCalls.Load(); got != 2 {
		t.Fatalf("document scans = %d, want cached ordinary GET plus one forced refresh", got)
	}
	if expiryStrings[0] != expiryStrings[1] {
		t.Fatalf("cache hit expiry changed from %q to %q", expiryStrings[0], expiryStrings[1])
	}
	if !expiries[2].After(expiries[1]) {
		t.Fatalf("forced refresh expiry %s did not advance beyond cached expiry %s", expiries[2], expiries[1])
	}
}

func TestDocumentRefreshPublishesImmutableExpiry(t *testing.T) {
	h := &Handler{documentsCacheTTL: time.Hour}
	refresh, owner := h.acquireDocumentsRefresh()
	if !owner {
		t.Fatal("first refresh did not become owner")
	}
	built := &documentsResponse{}
	published := h.finishDocumentsRefresh(refresh, built, nil)
	if built.CacheExpiresAt != "" {
		t.Fatalf("unpublished builder response was mutated: %q", built.CacheExpiresAt)
	}
	if published == nil || published.CacheExpiresAt == "" {
		t.Fatalf("published response expiry = %#v, want populated cache_expires_at", published)
	}
	if _, err := time.Parse(time.RFC3339Nano, published.CacheExpiresAt); err != nil {
		t.Fatalf("published expiry %q is not RFC3339: %v", published.CacheExpiresAt, err)
	}
	cached, ok := h.cachedDocuments()
	if !ok || cached.CacheExpiresAt != published.CacheExpiresAt {
		t.Fatalf("cached expiry = %#v, want stable %q", cached, published.CacheExpiresAt)
	}
}

func TestConcurrentForcedDocumentRefreshesCoalesce(t *testing.T) {
	var scanCalls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := scanCalls.Add(1)
		if call > 1 {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[],"next_page_offset":null}}`)
	}))
	defer backend.Close()

	h := NewHandler(nil, 0.65).WithDocumentRAG(qdrant.NewClient(backend.URL, "doc_chunks"), "/srv/documents")
	prime := httptest.NewRecorder()
	h.Router().ServeHTTP(prime, httptest.NewRequest(http.MethodGet, "/api/documents", nil))
	if prime.Code != http.StatusOK {
		t.Fatalf("prime cache: got %d (%s), want 200", prime.Code, prime.Body.String())
	}

	const callers = 8
	start := make(chan struct{})
	results := make(chan int, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rr := httptest.NewRecorder()
			h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/documents?refresh=1", nil))
			results <- rr.Code
		}()
	}
	close(start)
	<-started
	time.Sleep(50 * time.Millisecond)
	if got := scanCalls.Load(); got != 2 {
		t.Fatalf("total scans = %d, want one prime plus one coalesced forced refresh", got)
	}
	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	close(results)
	for code := range results {
		if code != http.StatusOK {
			t.Fatalf("forced refresh status = %d, want 200", code)
		}
	}
}

func TestRelToDocsRejectsCrossPlatformAbsoluteAndTraversalPaths(t *testing.T) {
	h := &Handler{docsDir: "/srv/documents"}
	tests := []struct {
		name     string
		path     string
		absolute bool
		want     string
	}{
		{name: "POSIX inside root", path: "/srv/documents/notes/a.md", absolute: true, want: "notes/a.md"},
		{name: "POSIX outside root", path: "/etc/passwd", absolute: true, want: ""},
		{name: "Windows drive backslashes", path: `C:\Users\me\secret.md`, absolute: true, want: ""},
		{name: "Windows drive slashes", path: "C:/Users/me/secret.md", absolute: true, want: ""},
		{name: "UNC backslashes", path: `\\server\share\secret.md`, absolute: true, want: ""},
		{name: "UNC slashes", path: "//server/share/secret.md", absolute: true, want: ""},
		{name: "safe relative", path: "notes/a.md", want: "notes/a.md"},
		{name: "safe relative backslashes", path: `notes\a.md`, want: "notes/a.md"},
		{name: "slash traversal", path: "../secret.md", want: ""},
		{name: "backslash traversal", path: `..\secret.md`, want: ""},
		{name: "drive relative", path: `C:secret.md`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCrossPlatformAbsolute(tt.path); got != tt.absolute {
				t.Fatalf("isCrossPlatformAbsolute(%q) = %v, want %v", tt.path, got, tt.absolute)
			}
			if got := h.relToDocs(tt.path); got != tt.want {
				t.Fatalf("relToDocs(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestFailedDocumentInventoryIsNotCached(t *testing.T) {
	scanCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scanCalls++
		if scanCalls == 1 {
			http.Error(w, "Qdrant is unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[],"next_page_offset":null}}`)
	}))
	defer backend.Close()

	h := NewHandler(nil, 0.65).WithDocumentRAG(qdrant.NewClient(backend.URL, "doc_chunks"), "/srv/documents")
	for i, want := range []int{http.StatusInternalServerError, http.StatusOK} {
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/documents", nil))
		if rr.Code != want {
			t.Fatalf("request %d: got %d (%s), want %d", i, rr.Code, rr.Body.String(), want)
		}
	}
	if scanCalls != 2 {
		t.Fatalf("document scans = %d, want 2 because failed inventory must not cache", scanCalls)
	}
}

func TestCanceledDocumentInventoryIsNotCached(t *testing.T) {
	scanCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scanCalls++
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[],"next_page_offset":null}}`)
	}))
	defer backend.Close()

	h := NewHandler(nil, 0.65).WithDocumentRAG(qdrant.NewClient(backend.URL, "doc_chunks"), "/srv/documents")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/documents", nil).WithContext(ctx)
	first := httptest.NewRecorder()
	h.Router().ServeHTTP(first, request)
	if first.Code != http.StatusRequestTimeout {
		t.Fatalf("canceled inventory: got %d (%s), want 408", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	h.Router().ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/documents", nil))
	if second.Code != http.StatusOK {
		t.Fatalf("second inventory: got %d (%s), want 200", second.Code, second.Body.String())
	}
	if scanCalls != 1 {
		t.Fatalf("successful document scans = %d, want 1 because canceled inventory must not cache", scanCalls)
	}
}

func TestConcurrentDocumentInventoryRequestsCoalesceToOneScan(t *testing.T) {
	var scanCalls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scanCalls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[],"next_page_offset":null}}`)
	}))
	defer backend.Close()

	h := NewHandler(nil, 0.65).WithDocumentRAG(qdrant.NewClient(backend.URL, "doc_chunks"), "/srv/documents")
	const callers = 8
	start := make(chan struct{})
	results := make(chan int, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rr := httptest.NewRecorder()
			h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/documents", nil))
			results <- rr.Code
		}()
	}
	close(start)
	<-started
	// Keep the owner in the Qdrant call long enough for all simultaneous
	// callers to observe the same in-flight refresh.
	time.Sleep(50 * time.Millisecond)
	if got := scanCalls.Load(); got != 1 {
		t.Fatalf("concurrent scan calls = %d, want exactly 1", got)
	}
	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	close(results)
	for code := range results {
		if code != http.StatusOK {
			t.Fatalf("coalesced request status = %d, want 200", code)
		}
	}
}

func TestDocumentRefreshWaiterHonorsCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[],"next_page_offset":null}}`)
	}))
	defer backend.Close()

	h := NewHandler(nil, 0.65).WithDocumentRAG(qdrant.NewClient(backend.URL, "doc_chunks"), "/srv/documents")
	ownerDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/documents", nil))
		ownerDone <- rr
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/documents", nil).WithContext(ctx))
		waiterDone <- rr
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case waiter := <-waiterDone:
		if waiter.Code != http.StatusRequestTimeout {
			t.Fatalf("canceled waiter status = %d (%s), want 408", waiter.Code, waiter.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("canceled document refresh waiter did not return promptly")
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case owner := <-ownerDone:
		if owner.Code != http.StatusOK {
			t.Fatalf("refresh owner status = %d (%s), want 200", owner.Code, owner.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("document refresh owner did not finish")
	}
}

func TestDocumentRefreshOwnerCancellationDoesNotPoisonWaiter(t *testing.T) {
	var scanCalls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scanCalls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[],"next_page_offset":null}}`)
	}))
	defer backend.Close()

	h := NewHandler(nil, 0.65).WithDocumentRAG(qdrant.NewClient(backend.URL, "doc_chunks"), "/srv/documents")
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	ownerDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/documents", nil).WithContext(ownerCtx))
		ownerDone <- rr
	}()
	<-started

	waiterDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/documents", nil))
		waiterDone <- rr
	}()
	cancelOwner()
	select {
	case owner := <-ownerDone:
		if owner.Code != http.StatusRequestTimeout {
			t.Fatalf("canceled owner status = %d (%s), want 408", owner.Code, owner.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("canceled refresh owner did not return promptly")
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case waiter := <-waiterDone:
		if waiter.Code != http.StatusOK {
			t.Fatalf("live waiter status = %d (%s), want 200", waiter.Code, waiter.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("live waiter did not receive the shared refresh")
	}
	if got := scanCalls.Load(); got != 1 {
		t.Fatalf("document scans = %d, want one shared scan", got)
	}
}

func TestBackendErrorsAreGeneric(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "GET http://qdrant.private:6333 failed: internal storage detail", http.StatusInternalServerError)
	}))
	defer backend.Close()

	h := NewHandler(qdrant.NewClient(backend.URL, "memory"), 0.65)
	for _, target := range []string{"/api/facts", "/api/facts/secret-id", "/api/graph"} {
		rr := httptest.NewRecorder()
		h.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("GET %s: got %d, want 500", target, rr.Code)
		}
		if strings.Contains(rr.Body.String(), "qdrant.private") || strings.Contains(rr.Body.String(), "storage detail") {
			t.Fatalf("GET %s leaked backend error: %s", target, rr.Body.String())
		}
	}
}

func assertJSONOmitsKeys(t *testing.T, body []byte, forbidden ...string) {
	t.Helper()
	var value interface{}
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	for _, key := range forbidden {
		if jsonContainsKey(value, key) {
			t.Fatalf("response contains forbidden key %q: %s", key, body)
		}
	}
}

func assertJSONFactSummariesHaveLegacyLifecycle(t *testing.T, body []byte, target string) {
	t.Helper()
	var value interface{}
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	found := 0
	var inspect func(interface{})
	inspect = func(node interface{}) {
		switch typed := node.(type) {
		case map[string]interface{}:
			if _, hasID := typed["id"]; hasID {
				if _, hasText := typed["text"]; hasText {
					found++
					view, ok := typed["lifecycle"].(map[string]interface{})
					if !ok || view["state"] != "current" || view["legacy"] != true || view["valid"] != true {
						t.Errorf("GET %s lifecycle = %#v, want valid legacy current", target, typed["lifecycle"])
					}
				}
			}
			for _, child := range typed {
				inspect(child)
			}
		case []interface{}:
			for _, child := range typed {
				inspect(child)
			}
		}
	}
	inspect(value)
	if found == 0 {
		t.Fatalf("GET %s returned no fact summaries: %s", target, body)
	}
}

func jsonContainsKey(value interface{}, wanted string) bool {
	switch node := value.(type) {
	case map[string]interface{}:
		for key, child := range node {
			if key == wanted || jsonContainsKey(child, wanted) {
				return true
			}
		}
	case []interface{}:
		for _, child := range node {
			if jsonContainsKey(child, wanted) {
				return true
			}
		}
	}
	return false
}

func TestScrollGraphPointsPagesFiltersAndStopsAtBound(t *testing.T) {
	calls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			mustWriteTestResponse(t, w, `{"result":{"points":[{"id":"ignored","vector":[1,0],"payload":{"namespace":"work"}},{"id":"1","vector":[1,0],"payload":{"namespace":"projects"}}],"next_page_offset":"page-2"}}`)
		case 2:
			mustWriteTestResponse(t, w, `{"result":{"points":[{"id":"2","vector":[1,0],"payload":{"namespace":"projects"}},{"id":"3","vector":[1,0],"payload":{"namespace":"projects"}}],"next_page_offset":"page-3"}}`)
		default:
			t.Fatal("scroll continued after finding max_nodes+1 matching points")
		}
	}))
	defer backend.Close()

	h := NewHandler(qdrant.NewClient(backend.URL, "memory"), 0.65)
	points, tooMany, err := h.scrollGraphPoints(context.Background(), 2, "projects", "", "", "")
	if err != nil {
		t.Fatalf("scrollGraphPoints: %v", err)
	}
	if !tooMany {
		t.Fatal("tooMany = false, want true")
	}
	if calls != 2 || len(points) != 2 || points[0].ID != "1" || points[1].ID != "2" {
		t.Fatalf("calls=%d points=%#v, want two pages and matching points 1,2", calls, points)
	}
}

func TestScrollGraphPointsStopsSparseScanAtHardBound(t *testing.T) {
	calls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[{"id":"%d-a","vector":[1,0],"payload":{"text":"present"}},{"id":"%d-b","vector":[1,0],"payload":{"text":"present"}}],"next_page_offset":"page-%d"}}`, calls, calls, calls+1)
	}))
	defer backend.Close()

	h := NewHandler(qdrant.NewClient(backend.URL, "memory"), 0.65)
	_, _, err := h.scrollGraphPointsWithLimit(context.Background(), 2, 3, "", "", "", "missing")
	if !errors.Is(err, errGraphScanLimit) {
		t.Fatalf("scrollGraphPointsWithLimit error = %v, want errGraphScanLimit", err)
	}
	if calls != 2 {
		t.Fatalf("scroll calls = %d, want early stop after 2 bounded pages", calls)
	}
}

func TestScrollGraphPointsPushesRepresentableFiltersToQdrant(t *testing.T) {
	var requestBody map[string]interface{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mustWriteTestResponse(t, w, `{"result":{"points":[],"next_page_offset":null}}`)
	}))
	defer backend.Close()

	h := NewHandler(qdrant.NewClient(backend.URL, "memory"), 0.65)
	if _, _, err := h.scrollGraphPoints(context.Background(), 10, "projects", "memory", "personal-memory", "missing"); err != nil {
		t.Fatalf("scrollGraphPoints: %v", err)
	}
	filter, ok := requestBody["filter"].(map[string]interface{})
	if !ok {
		t.Fatalf("request filter = %#v, want object", requestBody["filter"])
	}
	must, ok := filter["must"].([]interface{})
	if !ok || len(must) != 3 {
		t.Fatalf("filter.must = %#v, want namespace/tag/primary_tag conditions", filter["must"])
	}
	encoded, err := json.Marshal(filter)
	if err != nil {
		t.Fatalf("marshal filter: %v", err)
	}
	for _, want := range []string{`"namespace"`, `"tags"`, `"primary_tag"`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("Qdrant filter %s does not contain %s", encoded, want)
		}
	}
	if strings.Contains(string(encoded), `"text"`) {
		t.Fatalf("local text-state filter leaked into Qdrant filter: %s", encoded)
	}
}

func TestQdrantGraphFiltersKeepsMissingNamespaceLocal(t *testing.T) {
	filter := qdrantGraphFilters("__missing__", "memory", "")
	encoded, err := json.Marshal(filter)
	if err != nil {
		t.Fatalf("marshal filter: %v", err)
	}
	if strings.Contains(string(encoded), `"namespace"`) {
		t.Fatalf("missing-namespace sentinel must stay a local filter: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"tags"`) {
		t.Fatalf("representable tag filter was not pushed to Qdrant: %s", encoded)
	}
}

func TestGraphComputationErrorsHaveBoundedStatuses(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want int
	}{
		{err: errGraphScanLimit, want: http.StatusUnprocessableEntity},
		{err: context.DeadlineExceeded, want: http.StatusRequestTimeout},
	} {
		rr := httptest.NewRecorder()
		writeGraphComputationError(rr, tt.err)
		if rr.Code != tt.want {
			t.Errorf("error %v: status = %d, want %d", tt.err, rr.Code, tt.want)
		}
	}
}

func TestSimilarityScansHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	points := []qdrant.ScrollPoint{{ID: "1", Vector: []float32{1, 0}}, {ID: "2", Vector: []float32{1, 0}}}
	if _, err := strongestGraphEdges(ctx, points, 0.5, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("strongestGraphEdges error = %v, want context.Canceled", err)
	}
	if _, err := strongestDuplicates(ctx, points, 0.5, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("strongestDuplicates error = %v, want context.Canceled", err)
	}
}

func TestDuplicatesUIHandlesErrorsAndOffersBoundedRetry(t *testing.T) {
	js, err := staticFS.ReadFile("static/assets/js/duplicates.js")
	if err != nil {
		t.Fatalf("read duplicates.js: %v", err)
	}
	source := string(js)
	for _, want := range []string{"if (!res.ok)", "responseMessage(res)", "retryLimit", "maxNodes < 5000", "loadDuplicates(retryLimit)", "Scan up to 5,000 facts"} {
		if !strings.Contains(source, want) {
			t.Errorf("duplicates.js does not contain %q", want)
		}
	}
}

func TestStrongestEdgeHeapIsBoundedAndDeterministic(t *testing.T) {
	h := &edgeHeap{}
	for _, edge := range []graphEdge{
		{From: "b", To: "c", Similarity: 0.8},
		{From: "a", To: "b", Similarity: 0.9},
		{From: "a", To: "c", Similarity: 0.9},
	} {
		keepStrongestEdge(h, edge, 2)
	}
	if len(*h) != 2 {
		t.Fatalf("heap len = %d, want 2", len(*h))
	}
	for _, edge := range *h {
		if edge.Similarity != 0.9 {
			t.Fatalf("kept weak edge: %#v", edge)
		}
	}
}

func TestUpdateTagsRejectsUnknownOversizedAndInvalidPrimary(t *testing.T) {
	h := NewHandler(nil, 0.65)
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "unknown field", body: `{"tags":["health"],"extra":true}`, want: http.StatusBadRequest},
		{name: "oversized primary", body: `{"tags":["health"],"primary_tag":"` + strings.Repeat("x", maxFactTagLength+1) + `"}`, want: http.StatusBadRequest},
		{name: "too many tags", body: `{"tags":["` + strings.Repeat(`x","`, maxFactTags) + `x"]}`, want: http.StatusBadRequest},
		{name: "oversized", body: `{"tags":["` + strings.Repeat("x", maxTagsBodyBytes) + `"]}`, want: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/api/facts/id/tags", strings.NewReader(tt.body))
			req.Header.Set("X-Viz-Action", "update-tags")
			rr := httptest.NewRecorder()
			h.Router().ServeHTTP(rr, req)
			if rr.Code != tt.want {
				t.Fatalf("got %d (%s), want %d", rr.Code, rr.Body.String(), tt.want)
			}
		})
	}
}

func TestBuildShellHTML_Succeeds(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	if len(html) == 0 {
		t.Fatal("composed HTML is empty")
	}
}

func TestBuildShellHTML_PlaceholderReplaced(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	if strings.Contains(string(html), "<!-- VIEWS -->") {
		t.Error("placeholder <!-- VIEWS --> remains in composed HTML")
	}
}

func TestBuildShellHTML_AllViewContainersPresent(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	s := string(html)
	for _, name := range viewNames {
		marker := `id="` + name + `-view"`
		if !strings.Contains(s, marker) {
			t.Errorf("composed HTML is missing view container %q", marker)
		}
	}
}

func TestBuildShellHTML_AllTabsPresent(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	s := string(html)
	for _, name := range viewNames {
		marker := `data-view="` + name + `"`
		if !strings.Contains(s, marker) {
			t.Errorf("composed HTML is missing tab button %q", marker)
		}
	}
}

func TestBuildShellHTML_AssetsReferenced(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	s := string(html)
	for _, js := range []string{"shared.js", "overview.js", "duplicates.js", "forgotten.js", "timeline.js", "graph.js", "documents.js", "init.js"} {
		if !strings.Contains(s, "/viz/assets/js/"+js) {
			t.Errorf("shell does not reference %s", js)
		}
	}
	if !strings.Contains(s, "/viz/assets/styles.css") {
		t.Error("shell does not reference styles.css")
	}
	if !strings.Contains(s, "/viz/assets/vendor/dzarlax.css") {
		t.Error("shell does not reference the design-system bundle")
	}
	for _, asset := range []string{
		"/viz/assets/vendor/vis-network.min.js",
		"/viz/assets/vendor/vis-timeline-graph2d.min.js",
		"/viz/assets/vendor/vis-timeline-graph2d.min.css",
	} {
		if !strings.Contains(s, asset) {
			t.Errorf("shell does not reference %s", asset)
		}
	}
}

func TestBuildShellHTML_NoRuntimeCDNReferences(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	s := string(html)
	for _, blocked := range []string{"https://unpkg.com/", "https://cdn.jsdelivr.net/", "https://statically.io/"} {
		if strings.Contains(s, blocked) {
			t.Errorf("shell contains runtime CDN reference %q", blocked)
		}
	}
}

func TestBuildShellHTML_DarkModeDefault(t *testing.T) {
	html, err := buildShellHTML()
	if err != nil {
		t.Fatalf("buildShellHTML: %v", err)
	}
	if !strings.Contains(string(html), `dark-mode`) {
		t.Error("shell should opt into the design-system dark theme via the dark-mode attribute")
	}
}

func TestStaticUIContractsForLazyLoadingAccessibilityAndResilience(t *testing.T) {
	read := func(t *testing.T, name string) string {
		t.Helper()
		body, err := staticFS.ReadFile("static/" + name)
		if err != nil {
			t.Fatalf("read static %s: %v", name, err)
		}
		return string(body)
	}
	require := func(t *testing.T, source, needle string) {
		t.Helper()
		if !strings.Contains(source, needle) {
			t.Errorf("static source is missing %q", needle)
		}
	}
	forbid := func(t *testing.T, source, needle string) {
		t.Helper()
		if strings.Contains(source, needle) {
			t.Errorf("static source must not contain %q", needle)
		}
	}

	init := read(t, "assets/js/init.js")
	require(t, init, "loadDocumentStatus().catch")
	forbid(t, init, "initFacts();\nloadDocuments();")
	require(t, init, "aria-current")
	require(t, init, "documentsAreStale()")
	require(t, init, "name === 'forgotten' && !factsData")

	overview := read(t, "assets/js/overview.js")
	require(t, overview, "factsPromise = null")
	require(t, overview, "renderFactsFailure")
	require(t, overview, "<button class=\"treemap-tile\"")
	require(t, overview, "activity-section")
	require(t, overview, "gridStart.setUTCDate")
	require(t, overview, "gridEnd.setUTCDate")
	require(t, overview, "getUTCDay")
	require(t, overview, "role', 'listitem")
	require(t, overview, "No facts have been stored yet")
	require(t, overview, "activity-section').hidden = true")
	require(t, overview, "Loading knowledge map")

	graph := read(t, "assets/js/graph.js")
	require(t, graph, "api/facts/${encodeURIComponent(id)}")
	require(t, graph, "Retry with up to 5,000 nodes")
	require(t, graph, "panel.focus()")
	require(t, graph, "graph-results-list")
	require(t, graph, "save.disabled = true")
	require(t, graph, "factAtSave")
	require(t, graph, "detailAtSave")
	require(t, graph, "selectedFact !== factAtSave || detailRequest !== detailAtSave")
	require(t, graph, "pendingTagSaves")
	require(t, graph, "pendingTagSaves.has(fact.id)")
	require(t, graph, "saveFailureMessage")
	require(t, graph, "syncCachedFactTags")
	require(t, graph, "(factsData?.nodes || []).forEach(update)")
	require(t, graph, "(graphDataCache?.nodes || []).forEach(update)")
	require(t, graph, "id === 'tag-filter' ? 'input' : 'change'")
	require(t, graph, "graphAbortController")
	require(t, graph, "graphAbortController.abort()")
	require(t, graph, "request !== graphRequest")
	require(t, graph, "resetGraphNetwork()")
	require(t, graph, "const activeNetwork = network")

	documents := read(t, "assets/js/documents.js")
	require(t, documents, "api/documents/status")
	require(t, documents, "aria-expanded")
	require(t, documents, "documentsShown += 50")
	require(t, documents, "FILE_PAGE_SIZE = 50")
	require(t, documents, "renderDocumentFilePage")
	require(t, documents, "documentsFreshUntil")
	require(t, documents, "documentsAreStale")
	require(t, documents, "loadDocuments(true)")
	require(t, documents, "Date.parse(documentsData.cache_expires_at")
	require(t, documents, "Number.isNaN(parsedExpiry) ? 0 : parsedExpiry")
	require(t, documents, "loadDocuments(force)")
	require(t, documents, "status.enabled && !documentsData")
	require(t, documents, "if (!documentsData)")
	require(t, documents, "api/documents?refresh=1")
	require(t, documents, "const endpoint = force")
	require(t, documents, "status.cached ? (status.total_files ?? 0) : '—'")
	forbid(t, documents, "status.total_files || status.total_chunks")
	forbid(t, documents, "onclick=")

	forgotten := read(t, "assets/js/forgotten.js")
	require(t, forgotten, "forgottenShown += 50")
	require(t, forgotten, "Loading never-recalled facts")
	sharedTimeline := read(t, "assets/js/timeline.js")
	require(t, sharedTimeline, "Loading timeline")
	require(t, sharedTimeline, "No facts with creation dates")
	require(t, sharedTimeline, "renderRetry")
	shared := read(t, "assets/js/shared.js")
	require(t, shared, "normalizeTagDisplay")
	require(t, shared, "originalsByDisplay")
	require(t, shared, "${normalized} (${original})")
	duplicates := read(t, "assets/js/duplicates.js")
	require(t, duplicates, "duplicatesShown += 50")

	graphView := read(t, "views/graph.html")
	require(t, graphView, "role=\"region\"")
	forbid(t, graphView, "aria-modal")
	require(t, graphView, "<output")
	require(t, graphView, "for=\"ns-filter\"")
	overviewView := read(t, "views/overview.html")
	require(t, overviewView, "id=\"activity-section\"")
	require(t, overviewView, "id=\"heatmap-grid\"")
	require(t, overviewView, "role=\"list\"")
	documentsView := read(t, "views/documents.html")
	require(t, documentsView, "id=\"docs-refresh\"")
	shell := read(t, "shell.html")
	require(t, shell, "id=\"docs-badge\" aria-live=\"polite\">—")

	css := read(t, "assets/styles.css")
	require(t, css, "@media (max-width: 390px)")
	require(t, css, ":focus-visible")
	require(t, css, "clip-path: inset(50%)")
	require(t, css, "prefers-reduced-motion")
	require(t, css, "#graph-view { overflow-y: auto; }")
	require(t, css, "grid-template-rows: repeat(7, 16px)")
	require(t, css, ".heatmap-scroll")
	require(t, css, "flex: 1 1 100% !important")
	require(t, css, ".graph-status { position: static")
	require(t, css, "overflow-x: hidden;\n  flex-shrink: 0;")
	require(t, css, ".activity-section { width: 100%; margin-top: 24px; flex-shrink: 0; }")
	if !balancedCSSBraces(css) {
		t.Error("styles.css has unmatched braces")
	}
}

func balancedCSSBraces(source string) bool {
	depth := 0
	inComment := false
	for i := 0; i < len(source); i++ {
		if !inComment && i+1 < len(source) && source[i:i+2] == "/*" {
			inComment = true
			i++
			continue
		}
		if inComment && i+1 < len(source) && source[i:i+2] == "*/" {
			inComment = false
			i++
			continue
		}
		if inComment {
			continue
		}
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return !inComment && depth == 0
}

func TestNewHandler_ComposesHTMLAtConstruction(t *testing.T) {
	h := NewHandler(nil, 0.65)
	if len(h.composedHTML) == 0 {
		t.Fatal("NewHandler must compose HTML eagerly")
	}
}

// Regression: assets 404'd for two different reasons.
//  1. StripPrefix("/assets/") with a trailing slash made FileServer receive
//     a path without a leading "/" → 404.
//  2. chi.Mount does not rewrite r.URL.Path, only RoutePath, so any
//     StripPrefix call that assumes the URL is already stripped of the
//     mount prefix silently fails.
//
// This test mounts the router at /viz like production does, so both
// regressions would reproduce here.
func TestAssetRouter_ServesEmbeddedFiles(t *testing.T) {
	h := NewHandler(nil, 0.65)
	main := chi.NewRouter()
	main.Mount("/viz", h.Router())

	for _, asset := range []string{"/viz/assets/styles.css", "/viz/assets/js/init.js", "/viz/assets/vendor/dzarlax.css"} {
		req := httptest.NewRequest(http.MethodGet, asset, nil)
		rr := httptest.NewRecorder()
		main.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s: got %d, want 200", asset, rr.Code)
		}
	}
}

func TestFilterGraphPoints_AppliesNamespaceAndTagBeforeEdgeLimit(t *testing.T) {
	points := []qdrant.ScrollPoint{
		{ID: "1", Payload: map[string]interface{}{"namespace": "projects", "tags": []interface{}{"personal-memory"}}},
		{ID: "2", Payload: map[string]interface{}{"namespace": "projects", "tags": []interface{}{"health"}}},
		{ID: "3", Payload: map[string]interface{}{"namespace": "work", "tags": []interface{}{"personal-memory"}}},
	}

	filtered := filterGraphPoints(points, "projects", "personal-memory", "", "")
	if len(filtered) != 1 {
		t.Fatalf("len(filtered) = %d, want 1", len(filtered))
	}
	if filtered[0].ID != "1" {
		t.Fatalf("filtered[0].ID = %q, want %q", filtered[0].ID, "1")
	}
}

func TestFilterGraphPoints_MatchesMissingNamespaceSentinel(t *testing.T) {
	points := []qdrant.ScrollPoint{
		{ID: "1", Payload: map[string]interface{}{"namespace": nil}},
		{ID: "2", Payload: map[string]interface{}{"namespace": ""}},
		{ID: "3", Payload: map[string]interface{}{"namespace": "null"}},
		{ID: "4", Payload: map[string]interface{}{"namespace": "projects"}},
	}

	filtered := filterGraphPoints(points, "__missing__", "", "", "")
	if len(filtered) != 3 {
		t.Fatalf("len(filtered) = %d, want 3", len(filtered))
	}
	for i, want := range []string{"1", "2", "3"} {
		if filtered[i].ID != want {
			t.Fatalf("filtered[%d].ID = %q, want %q", i, filtered[i].ID, want)
		}
	}
}

func TestFilterGraphPoints_AppliesTextState(t *testing.T) {
	points := []qdrant.ScrollPoint{
		{ID: "1", Payload: map[string]interface{}{"text": "stored fact"}},
		{ID: "2", Payload: map[string]interface{}{"recall_count": 3, "recovery_status": "lost_text"}},
	}

	missing := filterGraphPoints(points, "", "", "", "missing")
	if len(missing) != 1 || missing[0].ID != "2" {
		t.Fatalf("missing filter = %#v, want only point 2", missing)
	}

	present := filterGraphPoints(points, "", "", "", "present")
	if len(present) != 1 || present[0].ID != "1" {
		t.Fatalf("present filter = %#v, want only point 1", present)
	}
}

func TestFilterGraphPoints_FiltersPrimaryTagSeparatelyFromTags(t *testing.T) {
	points := []qdrant.ScrollPoint{
		{ID: "1", Payload: map[string]interface{}{"tags": []interface{}{"personal-memory", "architecture"}, "primary_tag": "personal-memory"}},
		{ID: "2", Payload: map[string]interface{}{"tags": []interface{}{"personal-memory", "architecture"}, "primary_tag": "architecture"}},
	}

	filtered := filterGraphPoints(points, "", "", "personal-memory", "")
	if len(filtered) != 1 {
		t.Fatalf("len(filtered) = %d, want 1", len(filtered))
	}
	if filtered[0].ID != "1" {
		t.Fatalf("filtered[0].ID = %q, want %q", filtered[0].ID, "1")
	}
}

func TestPointToSummary_UsesLegacyTextFallbacks(t *testing.T) {
	node := pointToSummary(qdrant.ScrollPoint{
		ID: "1",
		Payload: map[string]interface{}{
			"fact":       "legacy fact text",
			"created":    "2026-05-21T10:00:00Z",
			"namespace":  "projects",
			"recall_cnt": 12,
		},
	})

	if node.Text != "legacy fact text" {
		t.Fatalf("text = %q, want legacy fallback", node.Text)
	}
	if node.CreatedAt != "2026-05-21T10:00:00Z" {
		t.Fatalf("created_at = %q, want created fallback", node.CreatedAt)
	}
}

func TestPointToSummary_ExposesPrimaryTag(t *testing.T) {
	node := pointToSummary(qdrant.ScrollPoint{
		ID: "1",
		Payload: map[string]interface{}{
			"text":        "fact text",
			"tags":        []interface{}{"health", "decision"},
			"primary_tag": "health",
		},
	})

	if node.PrimaryTag != "health" {
		t.Fatalf("primary_tag = %q, want health", node.PrimaryTag)
	}
}

func TestNormalizeFactTags_AddsValidatedPrimaryTag(t *testing.T) {
	tags, primary, err := normalizeFactTags([]string{"decision"}, "health")
	if err != nil {
		t.Fatalf("normalizeFactTags: %v", err)
	}
	if primary != "health" {
		t.Fatalf("primary = %q, want health", primary)
	}
	if len(tags) != 2 || tags[0] != "decision" || tags[1] != "health" {
		t.Fatalf("tags = %#v, want sorted [decision health]", tags)
	}
}

func TestPointToSummary_HidesPayloadKeysForTextlessPoints(t *testing.T) {
	node := pointToSummary(qdrant.ScrollPoint{
		ID:      "1",
		Payload: map[string]interface{}{"recall_count": 27},
	})

	if node.Text != "" {
		t.Fatalf("text = %q, want empty text", node.Text)
	}
}

func TestPointToSummary_DoesNotTreatRecoveryDiagnosticsAsFactText(t *testing.T) {
	node := pointToSummary(qdrant.ScrollPoint{
		ID: "1",
		Payload: map[string]interface{}{
			"recovery_status": "lost_text",
			"nearest_text":    "nearest neighbor is not the lost fact text",
			"nearest_score":   0.91,
		},
	})

	if node.Text != "" {
		t.Fatalf("text = %q, want empty text for recovery diagnostics", node.Text)
	}
	if !node.TextMissing {
		t.Fatalf("text_missing = %#v, want true", node.TextMissing)
	}
}
