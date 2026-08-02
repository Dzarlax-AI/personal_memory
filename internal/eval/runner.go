package eval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/embeddingidentity"
	"github.com/Dzarlax-AI/personal-memory/internal/embeddings"
	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
	"github.com/Dzarlax-AI/personal-memory/internal/retrieval"
)

// PurposeEmbedder embeds literal inputs using the dataset's declared profile.
type PurposeEmbedder interface {
	EmbedWithPurpose(context.Context, string, embeddings.Purpose, embeddings.InputProfile, string) ([]float32, error)
	EmbedBatchWithPurpose(context.Context, []string, embeddings.Purpose, embeddings.InputProfile, string) ([][]float32, error)
}

// RunOptions selects the evaluation source and external clients.
type RunOptions struct {
	Source    string
	QdrantURL string
	Embed     func(context.Context, string) ([]float32, error)
	Embedder  PurposeEmbedder
	Now       func() time.Time
}

// Run validates and evaluates a dataset in fixture or read-only live mode.
func Run(ctx context.Context, dataset *Dataset, options RunOptions) (Report, error) {
	if dataset == nil {
		return Report{}, fmt.Errorf("dataset is required")
	}
	if err := dataset.ValidateForSource(options.Source); err != nil {
		return Report{}, err
	}
	if strings.TrimSpace(options.QdrantURL) == "" {
		return Report{}, fmt.Errorf("qdrant URL is required")
	}
	switch options.Source {
	case "fixture":
		return runFixture(ctx, dataset, options.QdrantURL)
	case "live":
		return runLive(ctx, dataset, options)
	case "tei-fixture":
		return runTEIFixture(ctx, dataset, options)
	default:
		return Report{}, fmt.Errorf("source must be fixture, live, or tei-fixture")
	}
}

type collections struct {
	facts   *qdrant.Client
	chunks  *qdrant.Client
	folders *qdrant.Client
}

func runFixture(ctx context.Context, dataset *Dataset, qdrantURL string) (report Report, err error) {
	return runFixtureTimed(ctx, dataset, qdrantURL, "fixture", nil)
}

func runFixtureTimed(ctx context.Context, dataset *Dataset, qdrantURL, mode string, timings *timingCollector) (report Report, err error) {
	suffix, err := randomSuffix()
	if err != nil {
		return Report{}, err
	}
	names := []string{
		"eval_facts_" + suffix,
		"eval_chunks_" + suffix,
		"eval_folders_" + suffix,
	}
	clients := collections{
		facts:   qdrant.NewClient(qdrantURL, names[0]),
		chunks:  qdrant.NewClient(qdrantURL, names[1]),
		folders: qdrant.NewClient(qdrantURL, names[2]),
	}
	allClients := []*qdrant.Client{clients.facts, clients.chunks, clients.folders}
	created := make([]*qdrant.Client, 0, len(allClients))
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var cleanupErrors []error
		for i := len(created) - 1; i >= 0; i-- {
			if cleanupErr := created[i].DeleteCollection(cleanupCtx, "eval_"); cleanupErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("clean up %s: %w", created[i].CollectionName(), cleanupErr))
			}
		}
		if cleanupErr := errors.Join(cleanupErrors...); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()

	metadata := map[string]any{
		"evaluation": map[string]any{
			"dataset_version": dataset.DatasetVersion,
			"temporary":       true,
		},
	}
	if dataset.SchemaVersion == CurrentDatasetSchemaVersion {
		metadata[embeddingidentity.MetadataKey] = embeddingidentity.Record{
			SchemaVersion: 1, Provider: dataset.Embedding.Provider,
			ModelID: dataset.Embedding.ModelID, ModelRevision: dataset.Embedding.ModelRevision,
			ModelDType: dataset.Embedding.DType, Pooling: dataset.Embedding.Pooling,
			VectorSize:   dataset.Embedding.VectorSize,
			InputProfile: embeddings.InputProfile(dataset.Embedding.InputProfile),
		}
	}
	for _, client := range allClients {
		if !strings.HasPrefix(client.CollectionName(), "eval_") {
			return Report{}, fmt.Errorf("temporary collection %q lacks eval_ prefix", client.CollectionName())
		}
		info, infoErr := client.CollectionInfo(ctx)
		if infoErr != nil {
			return Report{}, fmt.Errorf("preflight temporary collection %s: %w", client.CollectionName(), infoErr)
		}
		if info.Exists {
			return Report{}, fmt.Errorf("temporary collection %q already exists", client.CollectionName())
		}
		if createErr := client.CreateCollection(ctx, dataset.Embedding.VectorSize, metadata); createErr != nil {
			return Report{}, fmt.Errorf("create temporary collection %s: %w", client.CollectionName(), createErr)
		}
		created = append(created, client)
	}
	if err := upsertFixturePoints(ctx, clients.facts, dataset.Facts); err != nil {
		return Report{}, fmt.Errorf("seed facts: %w", err)
	}
	if err := upsertFixturePoints(ctx, clients.chunks, dataset.Chunks); err != nil {
		return Report{}, fmt.Errorf("seed chunks: %w", err)
	}
	if err := upsertFixturePoints(ctx, clients.folders, dataset.Folders); err != nil {
		return Report{}, fmt.Errorf("seed folders: %w", err)
	}
	return executeQueriesTimed(ctx, dataset, clients, mode, timings)
}

func runLive(ctx context.Context, dataset *Dataset, options RunOptions) (Report, error) {
	clients := collections{
		facts:   qdrant.NewClient(options.QdrantURL, dataset.Configuration.FactCollection),
		chunks:  qdrant.NewClient(options.QdrantURL, dataset.Configuration.ChunkCollection),
		folders: qdrant.NewClient(options.QdrantURL, dataset.Configuration.FolderCollection),
	}
	copyDataset := *dataset
	copyDataset.Queries = append([]Query(nil), dataset.Queries...)
	timings := newTimingCollector(options.Now)
	if dataset.SchemaVersion == CurrentDatasetSchemaVersion {
		expected := embeddingidentity.Record{
			SchemaVersion: 1, Provider: dataset.Embedding.Provider,
			ModelID: dataset.Embedding.ModelID, ModelRevision: dataset.Embedding.ModelRevision,
			ModelDType: dataset.Embedding.DType, Pooling: dataset.Embedding.Pooling,
			VectorSize:   dataset.Embedding.VectorSize,
			InputProfile: embeddings.InputProfile(dataset.Embedding.InputProfile),
		}
		for _, client := range liveCollectionsUsed(dataset, clients) {
			if err := embeddingidentity.VerifyCollection(ctx, client, expected); err != nil {
				return Report{}, err
			}
		}
	}
	for i := range copyDataset.Queries {
		query := &copyDataset.Queries[i]
		if len(query.Vector) > 0 {
			continue
		}
		start := timings.now()
		var vector []float32
		var err error
		if dataset.SchemaVersion == CurrentDatasetSchemaVersion {
			if options.Embedder == nil {
				return Report{}, fmt.Errorf("live query %q has no vector and no purpose-aware embedder was configured", query.ID)
			}
			vector, err = options.Embedder.EmbedWithPurpose(ctx, query.Text, embeddings.RetrievalQuery,
				embeddings.InputProfile(dataset.Embedding.InputProfile), dataset.Embedding.ModelID)
		} else if options.Embed != nil {
			vector, err = options.Embed(ctx, query.Text)
		} else {
			return Report{}, fmt.Errorf("live query %q has no vector and no embedder was configured", query.ID)
		}
		if err != nil {
			return Report{}, fmt.Errorf("embed live query %q: %w", query.ID, err)
		}
		timings.embed[query.ID] = elapsedUS(start, timings.now())
		if err := validateVector(vector, copyDataset.Embedding.VectorSize); err != nil {
			return Report{}, fmt.Errorf("live query %q: %w", query.ID, err)
		}
		query.Vector = vector
	}
	return executeQueriesTimed(ctx, &copyDataset, clients, "live", timings)
}

func liveCollectionsUsed(dataset *Dataset, clients collections) []*qdrant.Client {
	used := make(map[string]*qdrant.Client)
	for _, query := range dataset.Queries {
		if query.Target == "facts" {
			used[clients.facts.CollectionName()] = clients.facts
			continue
		}
		used[clients.chunks.CollectionName()] = clients.chunks
		if query.Mode == "hierarchical" {
			used[clients.folders.CollectionName()] = clients.folders
		}
	}
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]*qdrant.Client, 0, len(names))
	for _, name := range names {
		result = append(result, used[name])
	}
	return result
}

func runTEIFixture(ctx context.Context, dataset *Dataset, options RunOptions) (Report, error) {
	if options.Embedder == nil {
		return Report{}, fmt.Errorf("tei-fixture requires a purpose-aware embedder")
	}
	copyDataset := cloneDataset(dataset)
	timings := newTimingCollector(options.Now)
	start := timings.now()
	count, err := embedFixtureCorpus(ctx, &copyDataset, options.Embedder)
	if err != nil {
		return Report{}, err
	}
	corpusUS := elapsedUS(start, timings.now())
	queryTexts := make([]string, len(copyDataset.Queries))
	for i := range copyDataset.Queries {
		queryTexts[i] = copyDataset.Queries[i].Text
	}
	start = timings.now()
	queryVectors, err := options.Embedder.EmbedBatchWithPurpose(ctx, queryTexts,
		embeddings.RetrievalQuery, embeddings.InputProfile(copyDataset.Embedding.InputProfile),
		copyDataset.Embedding.ModelID)
	queryElapsed := elapsedUS(start, timings.now())
	if err != nil {
		return Report{}, fmt.Errorf("embed queries: %w", err)
	}
	if len(queryVectors) != len(copyDataset.Queries) {
		return Report{}, fmt.Errorf("embed queries: result count mismatch")
	}
	for i, vector := range queryVectors {
		if err := validateVector(vector, copyDataset.Embedding.VectorSize); err != nil {
			return Report{}, fmt.Errorf("query %q: %w", copyDataset.Queries[i].ID, err)
		}
		copyDataset.Queries[i].Vector = vector
		timings.embed[copyDataset.Queries[i].ID] = queryElapsed
	}
	report, err := runFixtureTimed(ctx, &copyDataset, options.QdrantURL, "tei-fixture", timings)
	if err == nil {
		report.Diagnostics.Corpus = &CorpusDiagnostics{EmbeddingDurationUS: corpusUS, EmbeddingCount: count}
	}
	return report, err
}

func upsertFixturePoints(ctx context.Context, client *qdrant.Client, points []FixturePoint) error {
	for _, point := range points {
		if err := client.UpsertWithPointID(ctx, qdrant.Point{
			ID:      point.ID.String(),
			Vector:  point.Vector,
			Payload: point.Payload,
		}, point.ID.IsNumeric()); err != nil {
			return fmt.Errorf("upsert point %q: %w", point.ID.String(), err)
		}
	}
	return nil
}

func executeQueries(ctx context.Context, dataset *Dataset, clients collections, mode string) (Report, error) {
	return executeQueriesTimed(ctx, dataset, clients, mode, nil)
}

func executeQueriesTimed(ctx context.Context, dataset *Dataset, clients collections, mode string, timings *timingCollector) (Report, error) {
	queries := append([]Query(nil), dataset.Queries...)
	sort.Slice(queries, func(i, j int) bool { return queries[i].ID < queries[j].ID })
	queryReports := make([]QueryReport, 0, len(queries))
	metrics := make([]QueryMetrics, 0, len(queries))
	var lifecycleReport *LifecycleReport
	var lifecycleFailures []string
	if dataset.SchemaVersion >= LifecycleSchemaVersion {
		lifecycleReport = &LifecycleReport{
			Transitions: executeTransitionScenarios(dataset.TransitionScenarios),
		}
		for _, transition := range lifecycleReport.Transitions {
			lifecycleReport.Aggregate.Checks++
			lifecycleReport.Aggregate.Violations += len(transition.Violations)
			lifecycleFailures = append(lifecycleFailures, lifecycleViolationMessages(transition.Violations)...)
		}
	}
	maxK := dataset.Configuration.TopK[len(dataset.Configuration.TopK)-1]
	now := time.Now()
	for _, query := range queries {
		queryStart := time.Now()
		if timings != nil {
			queryStart = timings.now()
		}
		searchLimit := maxK
		if mode == "fixture" || mode == "tei-fixture" {
			if query.Target == "facts" && len(dataset.Facts) > searchLimit {
				searchLimit = len(dataset.Facts)
			}
			if query.Target == "documents" && len(dataset.Chunks) > searchLimit {
				searchLimit = len(dataset.Chunks)
			}
		}
		var (
			points []qdrant.Point
			err    error
		)
		searchStart := queryStart
		if timings != nil {
			searchStart = timings.now()
		}
		switch {
		case query.Target == "facts":
			candidateLimit := max(20, maxK*4)
			if dataset.SchemaVersion == CurrentDatasetSchemaVersion &&
				dataset.Configuration.RetrievalStrategy == RetrievalHybridRRF {
				candidateLimit = dataset.Configuration.DenseCandidateLimit
			} else if mode == "fixture" || mode == "tei-fixture" {
				candidateLimit = max(candidateLimit, len(dataset.Facts))
			}
			var filter map[string]any
			if query.EffectiveIntent() == QueryIntentCurrent {
				filter = currentLifecycleFilter()
			}
			points, err = clients.facts.Search(ctx, query.Vector, candidateLimit, filter, nil)
			if err == nil {
				points, err = rerankPoints(query.Text, points, maxK, dataset.Configuration, "facts")
			}
		case query.Mode == "flat":
			candidateLimit := searchLimit
			if dataset.Configuration.RetrievalStrategy == RetrievalHybridRRF {
				candidateLimit = dataset.Configuration.DenseCandidateLimit
			}
			points, err = clients.chunks.Search(ctx, query.Vector, candidateLimit, nil, nil)
			if err == nil {
				points, err = rerankPoints(query.Text, points, searchLimit, dataset.Configuration, "chunks")
			}
		default:
			points, err = hierarchicalSearchStrategy(ctx, clients, query.Text, query.Vector, searchLimit, dataset.Configuration)
		}
		if timings != nil {
			timings.search = append(timings.search, elapsedUS(searchStart, timings.now()))
		}
		if err != nil {
			return Report{}, fmt.Errorf("execute query %q: %w", query.ID, err)
		}
		items := []RetrievedItem{}
		var queryLifecycle *QueryLifecycleReport
		if query.Target == "facts" && dataset.SchemaVersion >= LifecycleSchemaVersion {
			evidencePoints := points
			if requiresBroadLifecycleSearch(query) {
				evidencePoints, err = fetchLifecycleEvidence(ctx, clients.facts, query, points)
				if err != nil {
					return Report{}, fmt.Errorf("fetch lifecycle evidence for query %q: %w", query.ID, err)
				}
			}
			presentation := presentFactCandidates(query, evidencePoints, now)
			if query.EffectiveIntent() == QueryIntentCurrent {
				if dataset.Configuration.RetrievalStrategy == RetrievalHybridRRF {
					items = normalizeFactResultsInOrder(points, now)
				} else {
					items = normalizeFactResults(points, now)
				}
			} else {
				items = presentFactCandidates(query, points, now).results
			}
			queryLifecycle = &presentation.report
			lifecycleReport.Aggregate.Checks += presentation.canonical.Checks
			lifecycleReport.Aggregate.Violations += presentation.canonical.Violations
			lifecycleReport.Aggregate.CanonicalPreferenceChecks += presentation.canonical.CanonicalPreferenceChecks
			lifecycleReport.Aggregate.CanonicalPreferenceViolations += presentation.canonical.CanonicalPreferenceViolations
			lifecycleFailures = append(lifecycleFailures, lifecycleViolationMessages(presentation.report.Violations)...)
			lifecycleFailures = append(lifecycleFailures, canonicalPreferenceFailureMessages(
				query.ID, presentation.canonical.CanonicalPreferenceViolations,
			)...)
		} else if query.Target == "facts" {
			if dataset.Configuration.RetrievalStrategy == RetrievalHybridRRF {
				items = normalizeFactResultsInOrder(points, now)
			} else {
				items = normalizeFactResults(points, now)
			}
		} else {
			if dataset.Configuration.RetrievalStrategy == RetrievalHybridRRF {
				items = itemsFromPoints(points)
			} else {
				items = normalizeResults(points)
			}
		}
		if items == nil {
			items = []RetrievedItem{}
		}
		if len(items) > maxK {
			items = items[:maxK]
		}
		queryMetrics := ScoreQuery(query, items, dataset.Configuration.TopK)
		queryReports = append(queryReports, QueryReport{
			ID: query.ID, Target: query.Target, Mode: query.Mode, Results: items, Metrics: queryMetrics,
			Lifecycle: queryLifecycle, Cohorts: append([]QueryCohort(nil), query.Cohorts...),
		})
		metrics = append(metrics, queryMetrics)
		if timings != nil {
			timings.total = append(timings.total,
				elapsedUS(queryStart, timings.now())+timings.embed[query.ID])
		}
	}
	aggregate := Aggregate(metrics, dataset.Configuration.TopK)
	failures := EvaluateGates(aggregate, dataset.Gates)
	if dataset.Gates.ForbidLifecycleViolations {
		failures = append(failures, lifecycleFailures...)
		sort.Strings(failures)
	}
	reportSchema := dataset.SchemaVersion
	var cohortMetrics []CohortAggregateMetrics
	if dataset.SchemaVersion == CurrentDatasetSchemaVersion {
		cohortMetrics = AggregateCohorts(queryReports, dataset.Configuration.TopK)
	}
	report := normalizeReport(Report{
		SchemaVersion:  reportSchema,
		DatasetVersion: dataset.DatasetVersion,
		Mode:           mode,
		Embedding:      dataset.Embedding,
		Configuration:  dataset.Configuration,
		TopK:           dataset.Configuration.TopK,
		Aggregate:      aggregate,
		Cohorts:        cohortMetrics,
		Queries:        queryReports,
		Lifecycle:      lifecycleReport,
		GatesPassed:    len(failures) == 0,
		GateFailures:   failures,
	})
	if timings != nil {
		report.Diagnostics = timings.diagnostics()
	}
	return report, nil
}

func fetchLifecycleEvidence(
	ctx context.Context,
	client *qdrant.Client,
	query Query,
	rankingPoints []qdrant.Point,
) ([]qdrant.Point, error) {
	evidence := append([]qdrant.Point(nil), rankingPoints...)
	seen := make(map[string]struct{}, len(rankingPoints)+len(query.LifecycleExpectations))
	for _, point := range rankingPoints {
		seen[point.ID] = struct{}{}
	}
	ids := make([]string, 0, len(query.LifecycleExpectations))
	for _, expectation := range query.LifecycleExpectations {
		if _, exists := seen[expectation.ID]; exists {
			continue
		}
		seen[expectation.ID] = struct{}{}
		ids = append(ids, expectation.ID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		point, exists, err := client.Get(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("get candidate %s: %w", id, err)
		}
		if exists {
			evidence = append(evidence, point)
		}
	}
	return evidence, nil
}

func requiresBroadLifecycleSearch(query Query) bool {
	if query.EffectiveIntent() != QueryIntentCurrent {
		return true
	}
	for _, expectation := range query.LifecycleExpectations {
		if expectation.Decision == PresentationSuppress ||
			expectation.Decision == PresentationUncertain {
			return true
		}
		if expectation.State != "" && expectation.State != lifecycle.Current {
			return true
		}
	}
	return false
}

func lifecycleViolationMessages(violations []LifecycleViolation) []string {
	messages := make([]string, len(violations))
	for i, violation := range violations {
		messages[i] = violation.message()
	}
	return messages
}

func canonicalPreferenceFailureMessages(queryID string, violations int) []string {
	if violations == 0 {
		return nil
	}
	return []string{fmt.Sprintf("query %s invariant %s", queryID, ReasonCanonicalPreference)}
}

func currentLifecycleFilter() map[string]any {
	return map[string]any{"should": []map[string]any{
		{"key": "lifecycle_state", "match": map[string]any{"value": "current"}},
		{"is_empty": map[string]any{"key": "lifecycle_state"}},
	}}
}

func hierarchicalSearch(ctx context.Context, clients collections, vector []float32, limit int, cfg Configuration) ([]qdrant.Point, error) {
	return hierarchicalSearchStrategy(ctx, clients, "", vector, limit, cfg)
}

func hierarchicalSearchStrategy(ctx context.Context, clients collections, rawQuery string, vector []float32, limit int, cfg Configuration) ([]qdrant.Point, error) {
	threshold := cfg.FolderThreshold
	folderLimit := cfg.FolderTopK
	if cfg.RetrievalStrategy == RetrievalHybridRRF {
		folderLimit = cfg.DenseCandidateLimit
	}
	folders, err := clients.folders.Search(ctx, vector, folderLimit, nil, &threshold)
	if err != nil {
		return nil, err
	}
	folders, err = rerankPoints(rawQuery, folders, cfg.FolderTopK, cfg, "folders")
	if err != nil {
		return nil, err
	}
	conditions := make([]map[string]any, 0, len(folders))
	for _, folder := range folders {
		if path, ok := folder.Payload["folder_path"].(string); ok && path != "" {
			conditions = append(conditions, map[string]any{
				"key": "folder_path", "match": map[string]any{"value": path},
			})
		}
	}
	if len(conditions) == 0 {
		return searchAndRerankChunks(ctx, clients.chunks, rawQuery, vector, limit, cfg, nil)
	}
	points, err := searchAndRerankChunks(ctx, clients.chunks, rawQuery, vector, limit, cfg,
		map[string]any{"should": conditions})
	if err != nil {
		return nil, err
	}
	if len(points) == 0 {
		return searchAndRerankChunks(ctx, clients.chunks, rawQuery, vector, limit, cfg, nil)
	}
	return points, nil
}

func searchAndRerankChunks(ctx context.Context, client *qdrant.Client, rawQuery string,
	vector []float32, limit int, cfg Configuration, filter map[string]any) ([]qdrant.Point, error) {
	candidateLimit := limit
	if cfg.RetrievalStrategy == RetrievalHybridRRF {
		candidateLimit = cfg.DenseCandidateLimit
	}
	points, err := client.Search(ctx, vector, candidateLimit, filter, nil)
	if err != nil {
		return nil, err
	}
	return rerankPoints(rawQuery, points, limit, cfg, "chunks")
}

func rerankPoints(rawQuery string, points []qdrant.Point, limit int, cfg Configuration, kind string) ([]qdrant.Point, error) {
	if cfg.RetrievalStrategy != RetrievalHybridRRF || len(points) == 0 {
		return points, nil
	}
	byID := make(map[string]qdrant.Point, len(points))
	candidates := make([]retrieval.Candidate, 0, len(points))
	for _, point := range points {
		fields := lexicalFields(point.Payload, kind)
		if len(fields) == 0 {
			return nil, fmt.Errorf("%s candidate %q has no usable lexical fields", kind, point.ID)
		}
		byID[point.ID] = point
		candidates = append(candidates, retrieval.Candidate{
			ID: point.ID, DenseScore: point.Score, Fields: fields,
		})
	}
	ranked, err := retrieval.Rank(rawQuery, candidates, retrieval.Options{
		RRFConstant: cfg.RRFConstant, Limit: min(limit, retrieval.MaxResults),
	})
	if err != nil {
		return nil, fmt.Errorf("hybrid rank %s candidates: %w", kind, err)
	}
	result := make([]qdrant.Point, len(ranked))
	for i := range ranked {
		result[i] = byID[ranked[i].Candidate.ID]
	}
	return result, nil
}

func lexicalFields(payload map[string]any, kind string) []retrieval.Field {
	names := []string{"text"}
	switch kind {
	case "chunks":
		names = append(names, "heading", "file_path")
	case "folders":
		names = append(names, "summary", "folder_path")
	}
	values := make([]struct{ name, value string }, 0, len(names))
	for _, name := range names {
		value, ok := payload[name].(string)
		if ok && strings.TrimSpace(value) != "" {
			values = append(values, struct{ name, value string }{name, value})
		}
	}
	if len(values) == 0 {
		return nil
	}
	// retrieval requires one canonical text field. If a legacy folder has only
	// summary/path, promote the first valid value without exposing it.
	fields := []retrieval.Field{{Name: "text", Value: values[0].value}}
	for _, value := range values {
		if value.name != "text" {
			fields = append(fields, retrieval.Field{Name: value.name, Value: value.value})
		}
	}
	return fields
}

func normalizeResults(points []qdrant.Point) []RetrievedItem {
	sort.SliceStable(points, func(i, j int) bool {
		if points[i].Score == points[j].Score {
			return points[i].ID < points[j].ID
		}
		return points[i].Score > points[j].Score
	})
	return itemsFromPoints(points)
}

func itemsFromPoints(points []qdrant.Point) []RetrievedItem {
	items := make([]RetrievedItem, len(points))
	for i, point := range points {
		text, _ := point.Payload["text"].(string)
		items[i] = RetrievedItem{
			ID: point.ID, Score: point.Score, MissingText: strings.TrimSpace(text) == "",
		}
	}
	return items
}

func normalizeFactResults(points []qdrant.Point, now time.Time) []RetrievedItem {
	byID := make(map[string]qdrant.Point, len(points))
	candidates := make([]lifecycle.Candidate, 0, len(points))
	for _, point := range points {
		view, _ := lifecycle.Parse(point.Payload, point.ID)
		if !lifecycle.IsCurrentTruth(view, factExpiredAt(point.Payload, now)) {
			continue
		}
		byID[point.ID] = point
		candidates = append(candidates, lifecycle.Candidate{PointID: point.ID, Score: point.Score, View: view})
	}
	lifecycle.SortCandidates(candidates)
	sorted := make([]qdrant.Point, 0, len(candidates))
	for _, candidate := range candidates {
		sorted = append(sorted, byID[candidate.PointID])
	}
	return itemsFromPoints(sorted)
}

func normalizeFactResultsInOrder(points []qdrant.Point, now time.Time) []RetrievedItem {
	filtered := make([]qdrant.Point, 0, len(points))
	for _, point := range points {
		view, _ := lifecycle.Parse(point.Payload, point.ID)
		if lifecycle.IsCurrentTruth(view, factExpiredAt(point.Payload, now)) {
			filtered = append(filtered, point)
		}
	}
	return itemsFromPoints(filtered)
}

func factExpiredAt(payload map[string]any, now time.Time) bool {
	raw, exists := payload["valid_until"]
	if !exists || raw == nil {
		return false
	}
	value, ok := raw.(string)
	if !ok || value == "" {
		return false
	}
	expiry, err := time.Parse("2006-01-02", value)
	return err == nil && now.After(expiry)
}

func randomSuffix() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate temporary collection suffix: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

type timingCollector struct {
	now    func() time.Time
	embed  map[string]int64
	total  []int64
	search []int64
}

func newTimingCollector(now func() time.Time) *timingCollector {
	if now == nil {
		now = time.Now
	}
	return &timingCollector{now: now, embed: make(map[string]int64)}
}

func elapsedUS(start, end time.Time) int64 {
	value := end.Sub(start).Microseconds()
	if value < 0 {
		return 0
	}
	return value
}

func summarizeDurations(values []int64) DurationSummary {
	if len(values) == 0 {
		return DurationSummary{}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	percentile := func(p int) int64 {
		index := (p*len(sorted) + 99) / 100
		if index < 1 {
			index = 1
		}
		return sorted[index-1]
	}
	return DurationSummary{
		Count: len(sorted), Min: sorted[0], P50: percentile(50),
		P95: percentile(95), Max: sorted[len(sorted)-1],
	}
}

func (collector *timingCollector) diagnostics() *Diagnostics {
	embeds := make([]int64, 0, len(collector.embed))
	for _, value := range collector.embed {
		embeds = append(embeds, value)
	}
	return &Diagnostics{Query: QueryDiagnostics{
		Total: summarizeDurations(collector.total), Embed: summarizeDurations(embeds),
		Search: summarizeDurations(collector.search),
	}}
}

func cloneDataset(source *Dataset) Dataset {
	cloned := *source
	cloned.Configuration.TopK = append([]int(nil), source.Configuration.TopK...)
	cloned.Configuration.present = clonePresence(source.Configuration.present)
	cloned.Gates.MinimumHitAt = cloneStringMetricMap(source.Gates.MinimumHitAt)
	cloned.Gates.MinimumNDCGAt = cloneStringMetricMap(source.Gates.MinimumNDCGAt)
	cloned.Facts = cloneFixturePoints(source.Facts)
	cloned.Chunks = cloneFixturePoints(source.Chunks)
	cloned.Folders = cloneFixturePoints(source.Folders)
	cloned.Queries = append([]Query(nil), source.Queries...)
	for i := range cloned.Queries {
		cloned.Queries[i].Vector = append(Vector(nil), source.Queries[i].Vector...)
		cloned.Queries[i].Expected = append([]ExpectedItem(nil), source.Queries[i].Expected...)
		cloned.Queries[i].ForbiddenIDs = append([]string(nil), source.Queries[i].ForbiddenIDs...)
		cloned.Queries[i].Cohorts = append([]QueryCohort(nil), source.Queries[i].Cohorts...)
		cloned.Queries[i].LifecycleExpectations =
			append([]LifecycleExpectation(nil), source.Queries[i].LifecycleExpectations...)
	}
	return cloned
}

func cloneStringMetricMap(source map[string]float64) map[string]float64 {
	if source == nil {
		return nil
	}
	cloned := make(map[string]float64, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneFixturePoints(points []FixturePoint) []FixturePoint {
	cloned := make([]FixturePoint, len(points))
	for i := range points {
		cloned[i] = points[i]
		cloned[i].Vector = append(Vector(nil), points[i].Vector...)
		cloned[i].Payload = clonePayload(points[i].Payload)
	}
	return cloned
}

func clonePayload(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		switch typed := value.(type) {
		case map[string]any:
			cloned[key] = clonePayload(typed)
		case []string:
			cloned[key] = append([]string(nil), typed...)
		case []any:
			items := make([]any, len(typed))
			copy(items, typed)
			cloned[key] = items
		default:
			cloned[key] = value
		}
	}
	return cloned
}

func embedFixtureCorpus(ctx context.Context, dataset *Dataset, embedder PurposeEmbedder) (int, error) {
	total := 0
	groups := []struct {
		name    string
		points  []FixturePoint
		purpose embeddings.Purpose
	}{
		{"facts", dataset.Facts, embeddings.FactPassage},
		{"chunks", dataset.Chunks, embeddings.ChunkPassage},
		{"folders", dataset.Folders, embeddings.FolderPassage},
	}
	for _, group := range groups {
		texts := make([]string, len(group.points))
		for i := range group.points {
			text, ok := corpusText(group.points[i].Payload, group.name)
			if !ok {
				return 0, fmt.Errorf("%s point %q has no usable corpus text", group.name, group.points[i].ID.String())
			}
			texts[i] = text
		}
		vectors, err := embedder.EmbedBatchWithPurpose(ctx, texts, group.purpose,
			embeddings.InputProfile(dataset.Embedding.InputProfile), dataset.Embedding.ModelID)
		if err != nil {
			return 0, fmt.Errorf("embed %s: %w", group.name, err)
		}
		if len(vectors) != len(group.points) {
			return 0, fmt.Errorf("embed %s: result count mismatch", group.name)
		}
		for i := range vectors {
			if err := validateVector(vectors[i], dataset.Embedding.VectorSize); err != nil {
				return 0, fmt.Errorf("%s point %q: %w", group.name, group.points[i].ID.String(), err)
			}
			group.points[i].Vector = append(Vector(nil), vectors[i]...)
		}
		total += len(vectors)
	}
	return total, nil
}

func corpusText(payload map[string]any, group string) (string, bool) {
	names := []string{"text"}
	if group == "folders" {
		names = append(names, "summary")
	}
	for _, name := range names {
		value, ok := payload[name].(string)
		if ok && strings.TrimSpace(value) != "" {
			return value, true
		}
	}
	return "", false
}
