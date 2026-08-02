package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
)

func TestPublicV2FixtureRunnerIntegration(t *testing.T) {
	qdrantURL := os.Getenv("QDRANT_TEST_URL")
	if qdrantURL == "" {
		t.Skip("QDRANT_TEST_URL is not set")
	}
	before := listEvaluationCollections(t, qdrantURL)
	file, err := os.Open(filepath.Join("..", "..", "evaldata", "public", "v2", "dataset.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close public dataset: %v", err)
		}
	})
	dataset, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), dataset, RunOptions{Source: "fixture", QdrantURL: qdrantURL})
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != LifecycleSchemaVersion || len(report.Queries) != 16 ||
		report.Aggregate.HitAt[1] != 1 || !report.GatesPassed || report.Lifecycle == nil {
		t.Fatalf("report = %#v", report)
	}
	if report.Lifecycle.Aggregate.Checks == 0 ||
		report.Lifecycle.Aggregate.Violations != 0 ||
		report.Lifecycle.Aggregate.CanonicalPreferenceChecks == 0 ||
		report.Lifecycle.Aggregate.CanonicalPreferenceViolations != 0 ||
		len(report.Lifecycle.Transitions) != 20 {
		t.Fatalf("lifecycle coverage = %#v", report.Lifecycle)
	}
	first, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	secondReport, err := Run(context.Background(), dataset, RunOptions{Source: "fixture", QdrantURL: qdrantURL})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderJSON(secondReport)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("independent Qdrant runs produced different reports")
	}
	baseline, err := os.ReadFile(filepath.Join("..", "..", "evaldata", "public", "v2", "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, baseline) {
		t.Fatal("public baseline differs from a fresh Qdrant report")
	}
	after := waitForEvaluationCollections(t, qdrantURL, before)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("eval collections leaked: before=%v after=%v", before, after)
	}
}

func TestCanonicalPreferenceViolationsProduceGateFailure(t *testing.T) {
	if got := canonicalPreferenceFailureMessages("canonical-query", 0); got != nil {
		t.Fatalf("zero violations produced failures: %v", got)
	}
	got := canonicalPreferenceFailureMessages("canonical-query", 2)
	if len(got) != 1 || got[0] != "query canonical-query invariant canonical_preference" {
		t.Fatalf("canonical failures = %v", got)
	}
	failures := EvaluateGates(AggregateMetrics{}, Gates{})
	failures = append(failures, got...)
	if len(failures) == 0 {
		t.Fatal("canonical preference violation did not fail lifecycle gate")
	}
}

func TestPublicV2DatasetLoads(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "evaldata", "public", "v2", "dataset.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close public dataset: %v", err)
		}
	})
	dataset, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if dataset.SchemaVersion != LifecycleSchemaVersion ||
		dataset.DatasetVersion != "2.0.0" ||
		dataset.Embedding.Provider != "synthetic" ||
		dataset.Embedding.ModelID != "personal-memory-golden-v2" ||
		dataset.Embedding.ModelRevision != "2" ||
		dataset.Embedding.DType != "float32" ||
		dataset.Embedding.Pooling != "mean" ||
		dataset.Embedding.VectorSize != 16 ||
		len(dataset.Queries) != 16 ||
		len(dataset.TransitionScenarios) != 20 ||
		!dataset.Gates.ForbidInvariantViolations ||
		!dataset.Gates.ForbidLifecycleViolations {
		t.Fatalf("dataset identity/coverage = %#v", dataset)
	}

	queryByID := make(map[string]Query, len(dataset.Queries))
	for _, query := range dataset.Queries {
		queryByID[query.ID] = query
	}
	for _, id := range []string{
		"fact-ambiguous-en",
		"fact-legacy-numeric-en",
		"fact-multilingual",
		"fact-russian",
		"document-flat",
		"document-hierarchical",
		"document-hierarchical-fallback",
		"document-missing-text",
	} {
		if _, exists := queryByID[id]; !exists {
			t.Errorf("v2 dataset does not preserve v1 query %q", id)
		}
	}
	for id, intent := range map[string]QueryIntent{
		"lifecycle-current-superseded":   QueryIntentCurrent,
		"lifecycle-history":              QueryIntentHistory,
		"lifecycle-as-of-expiry":         QueryIntentAsOf,
		"lifecycle-uncertainty":          QueryIntentUncertainty,
		"lifecycle-expired-canonical":    QueryIntentCurrent,
		"lifecycle-permanent-historical": QueryIntentCurrent,
		"lifecycle-canonical-preference": QueryIntentCurrent,
		"lifecycle-legacy-invalid":       QueryIntentCurrent,
	} {
		query, exists := queryByID[id]
		if !exists {
			t.Errorf("missing lifecycle query %q", id)
			continue
		}
		if query.EffectiveIntent() != intent || len(query.LifecycleExpectations) == 0 {
			t.Errorf("lifecycle query %q intent/expectations = %q/%d", id, query.EffectiveIntent(), len(query.LifecycleExpectations))
		}
	}
	if queryByID["lifecycle-as-of-expiry"].AsOf != "2025-03-14" {
		t.Errorf("public as_of date = %q", queryByID["lifecycle-as-of-expiry"].AsOf)
	}

	validPairs := make(map[string]int)
	invalidScenarios := make(map[string]bool)
	for _, scenario := range dataset.TransitionScenarios {
		if scenario.ExpectedValid {
			key := string(scenario.SourceLifecycle.State) + "->" + string(scenario.TargetLifecycle.State)
			validPairs[key]++
			wantID := "transition-" + string(scenario.SourceLifecycle.State) + "-to-" + string(scenario.TargetLifecycle.State)
			if scenario.ID != wantID {
				t.Errorf("transition pair %q ID = %q, want %q", key, scenario.ID, wantID)
			}
		}
		if strings.HasPrefix(scenario.ID, "invalid-") &&
			!scenario.ExpectedValid &&
			scenario.ExpectedReasonCode == string(ReasonTargetInvalid) {
			invalidScenarios[scenario.ID] = true
		}
	}
	states := []lifecycle.State{
		lifecycle.Current,
		lifecycle.Historical,
		lifecycle.Superseded,
		lifecycle.Disputed,
	}
	for _, source := range states {
		for _, target := range states {
			key := string(source) + "->" + string(target)
			if validPairs[key] != 1 {
				t.Errorf("valid transition pair %q count = %d, want exactly 1", key, validPairs[key])
			}
		}
	}
	for _, id := range []string{
		"invalid-canonical-historical",
		"invalid-superseded-without-successor",
		"invalid-current-with-successor",
		"invalid-self-reference",
	} {
		if !invalidScenarios[id] {
			t.Errorf("missing invalid transition coverage %q", id)
		}
	}
}

func TestPublicV2BaselineLoads(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "evaldata", "public", "v2", "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := DecodeReport(data)
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != LifecycleSchemaVersion ||
		report.DatasetVersion != "2.0.0" ||
		!report.GatesPassed ||
		report.Lifecycle == nil ||
		report.Lifecycle.Aggregate.Checks == 0 ||
		report.Lifecycle.Aggregate.Violations != 0 ||
		report.Lifecycle.Aggregate.CanonicalPreferenceChecks == 0 ||
		report.Lifecycle.Aggregate.CanonicalPreferenceViolations != 0 ||
		len(report.Lifecycle.Transitions) != 20 {
		t.Fatalf("v2 baseline coverage = %#v", report)
	}
}

func TestPublicV1BaselineByteCompatibility(t *testing.T) {
	qdrantURL := os.Getenv("QDRANT_TEST_URL")
	if qdrantURL == "" {
		t.Skip("QDRANT_TEST_URL is not set")
	}
	before := listEvaluationCollections(t, qdrantURL)
	file, err := os.Open(filepath.Join("..", "..", "evaldata", "public", "v1", "dataset.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close v1 public dataset: %v", err)
		}
	})
	dataset, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), dataset, RunOptions{Source: "fixture", QdrantURL: qdrantURL})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := os.ReadFile(filepath.Join("..", "..", "evaldata", "public", "v1", "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rendered, baseline) {
		t.Fatal("v1 public baseline differs from a fresh Qdrant report")
	}
	after := waitForEvaluationCollections(t, qdrantURL, before)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("v1 eval collections leaked: before=%v after=%v", before, after)
	}
}

func TestLiveRunnerUsesOnlySearchRequests(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Method != http.MethodPost || r.URL.Path != "/collections/memory/points/search" {
			t.Errorf("live runner made non-search request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"result":[{"id":42,"score":1,"payload":{"text":"numeric"}}]}`))
	}))
	defer server.Close()
	dataset, err := Load(strings.NewReader(validDataset))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts = nil
	report, err := Run(context.Background(), dataset, RunOptions{Source: "live", QdrantURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || report.Aggregate.MRR != 1 {
		t.Fatalf("paths/report = %v/%#v", paths, report)
	}
}

func TestLiveV3MinimumDatasetRunRenderDecodeRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/collections/memory" {
			_, _ = w.Write([]byte(`{"result":{
				"points_count":1,
				"config":{
					"params":{"vectors":{"size":2,"distance":"Cosine"}},
					"metadata":{"personal_memory.embedding":{
						"schema_version":1,
						"provider":"tei",
						"model_id":"synthetic-eval-v1",
						"model_revision":"v1",
						"model_dtype":"float32",
						"pooling":"mean",
						"vector_size":2,
						"input_profile":"legacy-raw-v1"
					}}
				}
			}}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/collections/memory/points/search" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"result":[{
			"id":42,
			"score":1,
			"payload":{
				"text":"numeric",
				"lifecycle_state":"current",
				"canonical":true
			}
		}]}`))
	}))
	defer server.Close()

	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts = nil
	dataset.Embedding.Provider = "tei"
	report, err := Run(context.Background(), dataset, RunOptions{
		Source: "live", QdrantURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Queries) != 1 || len(report.Cohorts) != 3 {
		t.Fatalf("minimum v3 report query/cohort counts = %d/%d", len(report.Queries), len(report.Cohorts))
	}
	encoded, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"cohorts": [`)) {
		t.Fatalf("minimum v3 report omitted cohorts: %s", encoded)
	}
	decoded, err := DecodeReport(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Queries) != 1 || len(decoded.Cohorts) != 3 {
		t.Fatalf("decoded minimum v3 report query/cohort counts = %d/%d", len(decoded.Queries), len(decoded.Cohorts))
	}
}

func TestLiveV3HybridFiltersBeforeBoundedRerankAndPreservesScore(t *testing.T) {
	var searchBody map[string]any
	searches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/collections/memory" {
			writeLiveIdentityCollection(w, 1)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/collections/memory/points/search" {
			searches++
			if err := json.NewDecoder(r.Body).Decode(&searchBody); err != nil {
				t.Errorf("decode search: %v", err)
			}
			_, _ = w.Write([]byte(`{"result":[
				{"id":43,"score":0.99,"payload":{"text":"general memory","lifecycle_state":"current","canonical":false,"supersedes":[],"superseded_by":[]}},
				{"id":42,"score":0.4,"payload":{"text":"incident PM-1427","lifecycle_state":"current","canonical":false,"supersedes":[],"superseded_by":[]}}
			]}`))
			return
		}
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Embedding.Provider = "tei"
	dataset.Facts = nil
	dataset.Queries[0].Text = "PM-1427"
	dataset.Configuration.RetrievalStrategy = RetrievalHybridRRF
	dataset.Configuration.DenseCandidateLimit = 20
	dataset.Configuration.RRFConstant = 60
	report, err := Run(context.Background(), dataset, RunOptions{
		Source: "live", QdrantURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if searches != 1 || searchBody["limit"] != float64(20) || searchBody["filter"] == nil {
		t.Fatalf("search request = %#v searches=%d", searchBody, searches)
	}
	if got := report.Queries[0].Results; len(got) != 2 || got[0].ID != "42" || got[0].Score != .4 {
		t.Fatalf("hybrid results = %+v", got)
	}
}

func writeLiveIdentityCollection(w http.ResponseWriter, points int) {
	fmt.Fprintf(w, `{"result":{
		"points_count":%d,
		"config":{"params":{"vectors":{"size":2,"distance":"Cosine"}},"metadata":{
			"personal_memory.embedding":{
				"schema_version":1,"provider":"tei","model_id":"synthetic-eval-v1",
				"model_revision":"v1","model_dtype":"float32","pooling":"mean",
				"vector_size":2,"input_profile":"legacy-raw-v1"
			}
		}}
	}}`, points)
}

func TestLiveV3HierarchicalHybridRanksFoldersChunksAndFallsBack(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback=%t", fallback), func(t *testing.T) {
			chunkSearches := 0
			sawFiltered := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet &&
					(r.URL.Path == "/collections/doc_chunks" || r.URL.Path == "/collections/doc_folders") {
					writeLiveIdentityCollection(w, 1)
					return
				}
				if r.Method == http.MethodPost && r.URL.Path == "/collections/doc_folders/points/search" {
					_, _ = w.Write([]byte(`{"result":[
						{"id":51,"score":0.99,"payload":{"text":"general","folder_path":"/docs/a"}},
						{"id":52,"score":0.4,"payload":{"summary":"PM-1427 folder","folder_path":"/docs/b"}}
					]}`))
					return
				}
				if r.Method == http.MethodPost && r.URL.Path == "/collections/doc_chunks/points/search" {
					chunkSearches++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode chunk search: %v", err)
					}
					if body["filter"] != nil {
						sawFiltered = true
						encoded, _ := json.Marshal(body["filter"])
						if !bytes.Contains(encoded, []byte(`/docs/b`)) {
							t.Errorf("selected folder filter = %s", encoded)
						}
						if fallback {
							_, _ = w.Write([]byte(`{"result":[]}`))
							return
						}
					}
					_, _ = w.Write([]byte(`{"result":[
						{"id":43,"score":0.99,"payload":{"text":"general","heading":"other","file_path":"/docs/b/other.md"}},
						{"id":42,"score":0.4,"payload":{"text":"PM-1427 details","heading":"incident","file_path":"/docs/b/incident.md"}}
					]}`))
					return
				}
				http.Error(w, "unexpected request", http.StatusInternalServerError)
			}))
			defer server.Close()
			dataset, err := Load(strings.NewReader(validV3Dataset()))
			if err != nil {
				t.Fatal(err)
			}
			dataset.Embedding.Provider = "tei"
			dataset.Queries[0].Target = "documents"
			dataset.Queries[0].Mode = "hierarchical"
			dataset.Queries[0].Text = "PM-1427"
			dataset.Queries[0].LifecycleExpectations = nil
			dataset.Queries[0].ForbiddenIDs = nil
			dataset.Configuration.RetrievalStrategy = RetrievalHybridRRF
			dataset.Configuration.DenseCandidateLimit = 20
			dataset.Configuration.RRFConstant = 60
			report, err := Run(context.Background(), dataset, RunOptions{
				Source: "live", QdrantURL: server.URL, DocumentsRoot: "/docs",
			})
			if err != nil {
				t.Fatal(err)
			}
			wantSearches := 1
			if fallback {
				wantSearches = 2
			}
			if !sawFiltered || chunkSearches != wantSearches {
				t.Fatalf("filtered=%t chunkSearches=%d", sawFiltered, chunkSearches)
			}
			if got := report.Queries[0].Results; len(got) != 2 || got[0].ID != "42" || got[0].Score != .4 {
				t.Fatalf("hierarchical results = %+v", got)
			}
		})
	}
}

func TestLiveV3LegacyIdentityAndPurposeAwareQueryEmbedding(t *testing.T) {
	searches := 0
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/collections/memory" {
			_, _ = w.Write([]byte(`{"result":{"points_count":1,"config":{
				"params":{"vectors":{"size":2,"distance":"Cosine"}},
				"metadata":{"personal_memory.embedding":{
					"schema_version":1,"provider":"tei","model_id":"synthetic-eval-v1",
					"model_revision":"v1","model_dtype":"float32","pooling":"mean","vector_size":2
				}}
			}}}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/collections/memory/points/search" {
			searches++
			_, _ = w.Write([]byte(`{"result":[{"id":42,"score":1,"payload":{
				"text":"numeric","lifecycle_state":"current","canonical":true,
				"supersedes":[],"superseded_by":[]
			}}]}`))
			return
		}
		writes++
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Embedding.Provider = "tei"
	dataset.Facts = nil
	dataset.Queries[0].Vector = nil
	embedder := &recordingPurposeEmbedder{}
	report, err := Run(context.Background(), dataset, RunOptions{
		Source: "live", QdrantURL: server.URL, Embedder: embedder,
	})
	if err != nil {
		t.Fatal(err)
	}
	if searches != 1 || writes != 0 || len(embedder.calls) != 1 ||
		embedder.calls[0] != embeddings.RetrievalQuery ||
		report.Diagnostics.Query.Embed.Count != 1 {
		t.Fatalf("searches=%d writes=%d purposes=%v diagnostics=%#v",
			searches, writes, embedder.calls, report.Diagnostics)
	}
}

func TestLiveV3IdentityMismatchOrMalformedStopsBeforeSearchAndWrites(t *testing.T) {
	for _, profileJSON := range []string{`"multilingual-e5-v1"`, `""`, `null`} {
		t.Run(profileJSON, func(t *testing.T) {
			searches, writes := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/collections/memory" {
					fmt.Fprintf(w, `{"result":{"points_count":1,"config":{
						"params":{"vectors":{"size":2,"distance":"Cosine"}},
						"metadata":{"personal_memory.embedding":{
							"schema_version":1,"provider":"tei","model_id":"synthetic-eval-v1",
							"model_revision":"v1","model_dtype":"float32","pooling":"mean",
							"vector_size":2,"input_profile":%s
						}}
					}}}`, profileJSON)
					return
				}
				if strings.Contains(r.URL.Path, "/points/search") {
					searches++
				} else {
					writes++
				}
				http.Error(w, "unexpected request", http.StatusInternalServerError)
			}))
			defer server.Close()
			dataset, err := Load(strings.NewReader(validV3Dataset()))
			if err != nil {
				t.Fatal(err)
			}
			dataset.Embedding.Provider = "tei"
			dataset.Facts = nil
			if _, err := Run(context.Background(), dataset, RunOptions{
				Source: "live", QdrantURL: server.URL,
			}); err == nil {
				t.Fatal("identity verification unexpectedly succeeded")
			}
			if searches != 0 || writes != 0 {
				t.Fatalf("searches=%d writes=%d", searches, writes)
			}
		})
	}
}

func TestLiveV3HybridLifecycleAuthorityUsesPoolBeforeTopK(t *testing.T) {
	for _, intent := range []QueryIntent{
		QueryIntentCurrent, QueryIntentHistory, QueryIntentAsOf, QueryIntentUncertainty,
	} {
		t.Run(string(intent), func(t *testing.T) {
			searches := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/collections/memory" {
					writeLiveIdentityCollection(w, 4)
					return
				}
				if r.Method == http.MethodPost && r.URL.Path == "/collections/memory/points/search" {
					searches++
					state := "historical"
					if intent == QueryIntentCurrent {
						state = "current"
					}
					fmt.Fprintf(w, `{"result":[
						{"id":43,"score":0.99,"payload":{"text":"PM-1427 alpha","lifecycle_state":"%s","canonical":false,"supersedes":[],"superseded_by":[]}},
						{"id":44,"score":0.98,"payload":{"text":"PM-1427 beta","lifecycle_state":"%s","canonical":false,"supersedes":[],"superseded_by":[]}},
						{"id":45,"score":0.97,"payload":{"text":"PM-1427 gamma","lifecycle_state":"%s","canonical":false,"supersedes":[],"superseded_by":[]}},
						{"id":42,"score":0.1,"payload":{"text":"unrelated canonical","lifecycle_state":"current","canonical":true,"supersedes":[],"superseded_by":[]}}
					]}`, state, state, state)
					return
				}
				http.Error(w, "unexpected request", http.StatusInternalServerError)
			}))
			defer server.Close()
			dataset, err := Load(strings.NewReader(validV3Dataset()))
			if err != nil {
				t.Fatal(err)
			}
			dataset.Embedding.Provider = "tei"
			dataset.Facts = nil
			dataset.Queries[0].Intent = intent
			dataset.Queries[0].AsOf = ""
			if intent == QueryIntentAsOf {
				dataset.Queries[0].AsOf = "2026-01-01"
			}
			dataset.Queries[0].Text = "PM-1427"
			dataset.Queries[0].LifecycleExpectations = nil
			dataset.Queries[0].ForbiddenIDs = nil
			dataset.Configuration.RetrievalStrategy = RetrievalHybridRRF
			dataset.Configuration.DenseCandidateLimit = 20
			dataset.Configuration.RRFConstant = 60
			report, err := Run(context.Background(), dataset, RunOptions{
				Source: "live", QdrantURL: server.URL,
			})
			if err != nil {
				t.Fatal(err)
			}
			if searches != 1 {
				t.Fatalf("searches = %d", searches)
			}
			results := report.Queries[0].Results
			if len(results) != 3 || results[0].ID != "42" {
				t.Fatalf("authority results = %+v", results)
			}
		})
	}
}

func TestLiveV2LifecycleEvidenceUsesExactReadWithoutChangingRanking(t *testing.T) {
	var requestBody map[string]any
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/collections/memory/points/search":
			if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
				t.Errorf("decode lifecycle ranking request: %v", err)
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			if _, filtered := requestBody["filter"]; !filtered {
				results := make([]map[string]any, 101)
				for i := range results {
					results[i] = map[string]any{
						"id": 1000 + i, "score": 1 - float64(i)/1000,
						"payload": map[string]any{
							"text": "obsolete", "lifecycle_state": "historical",
						},
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"result": results})
				return
			}
			_, _ = w.Write([]byte(`{"result":[
				{"id":42,"score":0.7,"payload":{"text":"current","lifecycle_state":"current","canonical":true}}
			]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/collections/memory/points/999":
			_, _ = w.Write([]byte(`{"result":{
				"id":999,"vector":[1,0],
				"payload":{"text":"expected obsolete","lifecycle_state":"historical"}
			}}`))
		default:
			t.Errorf("unexpected lifecycle request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	dataset, err := Load(strings.NewReader(validV2Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts = nil
	expectation := &dataset.Queries[0].LifecycleExpectations[0]
	expectation.ID = "999"
	expectation.State = lifecycle.Historical
	expectation.Decision = PresentationSuppress
	expectation.ReasonCodes = []string{string(ReasonHistorical)}
	dataset.Gates.ForbidLifecycleViolations = true
	report, err := Run(context.Background(), dataset, RunOptions{Source: "live", QdrantURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, filtered := requestBody["filter"]; !filtered {
		t.Fatalf("ranking search omitted current-only filter: %#v", requestBody)
	}
	if report.SchemaVersion != LifecycleSchemaVersion || report.Lifecycle == nil {
		t.Fatalf("v2 lifecycle report missing: %#v", report)
	}
	if got := resultIDs(report.Queries[0].Results); len(got) != 1 || got[0] != "42" ||
		report.Aggregate.MRR != 1 {
		t.Fatalf("relevance result IDs = %v, want only current candidate 42", got)
	}
	assertCandidate(t, *report.Queries[0].Lifecycle, "999", lifecycle.Historical, PresentationSuppress, ReasonHistorical)
	if !report.GatesPassed || len(report.GateFailures) != 0 {
		t.Fatalf("gate result = passed %t failures %#v", report.GatesPassed, report.GateFailures)
	}
	wantRequests := []string{
		"POST /collections/memory/points/search",
		"GET /collections/memory/points/999",
	}
	if fmt.Sprint(requests) != fmt.Sprint(wantRequests) {
		t.Fatalf("requests = %v, want read-only requests %v", requests, wantRequests)
	}
	encoded, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(encoded); err != nil {
		t.Fatalf("decode generated broad-search report: %v", err)
	}
}

func TestLiveFactRankingRespectsLifecycleIntentAndEvidenceBoundary(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/collections/memory/points/search":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode lifecycle intent request: %v", err)
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			if _, current := body["filter"]; current {
				_, _ = w.Write([]byte(`{"result":[
					{"id":42,"score":0.7,"payload":{"text":"current","lifecycle_state":"current","canonical":true}}
				]}`))
				return
			}
			_, _ = w.Write([]byte(`{"result":[
				{"id":43,"score":1.0,"payload":{"text":"historical","lifecycle_state":"historical"}},
				{"id":44,"score":0.9,"payload":{"text":"disputed","lifecycle_state":"disputed"}},
				{"id":42,"score":0.7,"payload":{"text":"current","lifecycle_state":"current","canonical":true}}
			]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/collections/memory/points/999":
			_, _ = w.Write([]byte(`{"result":{
				"id":999,"vector":[1,0],
				"payload":{"text":"outside ranking","lifecycle_state":"historical"}
			}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	historyDataset, err := Load(strings.NewReader(validV2Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	historyDataset.Facts = nil
	historyQuery := &historyDataset.Queries[0]
	historyQuery.Intent = QueryIntentHistory
	expectation := &historyQuery.LifecycleExpectations[0]
	expectation.ID = "999"
	expectation.State = lifecycle.Historical
	expectation.Decision = PresentationInclude
	expectation.ReasonCodes = []string{string(ReasonHistoricalContext)}
	historyReport, err := Run(context.Background(), historyDataset, RunOptions{
		Source: "live", QdrantURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	historyIDs := resultIDs(historyReport.Queries[0].Results)
	if strings.Join(historyIDs, ",") != "42,44,43" {
		t.Fatalf("history Results = %v, want policy-ranked search candidates", historyIDs)
	}
	for _, id := range historyIDs {
		if id == "999" {
			t.Fatal("exact lifecycle evidence inflated history Results")
		}
	}
	assertCandidate(t, *historyReport.Queries[0].Lifecycle, "43", lifecycle.Historical, PresentationInclude, ReasonHistoricalContext)
	assertCandidate(t, *historyReport.Queries[0].Lifecycle, "44", lifecycle.Disputed, PresentationUncertain, ReasonDisputed)
	assertCandidate(t, *historyReport.Queries[0].Lifecycle, "999", lifecycle.Historical, PresentationInclude, ReasonHistoricalContext)
	if historyReport.Aggregate.MRR != 1 {
		t.Fatalf("history MRR = %f, want 1", historyReport.Aggregate.MRR)
	}

	currentDataset, err := Load(strings.NewReader(validV2Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	currentDataset.Facts = nil
	currentReport, err := Run(context.Background(), currentDataset, RunOptions{
		Source: "live", QdrantURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if currentIDs := resultIDs(currentReport.Queries[0].Results); strings.Join(currentIDs, ",") != "42" {
		t.Fatalf("current Results = %v, want historical/disputed excluded", currentIDs)
	}
	wantRequests := []string{
		"POST /collections/memory/points/search",
		"GET /collections/memory/points/999",
		"POST /collections/memory/points/search",
	}
	if fmt.Sprint(requests) != fmt.Sprint(wantRequests) {
		t.Fatalf("requests = %v, want %v", requests, wantRequests)
	}
}

func TestLiveV2CurrentIncludeExpectationKeepsCurrentFilter(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode current-filter request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"result":[
			{"id":42,"score":0.7,"payload":{"text":"current","lifecycle_state":"current","canonical":true}}
		]}`))
	}))
	defer server.Close()

	dataset, err := Load(strings.NewReader(validV2Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts = nil
	report, err := Run(context.Background(), dataset, RunOptions{Source: "live", QdrantURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	filter, filtered := requestBody["filter"].(map[string]any)
	if !filtered || filter["should"] == nil {
		t.Fatalf("current include expectation did not preserve current-only filter: %#v", requestBody)
	}
	encoded, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(encoded); err != nil {
		t.Fatalf("decode generated filtered report: %v", err)
	}
}

func TestLiveV2CurrentDemoteExpectationKeepsCurrentFilter(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode canonical-demotion request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"result":[
			{"id":42,"score":0.9,"payload":{"text":"ordinary","lifecycle_state":"current","canonical":false}},
			{"id":99,"score":0.7,"payload":{"text":"canonical","lifecycle_state":"current","canonical":true}}
		]}`))
	}))
	defer server.Close()

	dataset, err := Load(strings.NewReader(validV2Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts = nil
	expectation := &dataset.Queries[0].LifecycleExpectations[0]
	expectation.Decision = PresentationDemote
	expectation.ReasonCodes = []string{string(ReasonCanonicalPreference)}
	report, err := Run(context.Background(), dataset, RunOptions{Source: "live", QdrantURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	filter, filtered := requestBody["filter"].(map[string]any)
	if !filtered || filter["should"] == nil {
		t.Fatalf("current demote expectation did not preserve current-only filter: %#v", requestBody)
	}
	if len(report.Queries[0].Lifecycle.Violations) != 0 {
		t.Fatalf("demote lifecycle violations = %#v", report.Queries[0].Lifecycle.Violations)
	}
	encoded, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(encoded); err != nil {
		t.Fatalf("decode generated demote report: %v", err)
	}
}

func listEvaluationCollections(t *testing.T, qdrantURL string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(qdrantURL, "/")+"/collections",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		closeErr := response.Body.Close()
		if closeErr != nil {
			t.Fatalf("list collections returned status %d; close body: %v", response.StatusCode, closeErr)
		}
		t.Fatalf("list collections returned status %d", response.StatusCode)
	}
	var decoded struct {
		Result struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		} `json:"result"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&decoded)
	closeErr := response.Body.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	var names []string
	for _, collection := range decoded.Result.Collections {
		if strings.HasPrefix(collection.Name, "eval_") {
			names = append(names, collection.Name)
		}
	}
	sort.Strings(names)
	return names
}

func waitForEvaluationCollections(t *testing.T, qdrantURL string, want []string) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := listEvaluationCollections(t, qdrantURL)
		if fmt.Sprint(got) == fmt.Sprint(want) {
			return got
		}
		if time.Now().After(deadline) {
			return got
		}
		time.Sleep(25 * time.Millisecond)
	}
}
