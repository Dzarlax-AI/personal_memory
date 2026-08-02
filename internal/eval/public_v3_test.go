package eval

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicV3DatasetAndBaselineContract(t *testing.T) {
	root := filepath.Join("..", "..", "evaldata", "public", "v3")
	datasetData, err := os.ReadFile(filepath.Join(root, "dataset.json"))
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := Load(bytes.NewReader(datasetData))
	if err != nil {
		t.Fatal(err)
	}
	if err := dataset.ValidateForSource("fixture"); err != nil {
		t.Fatal(err)
	}
	canonicalDataset, err := RenderDatasetJSON(dataset)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(datasetData, canonicalDataset) {
		t.Fatal("public v3 dataset is not in canonical rendered form")
	}

	baselineData, err := os.ReadFile(filepath.Join(root, "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := DecodeReport(baselineData)
	if err != nil {
		t.Fatal(err)
	}
	canonicalBaseline, err := RenderJSON(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(baselineData, canonicalBaseline) {
		t.Fatal("public v3 baseline is not in canonical rendered form")
	}
	if baseline.SchemaVersion != CurrentReportSchemaVersion ||
		baseline.DatasetVersion != dataset.DatasetVersion ||
		baseline.Mode != "fixture" ||
		!strictEmbeddingIdentityEqual(baseline.Embedding, dataset.Embedding) ||
		!strictConfigurationEqual(baseline.Configuration, dataset.Configuration) ||
		len(baseline.Queries) != len(dataset.Queries) {
		t.Fatalf("public v3 baseline identity does not match dataset")
	}
	if !baseline.GatesPassed || len(baseline.GateFailures) != 0 ||
		baseline.Aggregate.InvariantViolations != 0 ||
		baseline.Lifecycle == nil || baseline.Lifecycle.Aggregate.Violations != 0 {
		t.Fatalf("public v3 baseline is not gate-clean: %#v", baseline)
	}
	cohorts := cohortMap(baseline.Cohorts)
	for _, protected := range []QueryCohort{CohortExactName, CohortIdentifierPath} {
		if cohort, ok := cohorts[protected]; !ok || cohort.QueryCount == 0 {
			t.Fatalf("public v3 protected cohort %q is empty", protected)
		}
	}
}
