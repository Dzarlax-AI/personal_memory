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
