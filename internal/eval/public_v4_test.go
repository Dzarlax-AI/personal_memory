package eval

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicV4DocumentRoutingEvidenceIsCanonical(t *testing.T) {
	_, datasetData, dataset := loadPublicDataset(t, "v4")
	assertCanonicalDataset(t, filepath.Join("..", "..", "evaldata", "public", "v4", "dataset.json"), datasetData, dataset)
	if dataset.SchemaVersion != DocumentRoutingSchemaVersion || dataset.DatasetVersion != "4.0.0" {
		t.Fatalf("dataset identity = %d/%q", dataset.SchemaVersion, dataset.DatasetVersion)
	}
	if dataset.Configuration.DocumentRoutingStrategy != DocumentRoutingHierarchical ||
		dataset.Configuration.RoutingCandidateLimit != 20 || dataset.Configuration.RoutingRRFConstant != 60 {
		t.Fatalf("routing configuration = %#v", dataset.Configuration)
	}
	if len(dataset.Facts) != 1 || len(dataset.Chunks) != 11 || len(dataset.Folders) != 8 ||
		len(dataset.Queries) != 8 || len(dataset.TransitionScenarios) != 0 {
		t.Fatal("public v4 fixture must remain compact and routing-focused")
	}

	baseline := loadPublicV4Report(t, "hierarchical-only.json")
	cases := []struct {
		report, comparison string
	}{
		{"flat-only.json", "flat-only-failing-comparison.json"},
		{"blended-rrf.json", "blended-rrf-failing-comparison.json"},
		{"reranker-unavailable-fail-open.json", "reranker-unavailable-failing-comparison.json"},
	}
	for _, test := range cases {
		candidate := loadPublicV4Report(t, test.report)
		comparisonPath := filepath.Join("..", "..", "evaldata", "public", "v4", test.comparison)
		pinned, err := os.ReadFile(comparisonPath)
		if err != nil {
			t.Fatal(err)
		}
		recomputed, err := Compare(baseline, candidate, true)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := RenderComparisonJSON(recomputed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(pinned, canonical) {
			t.Fatalf("%s is not canonical", comparisonPath)
		}
		if recomputed.GatesPassed {
			t.Fatalf("%s must preserve no-winner evidence", comparisonPath)
		}
	}
}

func loadPublicV4Report(t *testing.T, name string) Report {
	t.Helper()
	path := filepath.Join("..", "..", "evaldata", "public", "v4", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := DecodeReport(data)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	canonical, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, canonical) {
		t.Fatalf("%s is not canonical", path)
	}
	return report
}
