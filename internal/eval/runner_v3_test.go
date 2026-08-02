package eval

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

type fixtureQdrant struct {
	mu           sync.Mutex
	exists       map[string]bool
	createCount  int
	failCreateAt int
	responseLoss bool
	failSeed     bool
	deleted      []string
}

func (fake *fixtureQdrant) handler(w http.ResponseWriter, r *http.Request) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/collections/"), "/")
	name := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			if !fake.exists[name] {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"result":{"points_count":0,"config":{"params":{"vectors":{"size":2,"distance":"Cosine"}},"metadata":{}}}}`)
			return
		case http.MethodPut:
			fake.createCount++
			if fake.createCount == fake.failCreateAt {
				if fake.responseLoss {
					fake.exists[name] = true
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				http.Error(w, "create denied", http.StatusInternalServerError)
				return
			}
			fake.exists[name] = true
			_, _ = w.Write([]byte(`{"status":"ok","result":{"status":"completed"}}`))
			return
		case http.MethodDelete:
			delete(fake.exists, name)
			fake.deleted = append(fake.deleted, name)
			_, _ = w.Write([]byte(`{"status":"ok","result":{"status":"completed"}}`))
			return
		}
	}
	if len(parts) >= 2 && parts[1] == "points" {
		if r.Method == http.MethodPut {
			if fake.failSeed {
				http.Error(w, "seed denied", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok","result":{"status":"completed"}}`))
			return
		}
		if r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "search" {
			_, _ = w.Write([]byte(`{"result":[]}`))
			return
		}
	}
	http.Error(w, "unexpected request", http.StatusInternalServerError)
}

type recordingPurposeEmbedder struct {
	calls       []embeddings.Purpose
	inputs      [][]string
	fail        error
	failPurpose embeddings.Purpose
}

func (embedder *recordingPurposeEmbedder) EmbedWithPurpose(_ context.Context, text string,
	purpose embeddings.Purpose, _ embeddings.InputProfile, _ string) ([]float32, error) {
	embedder.calls = append(embedder.calls, purpose)
	embedder.inputs = append(embedder.inputs, []string{text})
	if embedder.failPurpose == purpose {
		return nil, fmt.Errorf("purpose failure")
	}
	return []float32{1, 0}, embedder.fail
}

func (embedder *recordingPurposeEmbedder) EmbedBatchWithPurpose(_ context.Context, texts []string,
	purpose embeddings.Purpose, _ embeddings.InputProfile, _ string) ([][]float32, error) {
	embedder.calls = append(embedder.calls, purpose)
	embedder.inputs = append(embedder.inputs, append([]string(nil), texts...))
	if embedder.failPurpose == purpose {
		return nil, fmt.Errorf("purpose failure")
	}
	if embedder.fail != nil {
		return nil, embedder.fail
	}
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestHybridRerankLiftsExactIdentifierAndPreservesCosine(t *testing.T) {
	cfg := Configuration{
		RetrievalStrategy: RetrievalHybridRRF, RRFConstant: 60,
		DenseCandidateLimit: 20,
	}
	points := []qdrant.Point{
		{ID: "semantic", Score: .99, Payload: map[string]any{"text": "general memory server"}},
		{ID: "exact", Score: .40, Payload: map[string]any{"text": "incident PM-1427"}},
	}
	ranked, err := rerankPoints("PM-1427", points, 2, cfg, "facts", "")
	if err != nil {
		t.Fatal(err)
	}
	if ranked[0].ID != "exact" || ranked[0].Score != .40 {
		t.Fatalf("ranked = %#v, want exact first with original cosine", ranked)
	}
}

func TestV3VectorOnlyRerankPathLeavesDenseOutputUnchanged(t *testing.T) {
	points := []qdrant.Point{
		{ID: "dense-first", Score: .99, Payload: map[string]any{"text": "general"}},
		{ID: "lexical-second", Score: .4, Payload: map[string]any{"text": "PM-1427"}},
	}
	got, err := rerankPoints("PM-1427", points, 1, Configuration{
		RetrievalStrategy: RetrievalVectorOnly,
	}, "facts", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "dense-first" || got[1].ID != "lexical-second" ||
		got[0].Score != .99 || got[1].Score != .4 {
		t.Fatalf("vector-only output changed: %+v", got)
	}
}

func TestHybridRerankFallsBackToDenseOrderWithoutLexicalSignal(t *testing.T) {
	cfg := Configuration{
		RetrievalStrategy: RetrievalHybridRRF, RRFConstant: 60,
		DenseCandidateLimit: 20,
	}
	points := []qdrant.Point{
		{ID: "second", Score: .3, Payload: map[string]any{"text": "other"}},
		{ID: "first", Score: .8, Payload: map[string]any{"text": "different"}},
	}
	ranked, err := rerankPoints("absent-token", points, 2, cfg, "facts", "")
	if err != nil {
		t.Fatal(err)
	}
	if ranked[0].ID != "first" || ranked[1].ID != "second" {
		t.Fatalf("ranked IDs = %s, %s", ranked[0].ID, ranked[1].ID)
	}
}

func TestHybridRerankKeepsCandidateWithoutLexicalFieldsDenseOnly(t *testing.T) {
	cfg := Configuration{
		RetrievalStrategy: RetrievalHybridRRF, RRFConstant: 60,
		DenseCandidateLimit: 20,
	}
	points := []qdrant.Point{
		{ID: "dense-only", Score: .9, Payload: map[string]any{"text": 42}},
		{ID: "other", Score: .4, Payload: map[string]any{"text": "unrelated"}},
	}
	ranked, err := rerankPoints("absent", points, 2, cfg, "facts", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 2 || ranked[0].ID != "dense-only" {
		t.Fatalf("dense-only runner ranking = %+v", ranked)
	}
}

func TestLexicalFieldsRelativizeDeploymentPaths(t *testing.T) {
	fields := lexicalFields(map[string]any{
		"text": "body", "heading": "Title",
		"file_path": "/srv/personal/docs/project/design.md",
	}, "chunks", "/srv/personal/docs")
	var path string
	for _, field := range fields {
		if field.Name == "file_path" {
			path = field.Value
		}
	}
	if path != "project/design.md" {
		t.Fatalf("relative lexical path = %q", path)
	}
	fields = lexicalFields(map[string]any{
		"text": "body", "file_path": "/srv/private/secret/design.md",
	}, "chunks", "/srv/personal/docs")
	for _, field := range fields {
		if field.Name == "file_path" {
			t.Fatalf("unrelated deployment path leaked into lexical fields: %+v", fields)
		}
	}
}

func TestExperimentOverridesDoNotMutateDataset(t *testing.T) {
	original, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	original.Embedding.ModelID = multilingualE5SmallModelID
	beforeName := original.Configuration.Name
	beforeProfile := original.Embedding.InputProfile
	beforeStrategy := original.Configuration.RetrievalStrategy
	beforeTopK := append([]int(nil), original.Configuration.TopK...)
	name := "experiment"
	profile := MultilingualE5V1
	strategy := RetrievalHybridRRF
	limit, constant := 20, 60
	cloned, err := WithExperimentOverrides(original, ExperimentOverrides{
		ConfigurationName: &name, InputProfile: &profile, RetrievalStrategy: &strategy,
		DenseCandidateLimit: &limit, RRFConstant: &constant,
	}, "live")
	if err != nil {
		t.Fatal(err)
	}
	if cloned.Configuration.Name != name || cloned.Embedding.InputProfile != profile ||
		cloned.Configuration.RetrievalStrategy != strategy {
		t.Fatalf("overrides not applied: %#v", cloned.Configuration)
	}
	if original.Configuration.Name != beforeName ||
		original.Embedding.InputProfile != beforeProfile ||
		original.Configuration.RetrievalStrategy != beforeStrategy ||
		len(original.Configuration.TopK) != len(beforeTopK) ||
		original.Configuration.TopK[0] != beforeTopK[0] {
		t.Fatal("experiment overrides mutated input dataset")
	}
}

func TestExperimentOverridesRejectInvalidCombination(t *testing.T) {
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	strategy := RetrievalHybridRRF
	if _, err := WithExperimentOverrides(dataset, ExperimentOverrides{
		RetrievalStrategy: &strategy,
	}, "live"); err == nil || !strings.Contains(err.Error(), "dense_candidate_limit") {
		t.Fatalf("invalid override error = %v", err)
	}
}

func TestDurationSummaryDeterministic(t *testing.T) {
	got := summarizeDurations([]int64{40, 10, 20, 30})
	want := DurationSummary{Count: 4, Min: 10, P50: 20, P95: 40, Max: 40}
	if got != want {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
}

func TestHybridLifecyclePresentationUsesSemanticRankWithinAuthorityTier(t *testing.T) {
	for _, intent := range []QueryIntent{
		QueryIntentCurrent, QueryIntentHistory, QueryIntentAsOf, QueryIntentUncertainty,
	} {
		t.Run(string(intent), func(t *testing.T) {
			state := lifecycle.Current
			if intent != QueryIntentCurrent {
				state = lifecycle.Historical
			}
			payload := func() map[string]any {
				return map[string]any{
					"text": "value", "lifecycle_state": string(state), "canonical": false,
					"supersedes": []any{}, "superseded_by": []any{},
				}
			}
			points := []qdrant.Point{
				{ID: "exact", Score: .4, Payload: payload()},
				{ID: "semantic", Score: .99, Payload: payload()},
			}
			query := Query{ID: "q", Intent: intent}
			if intent == QueryIntentAsOf {
				query.AsOf = "2026-01-01"
			}
			got := presentFactCandidatesWithOrder(query, points, time.Now(), true).results
			if len(got) != 2 || got[0].ID != "exact" || got[1].ID != "semantic" {
				t.Fatalf("hybrid lifecycle results = %+v", got)
			}
		})
	}
}

func TestHybridLifecycleAuthorityStillDemotesNoncanonicalCurrent(t *testing.T) {
	payload := func(canonical bool) map[string]any {
		return map[string]any{
			"text": "value", "lifecycle_state": "current", "canonical": canonical,
			"supersedes": []any{}, "superseded_by": []any{},
		}
	}
	points := []qdrant.Point{
		{ID: "exact-noncanonical", Score: .4, Payload: payload(false)},
		{ID: "canonical", Score: .3, Payload: payload(true)},
	}
	got := presentFactCandidatesWithOrder(Query{ID: "q"}, points, time.Now(), true).results
	if len(got) != 2 || got[0].ID != "canonical" || got[1].ID != "exact-noncanonical" {
		t.Fatalf("authority ordering = %+v", got)
	}
}

func TestTEIFixtureCorpusEmbeddingUsesPurposeOrderWithoutMutatingSource(t *testing.T) {
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	cloned := cloneDataset(dataset)
	embedder := &recordingPurposeEmbedder{}
	count, err := embedFixtureCorpus(context.Background(), &cloned, embedder)
	if err != nil {
		t.Fatal(err)
	}
	if count != len(dataset.Facts)+len(dataset.Chunks)+len(dataset.Folders) {
		t.Fatalf("embedded count = %d", count)
	}
	wantPurposes := []embeddings.Purpose{
		embeddings.FactPassage, embeddings.ChunkPassage, embeddings.FolderPassage,
	}
	for i, want := range wantPurposes {
		if embedder.calls[i] != want {
			t.Fatalf("purpose call %d = %v, want %v", i, embedder.calls[i], want)
		}
	}
	if dataset.Facts[1].Vector[0] != 0 || cloned.Facts[1].Vector[0] != 1 {
		t.Fatal("TEI fixture corpus preparation mutated the source or failed to replace clone vectors")
	}
}

func TestTEIFixtureCorpusEmbeddingRejectsMissingText(t *testing.T) {
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts[0].Payload["text"] = 42
	if _, err := embedFixtureCorpus(context.Background(), dataset, &recordingPurposeEmbedder{}); err == nil ||
		!strings.Contains(err.Error(), `facts point "42"`) ||
		strings.Contains(err.Error(), "numeric") {
		t.Fatalf("missing corpus text error = %v", err)
	}
}

func TestV3DiagnosticsStrictDecodeAndComparisonIsInformational(t *testing.T) {
	baseline := validV3ComparisonReport()
	baseline.Mode = "live"
	queryCount := len(baseline.Queries)
	baseline.Diagnostics = &Diagnostics{Query: QueryDiagnostics{
		Total:  DurationSummary{Count: queryCount, Min: 10, P50: 10, P95: 10, Max: 10},
		Search: DurationSummary{Count: queryCount, Min: 10, P50: 10, P95: 10, Max: 10},
	}}
	candidate := baseline
	candidate.Diagnostics = &Diagnostics{Query: QueryDiagnostics{
		Total:  DurationSummary{Count: queryCount, Min: 100, P50: 100, P95: 100, Max: 100},
		Search: DurationSummary{Count: queryCount, Min: 100, P50: 100, P95: 100, Max: 100},
	}}
	comparison, err := Compare(baseline, candidate, false)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.BaselineDiagnostics == nil || comparison.CandidateDiagnostics == nil {
		t.Fatal("comparison omitted informational diagnostics")
	}

	invalid := baseline
	invalid.Diagnostics = &Diagnostics{Query: QueryDiagnostics{
		Total:  DurationSummary{Count: queryCount, Min: 10, P50: 5, P95: 10, Max: 10},
		Search: DurationSummary{Count: queryCount, Min: 10, P50: 10, P95: 10, Max: 10},
	}}
	encoded, err := RenderJSON(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(encoded); err == nil || !strings.Contains(err.Error(), "diagnostics") {
		t.Fatalf("DecodeReport() error = %v, want strict diagnostics rejection", err)
	}

	for _, tt := range []struct {
		name   string
		mutate func(*Report)
	}{
		{"count mismatch", func(report *Report) {
			report.Diagnostics.Query.Search.Count--
		}},
		{"live corpus", func(report *Report) {
			report.Diagnostics.Corpus = &CorpusDiagnostics{EmbeddingCount: 1}
		}},
		{"embed above queries", func(report *Report) {
			report.Diagnostics.Query.Embed = DurationSummary{
				Count: len(report.Queries) + 1, Min: 1, P50: 1, P95: 1, Max: 1,
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tampered := baseline
			diagnostics := *baseline.Diagnostics
			tampered.Diagnostics = &diagnostics
			tt.mutate(&tampered)
			data, err := RenderJSON(tampered)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeReport(data); err == nil || !strings.Contains(err.Error(), "diagnostics") {
				t.Fatalf("DecodeReport() error = %v", err)
			}
		})
	}
}

func TestFixtureCleanupCoversSuccessFailureAndAmbiguousCreate(t *testing.T) {
	for _, tt := range []struct {
		name            string
		failCreateAt    int
		responseLoss    bool
		failSeed        bool
		wantError       bool
		wantDeleteCount int
	}{
		{"success", 0, false, false, false, 3},
		{"seed failure", 0, false, true, true, 3},
		{"first create response loss", 1, true, false, true, 1},
		{"second create response loss", 2, true, false, true, 2},
		{"genuine create failure", 1, false, false, true, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fixtureQdrant{
				exists: make(map[string]bool), failCreateAt: tt.failCreateAt,
				responseLoss: tt.responseLoss, failSeed: tt.failSeed,
			}
			server := httptest.NewServer(http.HandlerFunc(fake.handler))
			defer server.Close()
			dataset, err := Load(strings.NewReader(validV3Dataset()))
			if err != nil {
				t.Fatal(err)
			}
			_, err = Run(context.Background(), dataset, RunOptions{
				Source: "fixture", QdrantURL: server.URL,
			})
			if (err != nil) != tt.wantError {
				t.Fatalf("Run() error = %v, wantError=%t", err, tt.wantError)
			}
			if len(fake.deleted) != tt.wantDeleteCount {
				t.Fatalf("deleted = %v, want %d", fake.deleted, tt.wantDeleteCount)
			}
			if len(fake.exists) != 0 {
				t.Fatalf("temporary collections leaked: %v", fake.exists)
			}
			for _, name := range fake.deleted {
				if !strings.HasPrefix(name, "eval_") {
					t.Fatalf("unsafe cleanup target %q", name)
				}
			}
		})
	}
}

func TestTEIFixtureDiagnosticsUsePerQueryOnlineTiming(t *testing.T) {
	fake := &fixtureQdrant{exists: make(map[string]bool)}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer server.Close()
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	originalSecondVector := append(Vector(nil), dataset.Facts[1].Vector...)
	embedder := &recordingPurposeEmbedder{}
	current := time.Unix(0, 0)
	now := func() time.Time {
		value := current
		current = current.Add(time.Microsecond)
		return value
	}
	report, err := Run(context.Background(), dataset, RunOptions{
		Source: "tei-fixture", QdrantURL: server.URL, Embedder: embedder, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Facts[1].Vector[0] != originalSecondVector[0] ||
		dataset.Facts[1].Vector[1] != originalSecondVector[1] {
		t.Fatal("tei-fixture run mutated source dataset vectors")
	}
	if report.Diagnostics == nil || report.Diagnostics.Corpus == nil {
		t.Fatalf("diagnostics = %#v", report.Diagnostics)
	}
	if report.Diagnostics.Corpus.EmbeddingCount != 2 ||
		report.Diagnostics.Query.Embed.Count != 1 ||
		report.Diagnostics.Query.Search.Count != 1 ||
		report.Diagnostics.Query.Total.Count != 1 {
		t.Fatalf("diagnostic counts = %#v", report.Diagnostics)
	}
	if report.Diagnostics.Query.Embed.Min != 1 ||
		report.Diagnostics.Query.Search.Min != 1 ||
		report.Diagnostics.Query.Total.Min != 5 {
		t.Fatalf("diagnostic timings = %#v", report.Diagnostics.Query)
	}
	wantPurposes := []embeddings.Purpose{
		embeddings.FactPassage, embeddings.ChunkPassage,
		embeddings.FolderPassage, embeddings.RetrievalQuery,
	}
	if len(embedder.calls) != len(wantPurposes) {
		t.Fatalf("purpose calls = %v", embedder.calls)
	}
	for i, want := range wantPurposes {
		if embedder.calls[i] != want {
			t.Fatalf("purpose call %d = %v, want %v", i, embedder.calls[i], want)
		}
	}
}

func TestTEIFixtureQueryFailureCleansAllTemporaryCollections(t *testing.T) {
	fake := &fixtureQdrant{exists: make(map[string]bool)}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer server.Close()
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), dataset, RunOptions{
		Source: "tei-fixture", QdrantURL: server.URL,
		Embedder: &recordingPurposeEmbedder{failPurpose: embeddings.RetrievalQuery},
	})
	if err == nil || !strings.Contains(err.Error(), "embed query") {
		t.Fatalf("Run() error = %v", err)
	}
	if len(fake.deleted) != 3 || len(fake.exists) != 0 {
		t.Fatalf("cleanup deleted=%v leaked=%v", fake.deleted, fake.exists)
	}
}

func TestTEIFixtureMissingCorpusTextStopsBeforeTemporaryCreation(t *testing.T) {
	fake := &fixtureQdrant{exists: make(map[string]bool)}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer server.Close()
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts[0].Payload["text"] = nil
	_, err = Run(context.Background(), dataset, RunOptions{
		Source: "tei-fixture", QdrantURL: server.URL,
		Embedder: &recordingPurposeEmbedder{},
	})
	if err == nil || !strings.Contains(err.Error(), `facts point "42"`) {
		t.Fatalf("Run() error = %v", err)
	}
	if fake.createCount != 0 || len(fake.deleted) != 0 || len(fake.exists) != 0 {
		t.Fatalf("external temporary state changed: %#v", fake)
	}
}
