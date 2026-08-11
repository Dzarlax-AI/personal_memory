package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRenderDatasetJSONPreservesHistoricalV1V2Bytes(t *testing.T) {
	var v2WithoutLifecycleGate map[string]any
	if err := json.Unmarshal([]byte(validV2Dataset()), &v2WithoutLifecycleGate); err != nil {
		t.Fatal(err)
	}
	delete(
		v2WithoutLifecycleGate["gates"].(map[string]any),
		"forbid_lifecycle_violations",
	)
	v2WithoutLifecycleGateData, err := json.Marshal(v2WithoutLifecycleGate)
	if err != nil {
		t.Fatal(err)
	}
	v2ExplicitFalse := decodeTestDocument(t, []byte(validV2Dataset()))
	v2ExplicitFalse["gates"].(map[string]any)["forbid_lifecycle_violations"] = false
	v2ExplicitFalseData, err := json.Marshal(v2ExplicitFalse)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		input string
	}{
		{name: "v1", input: validDataset},
		{name: "v2-true", input: validV2Dataset()},
		{name: "v2-explicit-false", input: string(v2ExplicitFalseData)},
		{name: "v2-absent-lifecycle-gate", input: string(v2WithoutLifecycleGateData)},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataset, err := Load(strings.NewReader(test.input))
			if err != nil {
				t.Fatal(err)
			}
			want, err := renderBASECompatibleHistoricalDataset(dataset)
			if err != nil {
				t.Fatal(err)
			}
			got, err := RenderDatasetJSON(dataset)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("historical rendering changed\nwant:\n%s\ngot:\n%s", want, got)
			}
		})
	}
}

func renderBASECompatibleHistoricalDataset(dataset *Dataset) ([]byte, error) {
	type baseGates struct {
		ForbidInvariantViolations bool               `json:"forbid_invariant_violations"`
		ForbidLifecycleViolations *bool              `json:"forbid_lifecycle_violations,omitempty"`
		MinimumHitAt              map[string]float64 `json:"minimum_hit_at,omitempty"`
		MinimumMRR                *float64           `json:"minimum_mrr,omitempty"`
		MinimumNDCGAt             map[string]float64 `json:"minimum_ndcg_at,omitempty"`
	}
	gates := baseGates{
		ForbidInvariantViolations: dataset.Gates.ForbidInvariantViolations,
		MinimumHitAt:              dataset.Gates.MinimumHitAt,
		MinimumMRR:                dataset.Gates.MinimumMRR,
		MinimumNDCGAt:             dataset.Gates.MinimumNDCGAt,
	}
	if dataset.Gates.ForbidLifecycleViolations {
		gates.ForbidLifecycleViolations = &dataset.Gates.ForbidLifecycleViolations
	}
	wire := struct {
		SchemaVersion       int                  `json:"schema_version"`
		DatasetVersion      string               `json:"dataset_version"`
		Embedding           EmbeddingIdentity    `json:"embedding"`
		Configuration       Configuration        `json:"configuration"`
		Facts               []FixturePoint       `json:"facts"`
		Chunks              []FixturePoint       `json:"chunks"`
		Folders             []FixturePoint       `json:"folders"`
		Queries             []Query              `json:"queries"`
		Gates               baseGates            `json:"gates"`
		TransitionScenarios []TransitionScenario `json:"transition_scenarios,omitempty"`
	}{
		SchemaVersion: dataset.SchemaVersion, DatasetVersion: dataset.DatasetVersion,
		Embedding: dataset.Embedding, Configuration: dataset.Configuration,
		Facts: dataset.Facts, Chunks: dataset.Chunks, Folders: dataset.Folders,
		Queries: dataset.Queries, Gates: gates,
		TransitionScenarios: dataset.TransitionScenarios,
	}
	data, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func TestGatesMarshalPreservesProgrammaticV2LifecycleTrue(t *testing.T) {
	dataset, err := Load(strings.NewReader(validV2Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Gates.forbidLifecycleViolationsPresent = false
	dataset.Gates.ForbidLifecycleViolations = true
	data, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	gates := document["gates"].(map[string]any)
	if value, exists := gates["forbid_lifecycle_violations"]; !exists || value != true {
		t.Fatalf("programmatic v2 lifecycle gate = %#v", gates)
	}
}

func TestRenderDatasetJSONAlwaysEmitsStrictV3LifecycleGate(t *testing.T) {
	for _, value := range []bool{false, true} {
		t.Run(fmt.Sprintf("%t", value), func(t *testing.T) {
			dataset, err := Load(strings.NewReader(validV3Dataset()))
			if err != nil {
				t.Fatal(err)
			}
			dataset.Gates.forbidLifecycleViolationsPresent = false
			dataset.Gates.ForbidLifecycleViolations = value
			data, err := RenderDatasetJSON(dataset)
			if err != nil {
				t.Fatal(err)
			}
			if dataset.Gates.forceLifecycleViolationsRender {
				t.Fatal("RenderDatasetJSON mutated source gate rendering metadata")
			}
			decoded, err := Load(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("strict v3 round-trip failed: %v\n%s", err, data)
			}
			if decoded.Gates.ForbidLifecycleViolations != value {
				t.Fatalf("lifecycle gate = %t, want %t",
					decoded.Gates.ForbidLifecycleViolations, value)
			}
		})
	}
}

func TestConfigurationMarshalJSONCoversEveryExportedWireField(t *testing.T) {
	configuration := Configuration{
		Name: "full", FactCollection: "facts", ChunkCollection: "chunks",
		FolderCollection: "folders", FolderTopK: 3, FolderThreshold: 0.5,
		TopK: []int{1, 3}, RetrievalStrategy: RetrievalHybridRRF,
		DenseCandidateLimit: 40, RRFConstant: 60,
		DocumentRoutingStrategy: DocumentRoutingBlendedRRF, RoutingCandidateLimit: 40,
		RoutingRRFConstant: 60, RerankerModelID: "model/revision",
		RerankerCandidateCap: 20, RerankerTimeoutMS: 500,
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]json.RawMessage
	if err := json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}

	expected := make(map[string]struct{})
	configurationType := reflect.TypeOf(configuration)
	for i := 0; i < configurationType.NumField(); i++ {
		field := configurationType.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			expected[name] = struct{}{}
		}
	}
	if len(actual) != len(expected) {
		t.Fatalf("configuration JSON keys = %v, want %v", actual, expected)
	}
	for name := range expected {
		if _, exists := actual[name]; !exists {
			t.Fatalf("Configuration.MarshalJSON omitted exported field %q", name)
		}
	}
}
