package eval

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

type fixtureQdrant struct {
	mu                  sync.Mutex
	exists              map[string]bool
	createCount         int
	failCreateAt        int
	responseLoss        bool
	failSeed            bool
	deleted             []string
	searchResult        string
	failInfoAfterCreate int
	deleteAttempts      int
	blockDeleteKind     string
}

func (fake *fixtureQdrant) handler(w http.ResponseWriter, r *http.Request) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/collections/"), "/")
	name := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			if fake.createCount > 0 && fake.failInfoAfterCreate > 0 {
				fake.failInfoAfterCreate--
				http.Error(w, "inspection unavailable", http.StatusInternalServerError)
				return
			}
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
			fake.deleteAttempts++
			if fake.blockDeleteKind != "" && strings.Contains(name, fake.blockDeleteKind) {
				<-r.Context().Done()
				return
			}
			if !fake.exists[name] {
				http.NotFound(w, r)
				return
			}
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
			result := fake.searchResult
			if result == "" {
				result = `[]`
			}
			fmt.Fprintf(w, `{"result":%s}`, result)
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
	vector      []float32
}

func (embedder *recordingPurposeEmbedder) EmbedWithPurpose(_ context.Context, text string,
	purpose embeddings.Purpose, _ embeddings.InputProfile, _ string) ([]float32, error) {
	embedder.calls = append(embedder.calls, purpose)
	embedder.inputs = append(embedder.inputs, []string{text})
	if embedder.failPurpose == purpose {
		return nil, fmt.Errorf("purpose failure")
	}
	vector := embedder.vector
	if vector == nil {
		vector = []float32{1, 0}
	}
	return append([]float32(nil), vector...), embedder.fail
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

func TestRelativeLexicalPathIsPlatformNeutralAndContained(t *testing.T) {
	tests := []struct {
		name, value, root, want string
		ok                      bool
	}{
		{"drive outside POSIX root", `C:\deploy\secret.md`, "/docs", "", false},
		{"drive slash outside POSIX root", `C:/deploy/secret.md`, "/docs", "", false},
		{"UNC outside root", `\\server\share\secret.md`, "/docs", "", false},
		{"UNC slash outside root", `//server/share/secret.md`, "/docs", "", false},
		{"backslash traversal", `..\secret.md`, "", "", false},
		{"nested backslash traversal", `safe\..\..\secret.md`, "", "", false},
		{"safe windows relative", `project\design.md`, "", "project/design.md", true},
		{"drive under root", `C:\docs\project\design.md`, `C:\docs`, "project/design.md", true},
		{"POSIX under root", "/docs/project/design.md", "/docs", "project/design.md", true},
		{"POSIX outside root", "/private/design.md", "/docs", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := relativeLexicalPath(tt.value, tt.root)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("relativeLexicalPath(%q, %q) = %q,%t want %q,%t",
					tt.value, tt.root, got, ok, tt.want, tt.ok)
			}
			if strings.Contains(got, tt.root) && tt.root != "" {
				t.Fatalf("lexical path leaked deployment root: %q", got)
			}
		})
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

func TestExperimentOverridesRejectFixtureInputProfileRelabel(t *testing.T) {
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	profile := LegacyRawV1
	if _, err := WithExperimentOverrides(dataset, ExperimentOverrides{
		InputProfile: &profile,
	}, "fixture"); err == nil || !strings.Contains(err.Error(), "cannot relabel") {
		t.Fatalf("fixture profile override error = %v", err)
	}
}

func TestLiveInputProfileOverrideClearsOnlyCloneVectorsWhenProfileChanges(t *testing.T) {
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Embedding.ModelID = multilingualE5SmallModelID
	second := dataset.Queries[0]
	second.ID = "q2"
	second.Vector = append(Vector(nil), second.Vector...)
	dataset.Queries = append(dataset.Queries, second)
	original := append(Vector(nil), dataset.Queries[0].Vector...)
	changed := MultilingualE5V1
	cloned, err := WithExperimentOverrides(dataset, ExperimentOverrides{
		InputProfile: &changed,
	}, "live")
	if err != nil {
		t.Fatal(err)
	}
	if len(cloned.Queries[0].Vector) != 0 || len(cloned.Queries[1].Vector) != 0 {
		t.Fatalf("changed-profile clone retained vectors %v / %v",
			cloned.Queries[0].Vector, cloned.Queries[1].Vector)
	}
	if len(dataset.Queries[0].Vector) != len(original) ||
		dataset.Queries[0].Vector[0] != original[0] ||
		len(dataset.Queries[1].Vector) != len(original) {
		t.Fatal("profile override mutated source query vector")
	}

	same := LegacyRawV1
	unchanged, err := WithExperimentOverrides(dataset, ExperimentOverrides{
		InputProfile: &same,
	}, "live")
	if err != nil {
		t.Fatal(err)
	}
	if len(unchanged.Queries[0].Vector) != len(original) ||
		unchanged.Queries[0].Vector[0] != original[0] ||
		len(unchanged.Queries[1].Vector) != len(original) {
		t.Fatal("same-profile override cleared precomputed vector")
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
	embedder := &recordingPurposeEmbedder{}
	materialized, diagnostics, err := Materialize(
		context.Background(), dataset, embedder, MaterializeOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	count := diagnostics.Facts + diagnostics.Chunks + diagnostics.Folders
	if count != len(dataset.Facts)+len(dataset.Chunks)+len(dataset.Folders) {
		t.Fatalf("embedded count = %d", count)
	}
	wantPurposes := []embeddings.Purpose{
		embeddings.FactPassage, embeddings.ChunkPassage,
		embeddings.FolderPassage, embeddings.RetrievalQuery,
	}
	for i, want := range wantPurposes {
		if embedder.calls[i] != want {
			t.Fatalf("purpose call %d = %v, want %v", i, embedder.calls[i], want)
		}
	}
	if dataset.Facts[1].Vector[0] != 0 || materialized.Facts[1].Vector[0] != 1 {
		t.Fatal("TEI fixture corpus preparation mutated the source or failed to replace clone vectors")
	}
}

func TestTEIFixtureCorpusEmbeddingRejectsMissingText(t *testing.T) {
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts[0].Payload["text"] = 42
	if _, _, err := Materialize(
		context.Background(), dataset, &recordingPurposeEmbedder{}, MaterializeOptions{},
	); err == nil ||
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
		name                string
		failCreateAt        int
		responseLoss        bool
		failSeed            bool
		wantError           bool
		wantDeleteCount     int
		failInfoAfterCreate int
		wantDeleteAttempts  int
	}{
		{"success", 0, false, false, false, 3, 0, 3},
		{"seed failure", 0, false, true, true, 3, 0, 3},
		{"first create response loss", 1, true, false, true, 1, 0, 1},
		{"second create response loss", 2, true, false, true, 2, 0, 2},
		{"response loss inspection failure", 1, true, false, true, 1, 2, 1},
		{"genuine no-create 404 cleanup", 1, false, false, true, 0, 2, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fixtureQdrant{
				exists: make(map[string]bool), failCreateAt: tt.failCreateAt,
				responseLoss: tt.responseLoss, failSeed: tt.failSeed,
				failInfoAfterCreate: tt.failInfoAfterCreate,
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
			if fake.deleteAttempts != tt.wantDeleteAttempts {
				t.Fatalf("delete attempts = %d, want %d", fake.deleteAttempts, tt.wantDeleteAttempts)
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

func TestFixtureCleanupUsesIndependentDeadlinePerCollection(t *testing.T) {
	fake := &fixtureQdrant{
		exists: make(map[string]bool), blockDeleteKind: "folders",
	}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer server.Close()
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), dataset, RunOptions{
		Source: "fixture", QdrantURL: server.URL, CleanupTimeout: 20 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "clean up") {
		t.Fatalf("cleanup error = %v", err)
	}
	if fake.deleteAttempts != 3 {
		t.Fatalf("delete attempts = %d, want all 3", fake.deleteAttempts)
	}
	if len(fake.deleted) != 2 {
		t.Fatalf("later cleanup attempts did not succeed: deleted=%v", fake.deleted)
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
	dataset.Folders = []FixturePoint{{
		ID:     dataset.Facts[0].ID,
		Vector: Vector{1, 0},
		Payload: map[string]any{
			"summary": "summary-only folder",
		},
	}}
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
	if report.Diagnostics.Corpus.EmbeddingCount != 3 ||
		report.Diagnostics.Query.Embed.Count != 1 ||
		report.Diagnostics.Query.Search.Count != 1 ||
		report.Diagnostics.Query.Total.Count != 1 {
		t.Fatalf("diagnostic counts = %#v", report.Diagnostics)
	}
	if report.Diagnostics.Query.Embed.Min != 1 ||
		report.Diagnostics.Query.Search.Min != 1 ||
		report.Diagnostics.Query.Total.Min != 3 {
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
	if !reflect.DeepEqual(embedder.inputs[2], []string{"summary-only folder"}) {
		t.Fatalf("folder inputs = %#v", embedder.inputs[2])
	}
}

func TestTEIFixtureQueryMaterializationFailureStopsBeforeTemporaryCollections(t *testing.T) {
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
	if err == nil || !strings.Contains(err.Error(), "embed queries") {
		t.Fatalf("Run() error = %v", err)
	}
	if fake.createCount != 0 || len(fake.deleted) != 0 || len(fake.exists) != 0 {
		t.Fatalf("external temporary state changed: %#v", fake)
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

func TestFixtureSourcesPropagateDocumentsRootForPathLift(t *testing.T) {
	for _, source := range []string{"fixture", "tei-fixture"} {
		t.Run(source, func(t *testing.T) {
			otherID := "11111111-1111-4111-8111-111111111111"
			fake := &fixtureQdrant{
				exists: make(map[string]bool),
				searchResult: fmt.Sprintf(`[
					{"id":"%s","score":0.99,"payload":{"text":"general","file_path":"/private/deploy/PM-1427.md"}},
					{"id":42,"score":0.4,"payload":{"text":"general","file_path":"/docs/project/PM-1427.md"}}
				]`, otherID),
			}
			server := httptest.NewServer(http.HandlerFunc(fake.handler))
			defer server.Close()
			dataset, err := Load(strings.NewReader(validV3Dataset()))
			if err != nil {
				t.Fatal(err)
			}
			dataset.Chunks, err = cloneFixturePoints(dataset.Facts)
			if err != nil {
				t.Fatal(err)
			}
			dataset.Chunks[0].Payload = map[string]any{
				"text": "general", "file_path": "/docs/project/PM-1427.md",
			}
			dataset.Chunks[1].Payload = map[string]any{
				"text": "general", "file_path": "/private/deploy/PM-1427.md",
			}
			dataset.Queries[0].Target = "documents"
			dataset.Queries[0].Mode = "flat"
			dataset.Queries[0].Text = "PM-1427"
			dataset.Queries[0].ForbiddenIDs = nil
			dataset.Queries[0].LifecycleExpectations = nil
			dataset.Configuration.RetrievalStrategy = RetrievalHybridRRF
			dataset.Configuration.DenseCandidateLimit = 20
			dataset.Configuration.RRFConstant = 60
			options := RunOptions{
				Source: source, QdrantURL: server.URL, DocumentsRoot: "/docs",
			}
			if source == "tei-fixture" {
				options.Embedder = &recordingPurposeEmbedder{}
			}
			report, err := Run(context.Background(), dataset, options)
			if err != nil {
				t.Fatal(err)
			}
			if got := report.Queries[0].Results; len(got) < 1 || got[0].ID != "42" {
				t.Fatalf("path-lift results = %+v", got)
			}
		})
	}
}

func TestV3VectorOnlyFixtureAndLivePreserveDenseOutput(t *testing.T) {
	otherID := "11111111-1111-4111-8111-111111111111"
	searchResult := fmt.Sprintf(`[
		{"id":"%s","score":0.99,"payload":{"text":"general","lifecycle_state":"current","canonical":false,"supersedes":[],"superseded_by":[]}},
		{"id":42,"score":0.4,"payload":{"text":"PM-1427","lifecycle_state":"current","canonical":false,"supersedes":[],"superseded_by":[]}}
	]`, otherID)
	fixtureFake := &fixtureQdrant{
		exists: make(map[string]bool), searchResult: searchResult,
	}
	fixtureServer := httptest.NewServer(http.HandlerFunc(fixtureFake.handler))
	defer fixtureServer.Close()
	fixtureDataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	fixtureDataset.Queries[0].Text = "PM-1427"
	fixtureReport, err := Run(context.Background(), fixtureDataset, RunOptions{
		Source: "fixture", QdrantURL: fixtureServer.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	liveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/collections/memory" {
			writeLiveIdentityCollection(w, 2)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/collections/memory/points/search" {
			fmt.Fprintf(w, `{"result":%s}`, searchResult)
			return
		}
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer liveServer.Close()
	liveDataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	liveDataset.Embedding.Provider = "tei"
	liveDataset.Facts = nil
	liveDataset.Queries[0].Text = "PM-1427"
	liveReport, err := Run(context.Background(), liveDataset, RunOptions{
		Source: "live", QdrantURL: liveServer.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, report := range []Report{fixtureReport, liveReport} {
		got := report.Queries[0].Results
		if len(got) != 2 || got[0].ID != otherID || got[0].Score != .99 ||
			got[1].ID != "42" || got[1].Score != .4 {
			t.Fatalf("%s vector-only results = %+v", report.Mode, got)
		}
	}
}
