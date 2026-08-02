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

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
	"github.com/Dzarlax-AI/personal-memory/internal/qdrant"
)

// RunOptions selects the evaluation source and external clients.
type RunOptions struct {
	Source    string
	QdrantURL string
	Embed     func(context.Context, string) ([]float32, error)
}

// Run validates and evaluates a dataset in fixture or read-only live mode.
func Run(ctx context.Context, dataset *Dataset, options RunOptions) (Report, error) {
	if dataset == nil {
		return Report{}, fmt.Errorf("dataset is required")
	}
	if err := dataset.ValidateForSource(options.Source); err != nil {
		return Report{}, err
	}
	if dataset.SchemaVersion == CurrentDatasetSchemaVersion {
		if dataset.Embedding.InputProfile != LegacyRawV1 {
			return Report{}, fmt.Errorf(
				"schema_version %d runner input profile %q is not supported; only %q is executable",
				CurrentDatasetSchemaVersion, dataset.Embedding.InputProfile, LegacyRawV1,
			)
		}
		if dataset.Configuration.RetrievalStrategy != RetrievalVectorOnly {
			return Report{}, fmt.Errorf(
				"schema_version %d runner retrieval strategy %q is not supported; only %q is executable",
				CurrentDatasetSchemaVersion,
				dataset.Configuration.RetrievalStrategy,
				RetrievalVectorOnly,
			)
		}
	}
	if strings.TrimSpace(options.QdrantURL) == "" {
		return Report{}, fmt.Errorf("qdrant URL is required")
	}
	switch options.Source {
	case "fixture":
		return runFixture(ctx, dataset, options.QdrantURL)
	case "live":
		return runLive(ctx, dataset, options)
	default:
		return Report{}, fmt.Errorf("source must be fixture or live")
	}
}

type collections struct {
	facts   *qdrant.Client
	chunks  *qdrant.Client
	folders *qdrant.Client
}

func runFixture(ctx context.Context, dataset *Dataset, qdrantURL string) (report Report, err error) {
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
	return executeQueries(ctx, dataset, clients, "fixture")
}

func runLive(ctx context.Context, dataset *Dataset, options RunOptions) (Report, error) {
	clients := collections{
		facts:   qdrant.NewClient(options.QdrantURL, dataset.Configuration.FactCollection),
		chunks:  qdrant.NewClient(options.QdrantURL, dataset.Configuration.ChunkCollection),
		folders: qdrant.NewClient(options.QdrantURL, dataset.Configuration.FolderCollection),
	}
	copyDataset := *dataset
	copyDataset.Queries = append([]Query(nil), dataset.Queries...)
	for i := range copyDataset.Queries {
		query := &copyDataset.Queries[i]
		if len(query.Vector) > 0 {
			continue
		}
		if options.Embed == nil {
			return Report{}, fmt.Errorf("live query %q has no vector and no embedder was configured", query.ID)
		}
		vector, err := options.Embed(ctx, query.Text)
		if err != nil {
			return Report{}, fmt.Errorf("embed live query %q: %w", query.ID, err)
		}
		if err := validateVector(vector, copyDataset.Embedding.VectorSize); err != nil {
			return Report{}, fmt.Errorf("live query %q: %w", query.ID, err)
		}
		query.Vector = vector
	}
	return executeQueries(ctx, &copyDataset, clients, "live")
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
		searchLimit := maxK
		if mode == "fixture" {
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
		switch {
		case query.Target == "facts":
			candidateLimit := max(20, maxK*4)
			if mode == "fixture" {
				candidateLimit = max(candidateLimit, len(dataset.Facts))
			}
			var filter map[string]any
			if query.EffectiveIntent() == QueryIntentCurrent {
				filter = currentLifecycleFilter()
			}
			points, err = clients.facts.Search(ctx, query.Vector, candidateLimit, filter, nil)
		case query.Mode == "flat":
			points, err = clients.chunks.Search(ctx, query.Vector, searchLimit, nil, nil)
		default:
			points, err = hierarchicalSearch(ctx, clients, query.Vector, searchLimit, dataset.Configuration)
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
				items = normalizeFactResults(points, now)
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
			items = normalizeFactResults(points, now)
		} else {
			items = normalizeResults(points)
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
	return normalizeReport(Report{
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
	}), nil
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
	threshold := cfg.FolderThreshold
	folders, err := clients.folders.Search(ctx, vector, cfg.FolderTopK, nil, &threshold)
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
		return clients.chunks.Search(ctx, vector, limit, nil, nil)
	}
	points, err := clients.chunks.Search(ctx, vector, limit, map[string]any{"should": conditions}, nil)
	if err != nil {
		return nil, err
	}
	if len(points) == 0 {
		return clients.chunks.Search(ctx, vector, limit, nil, nil)
	}
	return points, nil
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
