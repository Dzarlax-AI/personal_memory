package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

const (
	publicV3DatasetSHA256  = "63e6329df9f4bf7a792338a4744765a8c9889933b34414671e5f00f0c3452e05"
	publicV3BaselineSHA256 = "60bdd5a255f763fa78b611c722f97a5df6b745c27a89c84e4653689467f583e7"
	publicV3DatasetVersion = "3.0.0"
	publicV3ModelRevision  = "614241f622f53c4eeff9890bdc4f31cfecc418b3"
)

var publicV3TopK = []int{1, 3, 5, 20}

func TestPublicV3DatasetPinnedContract(t *testing.T) {
	datasetData, dataset := loadPublicDataset(t, "v3")
	assertRawSHA256(t, "dataset", datasetData, publicV3DatasetSHA256)
	if dataset.SchemaVersion != CurrentDatasetSchemaVersion ||
		dataset.DatasetVersion != publicV3DatasetVersion {
		t.Fatalf("dataset identity = %d/%q", dataset.SchemaVersion, dataset.DatasetVersion)
	}
	assertPublicV3Embedding(t, dataset.Embedding)
	assertPublicV3Configuration(t, dataset.Configuration)
	assertPublicV3Gates(t, dataset.Gates)
	if len(dataset.Facts) != 26 || len(dataset.Chunks) != 11 ||
		len(dataset.Folders) != 8 || len(dataset.Queries) != 21 ||
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

	wantCohorts := map[QueryCohort][]string{
		CohortExactName: {
			"exact-name-document",
			"exact-name-fact",
		},
		CohortGeneralSemantic: {
			"document-flat",
			"document-hierarchical",
			"document-hierarchical-fallback",
			"document-missing-text",
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
}

func TestPublicV3CarriesPublicV2Contracts(t *testing.T) {
	_, v2 := loadPublicDataset(t, "v2")
	_, v3 := loadPublicDataset(t, "v3")

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
	assertExtraPointIDs(t, "facts", v2.Facts, v3.Facts, []string{
		"61000000-0000-4000-8000-000000000001",
		"61000000-0000-4000-8000-000000000002",
		"61000000-0000-4000-8000-000000000003",
		"61000000-0000-4000-8000-000000000004",
	})

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
	assertExtraPointIDs(t, "chunks", v2.Chunks, v3.Chunks, []string{
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa6",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa7",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa8",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa9",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa10",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa11",
	})

	v3Folders := pointsByID(v3.Folders)
	for _, want := range v2.Folders {
		got, exists := v3Folders[want.ID.String()]
		if !exists || !reflect.DeepEqual(got.Payload, want.Payload) {
			t.Fatalf("carried folder %q changed or is missing", want.ID.String())
		}
	}
	assertExtraPointIDs(t, "folders", v2.Folders, v3.Folders, []string{
		"f4444444-4444-4444-8444-444444444444",
		"f5555555-5555-4555-8555-555555555555",
		"f6666666-6666-4666-8666-666666666666",
		"f7777777-7777-4777-8777-777777777777",
		"f8888888-8888-4888-8888-888888888888",
	})

	if len(v2.Queries) != 16 {
		t.Fatalf("public v2 query count = %d, want 16", len(v2.Queries))
	}
	v3Queries := queriesByID(v3.Queries)
	for _, want := range v2.Queries {
		got, exists := v3Queries[want.ID]
		if !exists || !reflect.DeepEqual(carriedQueryContract(got), carriedQueryContract(want)) {
			t.Fatalf("carried query contract %q changed or is missing", want.ID)
		}
	}
	assertExactExtraQueryIDs(t, v2.Queries, v3.Queries, []string{
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
	root := filepath.Join("..", "..", "evaldata", "public", "v3")
	data, err := os.ReadFile(filepath.Join(root, "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertRawSHA256(t, "baseline", data, publicV3BaselineSHA256)
	baseline, err := DecodeReport(data)
	if err != nil {
		t.Fatal(err)
	}
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
		"document-missing-text",
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
}

func loadPublicDataset(t *testing.T, version string) ([]byte, *Dataset) {
	t.Helper()
	path := filepath.Join("..", "..", "evaldata", "public", version, "dataset.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := Load(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return data, dataset
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

func carriedQueryContract(query Query) Query {
	query.Vector = nil
	query.Cohorts = nil
	query.cohortsPresent = false
	return query
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
