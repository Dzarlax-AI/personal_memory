package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPairedFrozenPreview(t *testing.T) {
	b, err := os.ReadFile("../../evaldata/experiments/optional-ai-memory-v1/synthetic.json")
	if err != nil {
		t.Fatal(err)
	}
	var c Corpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	m, err := preview(c, b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Calls != 84 || m.DispatchAllowed {
		t.Fatal("matrix/dispatch")
	}
	first, _ := json.Marshal(m)
	second, err := preview(c, b)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(second)
	if string(first) != string(again) {
		t.Fatal("preview not reproducible")
	}
	for _, r := range m.Requests {
		if digest(r.Body) != r.SHA256 {
			t.Fatal("body hash")
		}
		if strings.Contains(string(r.Body), "evidence_refs") || strings.Contains(string(r.Body), "point_id") {
			t.Fatal("private fields")
		}
	}
}
func TestDuplicateCorpusIdentityRefused(t *testing.T) {
	b, err := os.ReadFile("../../evaldata/experiments/optional-ai-memory-v1/synthetic.json")
	if err != nil {
		t.Fatal(err)
	}
	var c Corpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	c.Writes[1].ID = c.Writes[0].ID
	if _, err := preview(c, b); err == nil {
		t.Fatal("duplicate case")
	}
}
