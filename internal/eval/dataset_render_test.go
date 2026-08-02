package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	for _, test := range []struct {
		name  string
		input string
	}{
		{name: "v1", input: validDataset},
		{name: "v2", input: validV2Dataset()},
		{name: "v2-absent-lifecycle-gate", input: string(v2WithoutLifecycleGateData)},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataset, err := Load(strings.NewReader(test.input))
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.MarshalIndent(dataset, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, '\n')
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
