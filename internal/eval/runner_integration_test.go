package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/memory/lifecycle"
)

func TestFixtureRunnerIntegration(t *testing.T) {
	qdrantURL := os.Getenv("QDRANT_TEST_URL")
	if qdrantURL == "" {
		t.Skip("QDRANT_TEST_URL is not set")
	}
	before := listEvaluationCollections(t, qdrantURL)
	file, err := os.Open(filepath.Join("..", "..", "evaldata", "public", "v1", "dataset.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close public dataset: %v", err)
		}
	})
	dataset, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), dataset, RunOptions{Source: "fixture", QdrantURL: qdrantURL})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Queries) != 8 || report.Aggregate.HitAt[1] != 1 || !report.GatesPassed {
		t.Fatalf("report = %#v", report)
	}
	first, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	secondReport, err := Run(context.Background(), dataset, RunOptions{Source: "fixture", QdrantURL: qdrantURL})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderJSON(secondReport)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("independent Qdrant runs produced different reports")
	}
	baseline, err := os.ReadFile(filepath.Join("..", "..", "evaldata", "public", "v1", "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, baseline) {
		t.Fatal("public baseline differs from a fresh Qdrant report")
	}
	after := waitForEvaluationCollections(t, qdrantURL, before)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("eval collections leaked: before=%v after=%v", before, after)
	}
}

func TestPublicDatasetLoads(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "evaldata", "public", "v1", "dataset.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close public dataset: %v", err)
		}
	})
	dataset, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if dataset.DatasetVersion != "1.0.0" || len(dataset.Queries) != 8 {
		t.Fatalf("dataset identity/coverage = %q/%d", dataset.DatasetVersion, len(dataset.Queries))
	}
}

func TestLiveRunnerUsesOnlySearchRequests(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Method != http.MethodPost || r.URL.Path != "/collections/memory/points/search" {
			t.Fatalf("live runner made non-search request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"result":[{"id":42,"score":1,"payload":{"text":"numeric"}}]}`))
	}))
	defer server.Close()
	dataset, err := Load(strings.NewReader(validDataset))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts = nil
	report, err := Run(context.Background(), dataset, RunOptions{Source: "live", QdrantURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || report.Aggregate.MRR != 1 {
		t.Fatalf("paths/report = %v/%#v", paths, report)
	}
}

func TestLiveV2LifecycleQuerySearchesBroadCandidatesAndGatesViolations(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"result":[
			{"id":99,"score":1,"payload":{"text":"obsolete","lifecycle_state":"historical"}},
			{"id":42,"score":0.7,"payload":{"text":"current","lifecycle_state":"current","canonical":true}}
		]}`))
	}))
	defer server.Close()

	dataset, err := Load(strings.NewReader(validV2Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts = nil
	dataset.Queries[0].LifecycleExpectations[0].Decision = PresentationSuppress
	dataset.Gates.ForbidLifecycleViolations = true
	report, err := Run(context.Background(), dataset, RunOptions{Source: "live", QdrantURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, filtered := requestBody["filter"]; filtered {
		t.Fatalf("lifecycle query unexpectedly used current-only filter: %#v", requestBody["filter"])
	}
	if report.SchemaVersion != CurrentReportSchemaVersion || report.Lifecycle == nil {
		t.Fatalf("v2 lifecycle report missing: %#v", report)
	}
	if got := resultIDs(report.Queries[0].Results); len(got) != 1 || got[0] != "42" {
		t.Fatalf("relevance result IDs = %v, want only current candidate 42", got)
	}
	assertCandidate(t, *report.Queries[0].Lifecycle, "99", lifecycle.Historical, PresentationSuppress, ReasonHistorical)
	if report.GatesPassed ||
		len(report.GateFailures) != 1 ||
		report.GateFailures[0] != "query q1 candidate 42 invariant decision" {
		t.Fatalf("gate result = passed %t failures %#v", report.GatesPassed, report.GateFailures)
	}
	encoded, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(encoded); err != nil {
		t.Fatalf("decode generated broad-search report: %v", err)
	}
}

func TestLiveV2CurrentIncludeExpectationKeepsCurrentFilter(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"result":[
			{"id":42,"score":0.7,"payload":{"text":"current","lifecycle_state":"current","canonical":true}}
		]}`))
	}))
	defer server.Close()

	dataset, err := Load(strings.NewReader(validV2Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts = nil
	report, err := Run(context.Background(), dataset, RunOptions{Source: "live", QdrantURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	filter, filtered := requestBody["filter"].(map[string]any)
	if !filtered || filter["should"] == nil {
		t.Fatalf("current include expectation did not preserve current-only filter: %#v", requestBody)
	}
	encoded, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(encoded); err != nil {
		t.Fatalf("decode generated filtered report: %v", err)
	}
}

func TestLiveV2CurrentDemoteExpectationKeepsCurrentFilter(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"result":[
			{"id":42,"score":0.9,"payload":{"text":"ordinary","lifecycle_state":"current","canonical":false}},
			{"id":99,"score":0.7,"payload":{"text":"canonical","lifecycle_state":"current","canonical":true}}
		]}`))
	}))
	defer server.Close()

	dataset, err := Load(strings.NewReader(validV2Dataset()))
	if err != nil {
		t.Fatal(err)
	}
	dataset.Facts = nil
	expectation := &dataset.Queries[0].LifecycleExpectations[0]
	expectation.Decision = PresentationDemote
	expectation.ReasonCodes = []string{string(ReasonCanonicalPreference)}
	report, err := Run(context.Background(), dataset, RunOptions{Source: "live", QdrantURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	filter, filtered := requestBody["filter"].(map[string]any)
	if !filtered || filter["should"] == nil {
		t.Fatalf("current demote expectation did not preserve current-only filter: %#v", requestBody)
	}
	if len(report.Queries[0].Lifecycle.Violations) != 0 {
		t.Fatalf("demote lifecycle violations = %#v", report.Queries[0].Lifecycle.Violations)
	}
	encoded, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(encoded); err != nil {
		t.Fatalf("decode generated demote report: %v", err)
	}
}

func listEvaluationCollections(t *testing.T, qdrantURL string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(qdrantURL, "/")+"/collections",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		closeErr := response.Body.Close()
		if closeErr != nil {
			t.Fatalf("list collections returned status %d; close body: %v", response.StatusCode, closeErr)
		}
		t.Fatalf("list collections returned status %d", response.StatusCode)
	}
	var decoded struct {
		Result struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		} `json:"result"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&decoded)
	closeErr := response.Body.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	var names []string
	for _, collection := range decoded.Result.Collections {
		if strings.HasPrefix(collection.Name, "eval_") {
			names = append(names, collection.Name)
		}
	}
	sort.Strings(names)
	return names
}

func waitForEvaluationCollections(t *testing.T, qdrantURL string, want []string) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := listEvaluationCollections(t, qdrantURL)
		if fmt.Sprint(got) == fmt.Sprint(want) {
			return got
		}
		if time.Now().After(deadline) {
			return got
		}
		time.Sleep(25 * time.Millisecond)
	}
}
