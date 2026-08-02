package eval

import (
	"encoding/json"
	"strings"
	"testing"
)

func validV3Dataset() string {
	var document map[string]any
	if err := json.Unmarshal([]byte(validV2Dataset()), &document); err != nil {
		panic(err)
	}
	document["schema_version"] = float64(3)
	embedding := document["embedding"].(map[string]any)
	embedding["input_profile"] = string(LegacyRawV1)
	configuration := document["configuration"].(map[string]any)
	configuration["retrieval_strategy"] = string(RetrievalVectorOnly)
	configuration["dense_candidate_limit"] = float64(0)
	configuration["rrf_constant"] = float64(0)
	gates := document["gates"].(map[string]any)
	gates["forbid_lifecycle_violations"] = false
	query := document["queries"].([]any)[0].(map[string]any)
	query["cohorts"] = []any{
		string(CohortExactName),
		string(CohortGeneralSemantic),
		string(CohortIdentifierPath),
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestLoadV3RequiresVisibleEmbeddingAndRetrievalIdentity(t *testing.T) {
	dataset, err := Load(strings.NewReader(validV3Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	if dataset.SchemaVersion != 3 ||
		dataset.Embedding.InputProfile != LegacyRawV1 ||
		dataset.Configuration.RetrievalStrategy != RetrievalVectorOnly {
		t.Fatalf("dataset identity = %#v / %#v", dataset.Embedding, dataset.Configuration)
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "missing input profile",
			mutate: func(document map[string]any) {
				delete(document["embedding"].(map[string]any), "input_profile")
			},
			want: "input_profile",
		},
		{
			name: "null input profile",
			mutate: func(document map[string]any) {
				document["embedding"].(map[string]any)["input_profile"] = nil
			},
			want: "input_profile",
		},
		{
			name: "unknown input profile",
			mutate: func(document map[string]any) {
				document["embedding"].(map[string]any)["input_profile"] = "future-v9"
			},
			want: "input profile",
		},
		{
			name: "incompatible profile",
			mutate: func(document map[string]any) {
				document["embedding"].(map[string]any)["input_profile"] = string(MultilingualE5V1)
			},
			want: "does not support model",
		},
		{
			name: "missing strategy",
			mutate: func(document map[string]any) {
				delete(document["configuration"].(map[string]any), "retrieval_strategy")
			},
			want: "retrieval_strategy",
		},
		{
			name: "null strategy",
			mutate: func(document map[string]any) {
				document["configuration"].(map[string]any)["retrieval_strategy"] = nil
			},
			want: "retrieval_strategy",
		},
		{
			name: "vector candidate settings are not inert",
			mutate: func(document map[string]any) {
				document["configuration"].(map[string]any)["dense_candidate_limit"] = float64(10)
			},
			want: "vector-only",
		},
		{
			name: "hybrid candidate limit too small",
			mutate: func(document map[string]any) {
				cfg := document["configuration"].(map[string]any)
				cfg["retrieval_strategy"] = string(RetrievalHybridRRF)
				cfg["dense_candidate_limit"] = float64(2)
				cfg["rrf_constant"] = float64(60)
			},
			want: "dense_candidate_limit",
		},
		{
			name: "hybrid RRF constant missing",
			mutate: func(document map[string]any) {
				cfg := document["configuration"].(map[string]any)
				cfg["retrieval_strategy"] = string(RetrievalHybridRRF)
				cfg["dense_candidate_limit"] = float64(20)
			},
			want: "rrf_constant",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal([]byte(validV3Dataset()), &document); err != nil {
				t.Fatal(err)
			}
			tt.mutate(document)
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Load(strings.NewReader(string(encoded))); err == nil ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadV3AcceptsHybridAndMultilingualE5Profile(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal([]byte(validV3Dataset()), &document); err != nil {
		t.Fatal(err)
	}
	embedding := document["embedding"].(map[string]any)
	embedding["model_id"] = "intfloat/multilingual-e5-small"
	embedding["input_profile"] = string(MultilingualE5V1)
	configuration := document["configuration"].(map[string]any)
	configuration["retrieval_strategy"] = string(RetrievalHybridRRF)
	configuration["dense_candidate_limit"] = float64(20)
	configuration["rrf_constant"] = float64(60)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(strings.NewReader(string(encoded))); err != nil {
		t.Fatalf("Load(v3 hybrid): %v", err)
	}
}

func TestLoadV3RequiresCanonicalCohorts(t *testing.T) {
	tests := []struct {
		name    string
		cohorts any
		want    string
	}{
		{"missing", "DELETE", "cohorts"},
		{"null", nil, "cohorts"},
		{"empty", []any{}, "cohorts"},
		{"duplicate", []any{"exact-name", "exact-name"}, "duplicate"},
		{"unsorted", []any{"identifier-path", "exact-name"}, "sorted"},
		{"unsafe", []any{"private query text"}, "safe identifier"},
		{"non-kebab", []any{"Exact_Name"}, "safe identifier"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal([]byte(validV3Dataset()), &document); err != nil {
				t.Fatal(err)
			}
			query := document["queries"].([]any)[0].(map[string]any)
			if tt.cohorts == "DELETE" {
				delete(query, "cohorts")
			} else {
				query["cohorts"] = tt.cohorts
			}
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Load(strings.NewReader(string(encoded))); err == nil ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadHistoricalV1V2StillAcceptAbsentV3Fields(t *testing.T) {
	for name, value := range map[string]string{"v1": validDataset, "v2": validV2Dataset()} {
		t.Run(name, func(t *testing.T) {
			dataset, err := Load(strings.NewReader(value))
			if err != nil {
				t.Fatal(err)
			}
			if dataset.Embedding.InputProfile != "" ||
				dataset.Configuration.RetrievalStrategy != "" ||
				dataset.Configuration.DenseCandidateLimit != 0 ||
				dataset.Configuration.RRFConstant != 0 ||
				dataset.Queries[0].Cohorts != nil {
				t.Fatalf("historical v3 fields were synthesized: %#v", dataset)
			}
		})
	}
}

func TestLoadHistoricalSchemasRejectV3Fields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "input profile",
			mutate: func(document map[string]any) {
				document["embedding"].(map[string]any)["input_profile"] = string(LegacyRawV1)
			},
			want: "input_profile",
		},
		{
			name: "retrieval strategy",
			mutate: func(document map[string]any) {
				cfg := document["configuration"].(map[string]any)
				cfg["retrieval_strategy"] = string(RetrievalVectorOnly)
				cfg["dense_candidate_limit"] = float64(0)
				cfg["rrf_constant"] = float64(0)
			},
			want: "retrieval strategy fields",
		},
		{
			name: "cohorts",
			mutate: func(document map[string]any) {
				document["queries"].([]any)[0].(map[string]any)["cohorts"] = []any{"exact-name"}
			},
			want: "cohorts require",
		},
	}
	for _, version := range []struct {
		name  string
		value string
	}{{"v1", validDataset}, {"v2", validV2Dataset()}} {
		for _, tt := range tests {
			t.Run(version.name+"/"+tt.name, func(t *testing.T) {
				var document map[string]any
				if err := json.Unmarshal([]byte(version.value), &document); err != nil {
					t.Fatal(err)
				}
				tt.mutate(document)
				encoded, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := Load(strings.NewReader(string(encoded))); err == nil ||
					!strings.Contains(err.Error(), tt.want) {
					t.Fatalf("Load() error = %v, want containing %q", err, tt.want)
				}
			})
		}
	}
}
