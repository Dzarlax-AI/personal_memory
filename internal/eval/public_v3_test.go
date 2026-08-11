package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const (
	publicV3DatasetSHA256           = "717f3d5893c4fe400161d6e152acb1aa921fab279080e185008451ab28931b96"
	publicV3BaselineSHA256          = "7a461d7273fa4bb074b049b427607a7f65ec8f02b43859f66f356d1902d6c2f4"
	publicV3HybridCandidateSHA256   = "58819e8dbf0df0daa0b380ece30df7de8b00e8e60f0fa881337cf7e35493c2e9"
	publicV3FailingComparisonSHA256 = "4449eb08f6676fb47ddef7111ae19166b7f1e0fa8311d65d4203a3f4604ceadd"
	publicV3DatasetVersion          = "3.1.0"
	publicV3ModelRevision           = "614241f622f53c4eeff9890bdc4f31cfecc418b3"
	publicV3HybridCandidateName     = "public-v3-legacy-raw-hybrid-rrf60-candidate"
	publicV3DenseCandidateLimit     = 40
	publicV3RRFConstant             = 60
)

var publicV3TopK = []int{1, 3, 5, 20}

func TestPublicV3DatasetPinnedContract(t *testing.T) {
	datasetPath, datasetData, dataset := loadPublicDataset(t, "v3")
	assertCanonicalDataset(t, datasetPath, datasetData, dataset)
	if dataset.SchemaVersion != CurrentDatasetSchemaVersion ||
		dataset.DatasetVersion != publicV3DatasetVersion {
		t.Fatalf("dataset identity = %d/%q", dataset.SchemaVersion, dataset.DatasetVersion)
	}
	assertPublicV3Embedding(t, dataset.Embedding)
	assertPublicV3Configuration(t, dataset.Configuration)
	assertPublicV3Gates(t, dataset.Gates)
	if len(dataset.Facts) != 48 || len(dataset.Chunks) != 41 ||
		len(dataset.Folders) != 41 || len(dataset.Queries) != 21 ||
		len(dataset.TransitionScenarios) != 20 {
		t.Fatalf(
			"dataset counts facts/chunks/folders/queries/transitions = %d/%d/%d/%d/%d",
			len(dataset.Facts), len(dataset.Chunks), len(dataset.Folders),
			len(dataset.Queries), len(dataset.TransitionScenarios),
		)
	}
	if err := dataset.ValidateForSource("fixture"); err != nil {
		t.Fatal(err)
	}
	if got := currentOrLegacyFactCount(dataset.Facts); got != 41 {
		t.Fatalf("current/legacy fact candidate count = %d, want 41", got)
	}
	// These synthetic bounds ensure the evaluator encounters pools above the
	// candidate limit of 40 for facts, flat chunks, and folders. They exercise
	// the 40/60 choice on this bounded fixture only; they do not establish a
	// universal optimum.
	if currentOrLegacyFactCount(dataset.Facts) <= publicV3DenseCandidateLimit ||
		len(dataset.Chunks) <= publicV3DenseCandidateLimit ||
		len(dataset.Folders) <= publicV3DenseCandidateLimit {
		t.Fatal("public v3 corpus does not activate every 40-candidate bound")
	}
	assertIntentionalFolderOnlyCalibrationCandidates(t, dataset)

	wantCohorts := map[QueryCohort][]string{
		CohortExactName: {
			"exact-name-document",
			"exact-name-fact",
		},
		CohortGeneralSemantic: {
			"document-flat",
			"document-hierarchical",
			"document-hierarchical-fallback",
			"document-materialized-record",
			"exact-name-document",
			"fact-ambiguous-en",
			"fact-legacy-numeric-en",
			"fact-multilingual",
		},
		CohortIdentifierPath: {
			"identifier-adr-path-document",
			"identifier-cluster-document",
			"identifier-uuid-fact",
		},
		CohortLifecycle: {
			"fact-legacy-numeric-en",
			"lifecycle-as-of-expiry",
			"lifecycle-canonical-preference",
			"lifecycle-current-superseded",
			"lifecycle-expired-canonical",
			"lifecycle-history",
			"lifecycle-legacy-invalid",
			"lifecycle-permanent-historical",
			"lifecycle-uncertainty",
		},
		CohortMultilingual: {
			"fact-multilingual",
			"fact-russian",
		},
	}
	if got := cohortQueryIDs(dataset.Queries); !reflect.DeepEqual(got, wantCohorts) {
		t.Fatalf("cohort membership = %#v, want %#v", got, wantCohorts)
	}

	newExpected := map[string][]ExpectedItem{
		"exact-name-document": {
			{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa6", Grade: 3},
		},
		"exact-name-fact": {
			{ID: "61000000-0000-4000-8000-000000000001", Grade: 3},
		},
		"identifier-adr-path-document": {
			{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa10", Grade: 3},
		},
		"identifier-cluster-document": {
			{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa8", Grade: 3},
		},
		"identifier-uuid-fact": {
			{ID: "61000000-0000-4000-8000-000000000003", Grade: 3},
		},
	}
	queries := queriesByID(dataset.Queries)
	for id, wantExpected := range newExpected {
		query, exists := queries[id]
		if !exists {
			t.Fatalf("new query %q is missing", id)
		}
		if !reflect.DeepEqual(query.Expected, wantExpected) {
			t.Fatalf("new query %q expected = %#v, want %#v", id, query.Expected, wantExpected)
		}
		assertQueryReferencesExist(t, query, dataset)
	}
	for _, query := range dataset.Queries {
		for _, reference := range appendExpectedAndForbiddenIDs(query) {
			if strings.HasPrefix(reference, "62000000-") ||
				strings.HasPrefix(reference, "b1000000-") ||
				strings.HasPrefix(reference, "c1000000-") {
				t.Fatalf("query %q labels calibration point %q as relevant or forbidden", query.ID, reference)
			}
		}
	}
	assertRawSHA256(t, datasetPath, datasetData, publicV3DatasetSHA256)
}

func assertIntentionalFolderOnlyCalibrationCandidates(t *testing.T, dataset *Dataset) {
	t.Helper()
	chunkPaths := make(map[string]struct{}, len(dataset.Chunks))
	for _, chunk := range dataset.Chunks {
		if path, ok := chunk.Payload["folder_path"].(string); ok {
			chunkPaths[path] = struct{}{}
		}
	}
	var folderOnly []string
	for _, folder := range dataset.Folders {
		path, ok := folder.Payload["folder_path"].(string)
		if !ok {
			continue
		}
		if _, exists := chunkPaths[path]; !exists {
			folderOnly = append(folderOnly, path)
		}
	}
	sort.Strings(folderOnly)
	want := []string{
		"calibration/set-000000000031/terrariums",
		"calibration/set-000000000032/calligraphy",
		"calibration/set-000000000033/sailboats",
	}
	if !reflect.DeepEqual(folderOnly, want) {
		t.Fatalf("folder-only calibration candidates = %v, want intentional decoys %v", folderOnly, want)
	}
}

func TestPublicV3CarriesPublicV2Contracts(t *testing.T) {
	_, _, v2 := loadPublicDataset(t, "v2")
	_, _, v3 := loadPublicDataset(t, "v3")

	v3Facts := pointsByID(v3.Facts)
	if len(v2.Facts) != 22 {
		t.Fatalf("public v2 fact count = %d, want 22", len(v2.Facts))
	}
	for _, want := range v2.Facts {
		got, exists := v3Facts[want.ID.String()]
		if !exists || !reflect.DeepEqual(got.Payload, want.Payload) {
			t.Fatalf("carried fact %q changed or is missing", want.ID.String())
		}
	}
	wantExtraFactIDs := []string{
		"61000000-0000-4000-8000-000000000001",
		"61000000-0000-4000-8000-000000000002",
		"61000000-0000-4000-8000-000000000003",
		"61000000-0000-4000-8000-000000000004",
	}
	wantExtraFactIDs = append(wantExtraFactIDs,
		sequentialPointIDs("62000000-0000-4000-8000-", 22)...)
	assertExtraPointIDs(t, "facts", v2.Facts, v3.Facts, wantExtraFactIDs)

	const materializedText = "An intentionally malformed synthetic chunk is retained as a public fixture record."
	v3Chunks := pointsByID(v3.Chunks)
	for _, want := range v2.Chunks {
		got, exists := v3Chunks[want.ID.String()]
		if !exists {
			t.Fatalf("carried chunk %q is missing", want.ID.String())
		}
		wantPayload := want.Payload
		if want.ID.String() == "dddddddd-dddd-4ddd-8ddd-ddddddddddd4" {
			var err error
			wantPayload, err = clonePayload(want.Payload)
			if err != nil {
				t.Fatal(err)
			}
			wantPayload["text"] = materializedText
		}
		if !reflect.DeepEqual(got.Payload, wantPayload) {
			t.Fatalf("carried chunk %q changed outside the pinned TEI text exception", want.ID.String())
		}
	}
	wantExtraChunkIDs := []string{
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa6",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa7",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa8",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa9",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa10",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa11",
	}
	wantExtraChunkIDs = append(wantExtraChunkIDs,
		sequentialPointIDs("b1000000-0000-4000-8000-", 30)...)
	assertExtraPointIDs(t, "chunks", v2.Chunks, v3.Chunks, wantExtraChunkIDs)

	v3Folders := pointsByID(v3.Folders)
	for _, want := range v2.Folders {
		got, exists := v3Folders[want.ID.String()]
		if !exists || !reflect.DeepEqual(got.Payload, want.Payload) {
			t.Fatalf("carried folder %q changed or is missing", want.ID.String())
		}
	}
	wantExtraFolderIDs := []string{
		"f4444444-4444-4444-8444-444444444444",
		"f5555555-5555-4555-8555-555555555555",
		"f6666666-6666-4666-8666-666666666666",
		"f7777777-7777-4777-8777-777777777777",
		"f8888888-8888-4888-8888-888888888888",
	}
	wantExtraFolderIDs = append(wantExtraFolderIDs,
		sequentialPointIDs("c1000000-0000-4000-8000-", 33)...)
	assertExtraPointIDs(t, "folders", v2.Folders, v3.Folders, wantExtraFolderIDs)

	if len(v2.Queries) != 16 {
		t.Fatalf("public v2 query count = %d, want 16", len(v2.Queries))
	}
	v3Queries := queriesByID(v3.Queries)
	for _, want := range v2.Queries {
		v3ID := want.ID
		if want.ID == "document-missing-text" {
			v3ID = "document-materialized-record"
		}
		got, exists := v3Queries[v3ID]
		if !exists {
			t.Fatalf("carried query contract %q is missing", want.ID)
		}
		if want.ID == "document-missing-text" {
			if got.Text != "Find the retained synthetic fixture record." {
				t.Fatalf("TEI-materialized query text = %q", got.Text)
			}
			got.ID = want.ID
			got.Text = want.Text
		}
		if !reflect.DeepEqual(carriedQueryContract(got), carriedQueryContract(want)) {
			t.Fatalf("carried query contract %q changed or is missing", want.ID)
		}
	}
	assertExactExtraQueryIDs(t, v2.Queries, v3.Queries, []string{
		"document-materialized-record",
		"exact-name-document",
		"exact-name-fact",
		"identifier-adr-path-document",
		"identifier-cluster-document",
		"identifier-uuid-fact",
	})

	if len(v2.TransitionScenarios) != 20 {
		t.Fatalf("public v2 transition count = %d, want 20", len(v2.TransitionScenarios))
	}
	v3Transitions := transitionsByID(v3.TransitionScenarios)
	for _, want := range v2.TransitionScenarios {
		got, exists := v3Transitions[want.ID]
		if !exists || !reflect.DeepEqual(got, want) {
			t.Fatalf("carried transition scenario %q changed or is missing", want.ID)
		}
	}
}

func TestPublicV3BaselinePinnedContract(t *testing.T) {
	path, data, baseline := loadPublicReport(t, "baseline.json")
	assertCanonicalReport(t, path, data, baseline)
	if baseline.SchemaVersion != CurrentReportSchemaVersion ||
		baseline.DatasetVersion != publicV3DatasetVersion ||
		baseline.Mode != "fixture" {
		t.Fatalf("baseline identity = %d/%q/%q",
			baseline.SchemaVersion, baseline.DatasetVersion, baseline.Mode)
	}
	assertPublicV3Embedding(t, baseline.Embedding)
	assertPublicV3Configuration(t, baseline.Configuration)
	if !reflect.DeepEqual(baseline.TopK, publicV3TopK) {
		t.Fatalf("baseline top_k = %v, want %v", baseline.TopK, publicV3TopK)
	}
	if !baseline.GatesPassed || len(baseline.GateFailures) != 0 ||
		baseline.Aggregate.InvariantViolations != 0 ||
		baseline.Lifecycle == nil ||
		baseline.Lifecycle.Aggregate.Checks != 35 ||
		baseline.Lifecycle.Aggregate.Violations != 0 ||
		baseline.Lifecycle.Aggregate.CanonicalPreferenceChecks != 22 ||
		baseline.Lifecycle.Aggregate.CanonicalPreferenceViolations != 0 {
		t.Fatalf("baseline gate/lifecycle contract changed")
	}
	wantQueryIDs := []string{
		"document-flat",
		"document-hierarchical",
		"document-hierarchical-fallback",
		"document-materialized-record",
		"exact-name-document",
		"exact-name-fact",
		"fact-ambiguous-en",
		"fact-legacy-numeric-en",
		"fact-multilingual",
		"fact-russian",
		"identifier-adr-path-document",
		"identifier-cluster-document",
		"identifier-uuid-fact",
		"lifecycle-as-of-expiry",
		"lifecycle-canonical-preference",
		"lifecycle-current-superseded",
		"lifecycle-expired-canonical",
		"lifecycle-history",
		"lifecycle-legacy-invalid",
		"lifecycle-permanent-historical",
		"lifecycle-uncertainty",
	}
	gotQueryIDs := make([]string, len(baseline.Queries))
	for i := range baseline.Queries {
		gotQueryIDs[i] = baseline.Queries[i].ID
	}
	if !reflect.DeepEqual(gotQueryIDs, wantQueryIDs) {
		t.Fatalf("baseline query IDs = %v, want %v", gotQueryIDs, wantQueryIDs)
	}
	assertRawSHA256(t, path, data, publicV3BaselineSHA256)
}

func TestPublicV3HybridCandidateAndFailingComparisonPinnedContract(t *testing.T) {
	baselinePath, _, baseline := loadPublicReport(t, "baseline.json")
	candidatePath, candidateData, candidate := loadPublicReport(
		t, "hybrid-rrf60-candidate.json",
	)
	assertCanonicalReport(t, candidatePath, candidateData, candidate)
	if candidate.SchemaVersion != CurrentReportSchemaVersion ||
		candidate.DatasetVersion != publicV3DatasetVersion ||
		candidate.Mode != "fixture" {
		t.Fatalf("candidate identity = %d/%q/%q",
			candidate.SchemaVersion, candidate.DatasetVersion, candidate.Mode)
	}
	assertPublicV3Embedding(t, candidate.Embedding)
	assertPublicV3HybridConfiguration(t, candidate.Configuration)
	if !reflect.DeepEqual(candidate.TopK, publicV3TopK) ||
		!candidate.GatesPassed || len(candidate.GateFailures) != 0 ||
		candidate.Aggregate.InvariantViolations != 0 ||
		candidate.Lifecycle == nil ||
		candidate.Lifecycle.Aggregate.Violations != 0 {
		t.Fatal("hybrid candidate query/gate contract changed")
	}
	maxK := publicV3TopK[len(publicV3TopK)-1]
	for _, query := range candidate.Queries {
		if len(query.Results) > maxK {
			t.Fatalf("candidate query %q returned %d results, max top_k is %d",
				query.ID, len(query.Results), maxK)
		}
	}
	assertRawSHA256(t, candidatePath, candidateData, publicV3HybridCandidateSHA256)

	comparisonPath := filepath.Join(
		"..", "..", "evaldata", "public", "v3",
		"hybrid-rrf60-failing-comparison.json",
	)
	comparisonData, err := os.ReadFile(comparisonPath)
	if err != nil {
		t.Fatalf("read %s: %v", comparisonPath, err)
	}
	comparison := decodeComparison(t, comparisonPath, comparisonData)
	recomputed, err := Compare(baseline, candidate, true)
	if err != nil {
		t.Fatalf("compare %s and %s: %v", baselinePath, candidatePath, err)
	}
	recomputedData, err := RenderComparisonJSON(recomputed)
	if err != nil {
		t.Fatalf("render recomputed comparison: %v", err)
	}
	if !bytes.Equal(comparisonData, recomputedData) {
		t.Fatalf("%s is not the canonical offline comparison", comparisonPath)
	}
	if comparison.GatesPassed ||
		!reflect.DeepEqual(comparison.GateFailures,
			[]string{"protected cohorts require a ranking improvement"}) {
		t.Fatalf("%s must preserve explicit no-winner evidence", comparisonPath)
	}
	assertRawSHA256(t, comparisonPath, comparisonData, publicV3FailingComparisonSHA256)
}

func loadPublicDataset(t *testing.T, version string) (string, []byte, *Dataset) {
	t.Helper()
	path := filepath.Join("..", "..", "evaldata", "public", version, "dataset.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	dataset, err := Load(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return path, data, dataset
}

func loadPublicReport(t *testing.T, name string) (string, []byte, Report) {
	t.Helper()
	path := filepath.Join("..", "..", "evaldata", "public", "v3", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	report, err := DecodeReport(data)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return path, data, report
}

func assertCanonicalDataset(t *testing.T, path string, data []byte, dataset *Dataset) {
	t.Helper()
	rendered, err := RenderDatasetJSON(dataset)
	if err != nil {
		t.Fatalf("render %s: %v", path, err)
	}
	if !bytes.Equal(data, rendered) {
		t.Fatalf("%s is not canonical RenderDatasetJSON output", path)
	}
}

func assertCanonicalReport(t *testing.T, path string, data []byte, report Report) {
	t.Helper()
	rendered, err := RenderJSON(report)
	if err != nil {
		t.Fatalf("render %s: %v", path, err)
	}
	if !bytes.Equal(data, rendered) {
		t.Fatalf("%s is not canonical RenderJSON output", path)
	}
}

func decodeComparison(t *testing.T, path string, data []byte) Comparison {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var comparison Comparison
	if err := decoder.Decode(&comparison); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("decode trailing data in %s: %v", path, err)
	}
	return comparison
}

func assertRawSHA256(t *testing.T, label string, data []byte, want string) {
	t.Helper()
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("%s SHA-256 = %s, want %s", label, got, want)
	}
}

func assertPublicV3Embedding(t *testing.T, got EmbeddingIdentity) {
	t.Helper()
	if got.Provider != "tei" ||
		got.ModelID != "intfloat/multilingual-e5-small" ||
		got.ModelRevision != publicV3ModelRevision ||
		got.DType != "float32" ||
		got.Pooling != "mean" ||
		got.VectorSize != 384 ||
		got.InputProfile != LegacyRawV1 {
		t.Fatalf("public v3 embedding identity changed: %#v", got)
	}
}

func assertPublicV3Configuration(t *testing.T, got Configuration) {
	t.Helper()
	if got.Name != "public-v3-legacy-raw-vector-only" ||
		got.FactCollection != "memory" ||
		got.ChunkCollection != "doc_chunks" ||
		got.FolderCollection != "doc_folders" ||
		got.FolderTopK != 3 ||
		got.FolderThreshold != 0.5 ||
		!reflect.DeepEqual(got.TopK, publicV3TopK) ||
		got.RetrievalStrategy != RetrievalVectorOnly ||
		got.DenseCandidateLimit != 0 ||
		got.RRFConstant != 0 {
		t.Fatalf("public v3 retrieval configuration changed: %#v", got)
	}
}

func assertPublicV3HybridConfiguration(t *testing.T, got Configuration) {
	t.Helper()
	if got.Name != publicV3HybridCandidateName ||
		got.FactCollection != "memory" ||
		got.ChunkCollection != "doc_chunks" ||
		got.FolderCollection != "doc_folders" ||
		got.FolderTopK != 3 ||
		got.FolderThreshold != 0.5 ||
		!reflect.DeepEqual(got.TopK, publicV3TopK) ||
		got.RetrievalStrategy != RetrievalHybridRRF ||
		got.DenseCandidateLimit != publicV3DenseCandidateLimit ||
		got.RRFConstant != publicV3RRFConstant {
		t.Fatalf("public v3 hybrid candidate configuration changed: %#v", got)
	}
}

func assertPublicV3Gates(t *testing.T, got Gates) {
	t.Helper()
	wantMRR := 0.25
	if !got.ForbidInvariantViolations ||
		!got.ForbidLifecycleViolations ||
		!reflect.DeepEqual(got.MinimumHitAt, map[string]float64{"20": 0.8}) ||
		got.MinimumMRR == nil || *got.MinimumMRR != wantMRR ||
		!reflect.DeepEqual(got.MinimumNDCGAt, map[string]float64{"20": 0.45}) {
		t.Fatalf("public v3 dataset gates changed: %#v", got)
	}
}

func pointsByID(points []FixturePoint) map[string]FixturePoint {
	result := make(map[string]FixturePoint, len(points))
	for _, point := range points {
		result[point.ID.String()] = point
	}
	return result
}

func queriesByID(queries []Query) map[string]Query {
	result := make(map[string]Query, len(queries))
	for _, query := range queries {
		result[query.ID] = query
	}
	return result
}

func transitionsByID(scenarios []TransitionScenario) map[string]TransitionScenario {
	result := make(map[string]TransitionScenario, len(scenarios))
	for _, scenario := range scenarios {
		result[scenario.ID] = scenario
	}
	return result
}

func cohortQueryIDs(queries []Query) map[QueryCohort][]string {
	result := make(map[QueryCohort][]string)
	for _, query := range queries {
		for _, cohort := range query.Cohorts {
			result[cohort] = append(result[cohort], query.ID)
		}
	}
	for cohort := range result {
		sort.Strings(result[cohort])
	}
	return result
}

func currentOrLegacyFactCount(points []FixturePoint) int {
	count := 0
	for _, point := range points {
		state, exists := point.Payload["lifecycle_state"]
		if !exists || state == string("current") {
			count++
		}
	}
	return count
}

func appendExpectedAndForbiddenIDs(query Query) []string {
	result := make([]string, 0, len(query.Expected)+len(query.ForbiddenIDs))
	for _, expected := range query.Expected {
		result = append(result, expected.ID)
	}
	return append(result, query.ForbiddenIDs...)
}

func carriedQueryContract(query Query) Query {
	query.Vector = nil
	query.Cohorts = nil
	query.cohortsPresent = false
	return query
}

func sequentialPointIDs(prefix string, count int) []string {
	result := make([]string, count)
	for i := range result {
		result[i] = fmt.Sprintf("%s%012d", prefix, i+1)
	}
	return result
}

func assertExtraPointIDs(
	t *testing.T,
	label string,
	oldPoints, newPoints []FixturePoint,
	want []string,
) {
	t.Helper()
	oldIDs := make(map[string]struct{}, len(oldPoints))
	for _, point := range oldPoints {
		oldIDs[point.ID.String()] = struct{}{}
	}
	var got []string
	for _, point := range newPoints {
		if _, carried := oldIDs[point.ID.String()]; !carried {
			got = append(got, point.ID.String())
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extra %s IDs = %v, want %v", label, got, want)
	}
}

func assertExactExtraQueryIDs(t *testing.T, oldQueries, newQueries []Query, want []string) {
	t.Helper()
	oldIDs := make(map[string]struct{}, len(oldQueries))
	for _, query := range oldQueries {
		oldIDs[query.ID] = struct{}{}
	}
	var got []string
	for _, query := range newQueries {
		if _, carried := oldIDs[query.ID]; !carried {
			got = append(got, query.ID)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extra query IDs = %v, want %v", got, want)
	}
}

func assertQueryReferencesExist(t *testing.T, query Query, dataset *Dataset) {
	t.Helper()
	var points []FixturePoint
	if query.Target == "facts" {
		points = dataset.Facts
	} else {
		points = dataset.Chunks
	}
	ids := pointsByID(points)
	for _, expected := range query.Expected {
		if _, exists := ids[expected.ID]; !exists {
			t.Fatalf("query %q expected ID %q is missing", query.ID, expected.ID)
		}
	}
	for _, forbidden := range query.ForbiddenIDs {
		if _, exists := ids[forbidden]; !exists {
			t.Fatalf("query %q forbidden ID %q is missing", query.ID, forbidden)
		}
	}
}
