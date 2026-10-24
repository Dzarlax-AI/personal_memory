package embeddingmigration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

type migrationHarness struct {
	source, target      []map[string]any
	metadata            map[string]any
	exists              bool
	calls, sourceWrites int
	failAt              int
	server              *httptest.Server
}

func newHarness(t *testing.T) *migrationHarness {
	h := &migrationHarness{}
	vector := make([]float32, 384)
	vector[0] = 1
	h.source = []map[string]any{{"id": 42, "vector": vector, "payload": map[string]any{"text": "synthetic fact", "namespace": "projects", "tags": []string{"personal-memory"}, "lifecycle_state": "historical", "recall_count": 7, "origin": map[string]any{"source": "synthetic", "large_id": json.Number("9007199254740993123")}}}}
	h.server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.server.Close)
	return h
}
func (h *migrationHarness) serve(w http.ResponseWriter, r *http.Request) {
	write := func(value any) { json.NewEncoder(w).Encode(value) }
	if r.URL.Path == "/info" {
		write(map[string]any{"model_id": "intfloat/multilingual-e5-small", "model_sha": strings.Repeat("a", 40), "model_dtype": "float32", "model_type": map[string]any{"embedding": map[string]any{"pooling": "mean"}}})
		return
	}
	if r.URL.Path == "/embed" {
		h.calls++
		if h.failAt > 0 && h.calls == h.failAt {
			w.WriteHeader(400)
			return
		}
		var input struct {
			Inputs []string `json:"inputs"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		vectors := make([][]float32, len(input.Inputs))
		for i := range vectors {
			vectors[i] = make([]float32, 384)
			vectors[i][0] = 1
		}
		write(vectors)
		return
	}
	source := strings.HasPrefix(r.URL.Path, "/collections/source")
	if source && r.Method != "GET" && !strings.HasSuffix(r.URL.Path, "/scroll") && !strings.HasSuffix(r.URL.Path, "/count") {
		h.sourceWrites++
		w.WriteHeader(500)
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/count"):
		count := len(h.target)
		if source {
			count = len(h.source)
		}
		write(map[string]any{"result": map[string]any{"count": count}})
	case strings.HasSuffix(r.URL.Path, "/scroll"):
		points := h.target
		if source {
			points = h.source
		}
		if points == nil {
			points = []map[string]any{}
		}
		write(map[string]any{"result": map[string]any{"points": points, "next_page_offset": nil}})
	case strings.HasSuffix(r.URL.Path, "/points"):
		var request struct {
			Points []map[string]any `json:"points"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		decoder.Decode(&request)
		for _, point := range request.Points {
			found := false
			for i, old := range h.target {
				if old["id"] == point["id"] {
					h.target[i] = point
					found = true
				}
			}
			if !found {
				h.target = append(h.target, point)
			}
		}
		write(map[string]any{"result": map[string]any{"status": "completed"}})
	case r.Method == "PUT":
		var request struct {
			Metadata map[string]any `json:"metadata"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		decoder.Decode(&request)
		h.metadata = request.Metadata
		h.exists = true
		write(map[string]any{"status": "ok", "result": true})
	case r.Method == "GET":
		if !source && !h.exists {
			w.WriteHeader(404)
			return
		}
		metadata := h.metadata
		count := len(h.target)
		if source {
			metadata = map[string]any{"personal_memory.embedding": map[string]any{"schema_version": 1, "provider": "tei", "model_id": "intfloat/multilingual-e5-small", "model_revision": strings.Repeat("b", 40), "model_dtype": "float32", "pooling": "mean", "vector_size": 384, "input_profile": "legacy-raw-v1"}}
			count = len(h.source)
		}
		write(map[string]any{"result": map[string]any{"points_count": count, "config": map[string]any{"params": map[string]any{"vectors": map[string]any{"size": 384}}, "metadata": metadata}}})
	default:
		w.WriteHeader(500)
	}
}
func (h *migrationHarness) job(t *testing.T) Job {
	cfg := embeddings.BackendConfig{Provider: "tei", Model: "intfloat/multilingual-e5-small", InputProfile: embeddings.LegacyRawV1, Dimensions: 384, URL: h.server.URL, Revision: strings.Repeat("a", 40)}
	backend, err := embeddings.NewBackend(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return Job{Source: qdrant.NewClient(h.server.URL, "source"), Target: qdrant.NewClient(h.server.URL, "target"), Backend: backend, Config: cfg, Role: "memory"}
}
func TestMigrationPreservesPayloadIDsAndResume(t *testing.T) {
	h := newHarness(t)
	job := h.job(t)
	ctx := context.Background()
	m, err := job.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.exists || h.sourceWrites != 0 {
		t.Fatal("prepare mutated data")
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := WriteManifest(path, m); err != nil {
		t.Fatal(err)
	}
	m, err = ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Apply(ctx, m, false); err == nil {
		t.Fatal("accepted live writers")
	}
	if err := job.Apply(ctx, m, true); err != nil {
		t.Fatal(err)
	}
	if len(h.target) != 1 || !sameJSON(h.target[0]["payload"], h.source[0]["payload"]) || fmt.Sprint(h.target[0]["id"]) != "42" {
		t.Fatalf("payload or ID changed: %#v", h.target)
	}
	before := h.calls
	if err := job.Apply(ctx, m, true); err != nil {
		t.Fatal(err)
	}
	// Three identity/freshness probes are expected, but no re-embedding of the
	// existing point during resume (Prepare, Verify start, Verify finish).
	if h.calls-before != 3 {
		t.Fatalf("resume re-embedded point: calls delta=%d", h.calls-before)
	}
	if h.sourceWrites != 0 {
		t.Fatal("source changed")
	}
}
func TestMigrationRejectsSourceDriftAndForeignTarget(t *testing.T) {
	h := newHarness(t)
	job := h.job(t)
	ctx := context.Background()
	m, err := job.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.source[0]["payload"].(map[string]any)["recall_count"] = 8
	if err := job.Apply(ctx, m, true); err == nil {
		t.Fatal("accepted stale source")
	}
	if h.exists {
		t.Fatal("created target before freshness pass")
	}
	m, err = job.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.exists = true
	h.metadata = map[string]any{}
	if err := job.Apply(ctx, m, true); err == nil {
		t.Fatal("adopted foreign target")
	}
}
func TestMigrationFingerprintIncludesAllPayload(t *testing.T) {
	p := []qdrant.ScrollPoint{{ID: "opaque", Payload: map[string]any{"text": "x", "recall_count": 1}, Vector: []float32{1}}}
	a, _ := fingerprint(p, true)
	p[0].Payload["recall_count"] = 2
	b, _ := fingerprint(p, true)
	if a == b {
		t.Fatal("ignored metadata drift")
	}
}

func TestInterruptedMigrationResumesOnlyMissingPoints(t *testing.T) {
	h := newHarness(t)
	h.source = append(h.source, map[string]any{"id": 43, "vector": h.source[0]["vector"], "payload": map[string]any{"text": "second synthetic fact", "namespace": "projects"}})
	job := h.job(t)
	ctx := context.Background()
	manifest, err := job.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.failAt = 4 // prepare probe, apply probe, first point, then second point fails.
	if err := job.Apply(ctx, manifest, true); err == nil {
		t.Fatal("expected interrupted migration")
	}
	if len(h.target) != 1 {
		t.Fatalf("partial target count=%d", len(h.target))
	}
	h.failAt = 0
	before := h.calls
	if err := job.Apply(ctx, manifest, true); err != nil {
		t.Fatal(err)
	}
	if len(h.target) != 2 || h.calls-before != 4 {
		t.Fatalf("resume points=%d calls=%d", len(h.target), h.calls-before)
	}
}

func TestFolderMigrationEmbedsSummaryAndPreservesPayload(t *testing.T) {
	h := newHarness(t)
	h.source[0]["payload"] = map[string]any{"summary": "synthetic bounded folder summary", "folder_path": "/synthetic/folder", "summary_version": "synthetic-v1"}
	job := h.job(t)
	job.Role = "folders"
	ctx := context.Background()
	manifest, err := job.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Apply(ctx, manifest, true); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(h.target[0]["payload"], h.source[0]["payload"]) {
		t.Fatal("folder payload changed")
	}
}
