package viz

import (
	"encoding/json"
	"testing"
)

func TestOriginSummaryProjectsValidatedFields(t *testing.T) {
	p := map[string]interface{}{"origin": map[string]interface{}{"source_project": "alpha", "source_kind": "client_declared", "recorded_at": "2026-10-07T10:00:00Z", "private_extra": "hidden"}}
	var summary map[string]interface{}
	if err := json.Unmarshal(originSummary(p), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary) != 3 || summary["source_project"] != "alpha" {
		t.Fatal("unexpected origin projection")
	}
	p["origin"].(map[string]interface{})["recorded_at"] = "invalid"
	if originSummary(p) != nil {
		t.Fatal("malformed timestamp exposed")
	}
	if originSummary(map[string]interface{}{}) != nil {
		t.Fatal("legacy origin invented")
	}
}
