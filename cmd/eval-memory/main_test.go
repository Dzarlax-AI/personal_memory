package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	memoryeval "github.com/Dzarlax-AI/personal-memory/internal/eval"
)

func TestCLIRejectsUnknownSubcommand(t *testing.T) {
	err := runCLI([]string{"unknown"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunCLIRejectsSameReportPath(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report")
	err := runCLI([]string{
		"run",
		"--dataset", filepath.Join(dir, "missing.json"),
		"--json", reportPath,
		"--markdown", filepath.Join(dir, ".", "report"),
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "different files") {
		t.Fatalf("error = %v, want same output path rejection", err)
	}
}

func TestRunCLIForbidsFixtureInputProfileRelabel(t *testing.T) {
	dir := t.TempDir()
	err := runCLI([]string{
		"run", "--source", "fixture",
		"--dataset", filepath.Join("..", "..", "evaldata", "public", "v1", "dataset.json"),
		"--json", filepath.Join(dir, "report.json"),
		"--markdown", filepath.Join(dir, "report.md"),
		"--input-profile", "multilingual-e5-v1",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot relabel") {
		t.Fatalf("error = %v, want fixture relabel rejection", err)
	}
}

func TestRunCLITEIFixtureRequiresV3BeforeExternalWork(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EMBED_URL", "http://127.0.0.1:1")
	err := runCLI([]string{
		"run", "--source", "tei-fixture",
		"--dataset", filepath.Join("..", "..", "evaldata", "public", "v1", "dataset.json"),
		"--json", filepath.Join(dir, "report.json"),
		"--markdown", filepath.Join(dir, "report.md"),
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "schema_version 3") ||
		strings.Contains(err.Error(), "TEI") {
		t.Fatalf("error = %v, want preflight schema rejection", err)
	}
}

func TestRunCLIFixtureIgnoresAmbientEmbedURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EMBED_URL", "http://127.0.0.1:1")
	err := runCLI([]string{
		"run", "--source", "fixture",
		"--dataset", filepath.Join("..", "..", "evaldata", "public", "v1", "dataset.json"),
		"--qdrant-url", "http://127.0.0.1:1",
		"--json", filepath.Join(dir, "report.json"),
		"--markdown", filepath.Join(dir, "report.md"),
		"--timeout", "10ms",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected unavailable Qdrant error")
	}
	if strings.Contains(err.Error(), "TEI") || strings.Contains(err.Error(), "embed") {
		t.Fatalf("fixture consumed ambient EMBED_URL: %v", err)
	}
}

func TestRunCLILiveProfileOverrideReembedsPopulatedVector(t *testing.T) {
	dir := t.TempDir()
	datasetPath := filepath.Join(dir, "dataset.json")
	dataset := `{
		"schema_version":3,"dataset_version":"cli-profile-v3",
		"embedding":{"provider":"tei","model_id":"intfloat/multilingual-e5-small","model_revision":"rev","dtype":"float32","pooling":"mean","vector_size":2,"input_profile":"legacy-raw-v1"},
		"configuration":{"name":"base","fact_collection":"memory","chunk_collection":"doc_chunks","folder_collection":"doc_folders","folder_top_k":1,"folder_threshold":0.5,"top_k":[1],"retrieval_strategy":"vector-only","dense_candidate_limit":0,"rrf_constant":0},
		"facts":[{"id":42,"vector":[1,0],"payload":{"text":"numeric","lifecycle_state":"current","canonical":true,"supersedes":[],"superseded_by":[]}}],
		"chunks":[],"folders":[],
		"queries":[{"id":"q","target":"facts","mode":"flat","text":"numeric","vector":[1,0],"intent":"current","expected":[{"id":"42","grade":3}],"cohorts":["general-semantic"]}],
		"gates":{"forbid_invariant_violations":true,"forbid_lifecycle_violations":false}
	}`
	if err := os.WriteFile(datasetPath, []byte(dataset), 0o600); err != nil {
		t.Fatal(err)
	}
	var teiInput string
	tei := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info":
			_, _ = w.Write([]byte(`{"model_id":"intfloat/multilingual-e5-small","model_sha":"rev","model_dtype":"float32","model_type":{"embedding":{"pooling":"mean"}},"version":"test"}`))
		case "/embed":
			var body struct {
				Inputs []string `json:"inputs"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode TEI input: %v", err)
			}
			if len(body.Inputs) > 0 {
				teiInput = body.Inputs[0]
			}
			_, _ = w.Write([]byte(`[[0,1]]`))
		default:
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer tei.Close()
	var searchedVector []float32
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/collections/memory" {
			_, _ = w.Write([]byte(`{"result":{"points_count":1,"config":{
				"params":{"vectors":{"size":2,"distance":"Cosine"}},
				"metadata":{"personal_memory.embedding":{"schema_version":1,"provider":"tei","model_id":"intfloat/multilingual-e5-small","model_revision":"rev","model_dtype":"float32","pooling":"mean","vector_size":2,"input_profile":"multilingual-e5-v1"}}
			}}}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/collections/memory/points/search" {
			var body struct {
				Vector []float32 `json:"vector"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode Qdrant search: %v", err)
			}
			searchedVector = body.Vector
			_, _ = w.Write([]byte(`{"result":[{"id":42,"score":1,"payload":{"text":"numeric","lifecycle_state":"current","canonical":true,"supersedes":[],"superseded_by":[]}}]}`))
			return
		}
		http.Error(w, fmt.Sprintf("unexpected %s %s", r.Method, r.URL.Path), http.StatusInternalServerError)
	}))
	defer qdrant.Close()
	err := runCLI([]string{
		"run", "--source", "live", "--dataset", datasetPath,
		"--qdrant-url", qdrant.URL, "--embed-url", tei.URL,
		"--input-profile", "multilingual-e5-v1",
		"--json", filepath.Join(dir, "report.json"),
		"--markdown", filepath.Join(dir, "report.md"),
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if teiInput != "query: numeric" || len(searchedVector) != 2 ||
		searchedVector[0] != 0 || searchedVector[1] != 1 {
		t.Fatalf("TEI input=%q searched vector=%v", teiInput, searchedVector)
	}
}

func materializationDatasetJSON() string {
	return `{
		"schema_version":3,
		"dataset_version":"materialize-test-v3",
		"embedding":{
			"provider":"tei",
			"model_id":"intfloat/multilingual-e5-small",
			"model_revision":"rev",
			"dtype":"float32",
			"pooling":"mean",
			"vector_size":2,
			"input_profile":"legacy-raw-v1"
		},
		"configuration":{
			"name":"base",
			"fact_collection":"memory",
			"chunk_collection":"doc_chunks",
			"folder_collection":"doc_folders",
			"folder_top_k":1,
			"folder_threshold":0.5,
			"top_k":[1],
			"retrieval_strategy":"vector-only",
			"dense_candidate_limit":0,
			"rrf_constant":0
		},
		"facts":[{
			"id":42,
			"payload":{
				"text":"private materialization fact",
				"lifecycle_state":"current",
				"canonical":true,
				"supersedes":[],
				"superseded_by":[]
			}
		}],
		"chunks":[],
		"folders":[],
		"queries":[{
			"id":"q",
			"target":"facts",
			"mode":"flat",
			"text":"private materialization query",
			"expected":[{"id":"42","grade":3}],
			"cohorts":["general-semantic"]
		}],
		"gates":{
			"forbid_invariant_violations":true,
			"forbid_lifecycle_violations":false
		}
	}`
}

func writeMaterializationDataset(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "input.json")
	if err := os.WriteFile(path, []byte(materializationDatasetJSON()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMaterializeCLIRejectsSameInputAndOutputPath(t *testing.T) {
	path := writeMaterializationDataset(t, t.TempDir())
	err := runCLI([]string{
		"materialize", "--dataset", path, "--output", filepath.Join(filepath.Dir(path), ".", "input.json"),
		"--embed-url", "http://127.0.0.1:1",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "different files") {
		t.Fatalf("error = %v", err)
	}
}

func TestMaterializeCLIRejectsV1AndV2BeforeTEI(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output.json")
			err := runCLI([]string{
				"materialize",
				"--dataset", filepath.Join("..", "..", "evaldata", "public", version, "dataset.json"),
				"--output", output,
				"--embed-url", "http://127.0.0.1:1",
			}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "schema_version 3") ||
				strings.Contains(err.Error(), "TEI") {
				t.Fatalf("error = %v", err)
			}
			if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("output exists after rejection: %v", statErr)
			}
		})
	}
}

func TestMaterializeCLIValidatesTEIIdentityAndModelAssertion(t *testing.T) {
	infoCalls := 0
	tei := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			t.Fatalf("unexpected external request %s", r.URL.Path)
		}
		infoCalls++
		revision := "other-revision"
		if infoCalls == 2 {
			revision = "rev"
		}
		fmt.Fprintf(w, `{
			"model_id":"intfloat/multilingual-e5-small",
			"model_sha":%q,
			"model_dtype":"float32",
			"model_type":{"embedding":{"pooling":"mean"}},
			"version":"test"
		}`, revision)
	}))
	defer tei.Close()
	dir := t.TempDir()
	input := writeMaterializationDataset(t, dir)
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{
			"materialize", "--dataset", input, "--output", filepath.Join(dir, "identity.json"),
			"--embed-url", tei.URL,
		}, want: "embedding identity"},
		{args: []string{
			"materialize", "--dataset", input, "--output", filepath.Join(dir, "model.json"),
			"--embed-url", tei.URL, "--embed-model", "other-model",
		}, want: "--embed-model"},
	} {
		err := runCLI(test.args, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "TEI") ||
			!strings.Contains(err.Error(), test.want) {
			t.Fatalf("error = %v", err)
		}
		if _, statErr := os.Stat(test.args[4]); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("output exists after identity rejection: %v", statErr)
		}
	}
}

func TestMaterializeCLIProfileOverrideWritesStrictDeterministicFixture(t *testing.T) {
	var inputs [][]string
	tei := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info":
			_, _ = w.Write([]byte(`{
				"model_id":"intfloat/multilingual-e5-small",
				"model_sha":"rev",
				"model_dtype":"float32",
				"model_type":{"embedding":{"pooling":"mean"}},
				"version":"test"
			}`))
		case "/embed":
			var body struct {
				Inputs []string `json:"inputs"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			inputs = append(inputs, append([]string(nil), body.Inputs...))
			vectors := make([][]float32, len(body.Inputs))
			for i := range vectors {
				vectors[i] = []float32{1, 0}
			}
			if err := json.NewEncoder(w).Encode(vectors); err != nil {
				t.Fatal(err)
			}
		default:
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer tei.Close()
	dir := t.TempDir()
	input := writeMaterializationDataset(t, dir)
	output := filepath.Join(dir, "output.json")
	var stdout bytes.Buffer
	err := runCLI([]string{
		"materialize", "--dataset", input, "--output", output,
		"--embed-url", tei.URL,
		"--embed-model", "intfloat/multilingual-e5-small",
		"--input-profile", "multilingual-e5-v1",
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	wantInputs := [][]string{
		{"passage: private materialization fact"},
		{"query: private materialization query"},
	}
	if !reflect.DeepEqual(inputs, wantInputs) {
		t.Fatalf("TEI inputs = %#v, want %#v", inputs, wantInputs)
	}
	file, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	dataset, loadErr := memoryeval.Load(file)
	closeErr := file.Close()
	if loadErr != nil || closeErr != nil {
		t.Fatalf("strict output decode = %v, close = %v", loadErr, closeErr)
	}
	if dataset.Embedding.InputProfile != memoryeval.MultilingualE5V1 ||
		len(dataset.Facts[0].Vector) != 2 || len(dataset.Queries[0].Vector) != 2 {
		t.Fatalf("materialized dataset = %#v", dataset)
	}
	if err := dataset.ValidateForSource("fixture"); err != nil {
		t.Fatalf("fixture contract = %v", err)
	}
	if strings.Contains(stdout.String(), "private materialization") ||
		strings.Contains(stdout.String(), "[1") {
		t.Fatalf("summary leaked text/vector: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "facts=1") ||
		!strings.Contains(stdout.String(), "queries=1") ||
		!strings.Contains(stdout.String(), "profile=multilingual-e5-v1") {
		t.Fatalf("summary = %q", stdout.String())
	}
}

func TestMaterializeCLIEmbeddingFailureDoesNotWriteOutput(t *testing.T) {
	embedCalls := 0
	tei := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info":
			_, _ = w.Write([]byte(`{
				"model_id":"intfloat/multilingual-e5-small",
				"model_sha":"rev",
				"model_dtype":"float32",
				"model_type":{"embedding":{"pooling":"mean"}},
				"version":"test"
			}`))
		case "/embed":
			embedCalls++
			if embedCalls == 2 {
				http.Error(w, "private materialization query", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`[[1,0]]`))
		}
	}))
	defer tei.Close()
	dir := t.TempDir()
	input := writeMaterializationDataset(t, dir)
	before, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "output.json")
	err = runCLI([]string{
		"materialize", "--dataset", input, "--output", output, "--embed-url", tei.URL,
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "embed queries") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "private materialization") {
		t.Fatalf("embedding error leaked provider response: %v", err)
	}
	if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output exists after embed failure: %v", statErr)
	}
	after, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("source file changed after embed failure")
	}
}

func TestCompareCLIProducesDeterministicOutput(t *testing.T) {
	dir := t.TempDir()
	report := memoryeval.Report{
		SchemaVersion: 1, DatasetVersion: "1.0.0", Mode: "fixture",
		Embedding:     memoryeval.EmbeddingIdentity{Provider: "synthetic", ModelID: "m", ModelRevision: "r", DType: "float32", Pooling: "mean", VectorSize: 2},
		Configuration: memoryeval.Configuration{Name: "cfg"},
		TopK:          []int{1}, Aggregate: memoryeval.AggregateMetrics{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 1}, MRR: 1},
		Queries: []memoryeval.QueryReport{{
			ID: "q", Target: "facts", Mode: "flat",
			Metrics: memoryeval.QueryMetrics{HitAt: map[int]float64{1: 1}, NDCGAt: map[int]float64{1: 1}, MRR: 1},
		}},
		GatesPassed: true,
	}
	data, err := memoryeval.RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(dir, "baseline.json")
	candidate := filepath.Join(dir, "candidate.json")
	if err := os.WriteFile(baseline, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var first, second bytes.Buffer
	args := []string{"compare", "--baseline", baseline, "--candidate", candidate, "--enforce-gates"}
	if err := runCLI(args, &first, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := runCLI(args, &second, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("comparison output is not deterministic")
	}
}

func TestWriteAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "report.json")
	if err := writeAtomic(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("content = %q", data)
	}
}
