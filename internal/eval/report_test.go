package eval

import (
	"bytes"
	"testing"
)

func TestRenderReportIsDeterministic(t *testing.T) {
	report := Report{
		SchemaVersion:  1,
		DatasetVersion: "1.0.0",
		Mode:           "fixture",
		TopK:           []int{3, 1},
		Queries: []QueryReport{
			{ID: "z", Metrics: QueryMetrics{HitAt: map[int]float64{3: 1, 1: 0}}},
			{ID: "a", Metrics: QueryMetrics{HitAt: map[int]float64{3: 1, 1: 1}}},
		},
	}
	firstJSON, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := RenderJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("JSON render is not deterministic")
	}
	firstMarkdown := RenderMarkdown(report)
	secondMarkdown := RenderMarkdown(report)
	if firstMarkdown != secondMarkdown {
		t.Fatal("Markdown render is not deterministic")
	}
	if bytes.Index(firstJSON, []byte(`"id": "a"`)) > bytes.Index(firstJSON, []byte(`"id": "z"`)) {
		t.Fatal("queries are not sorted by ID")
	}
}
