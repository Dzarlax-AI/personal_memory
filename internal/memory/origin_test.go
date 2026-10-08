package memory

import (
	"encoding/json"
	"testing"
)

func TestOriginSeparateFromSubject(t *testing.T) {
	args := map[string]interface{}{"source_project": "personal-assistant", "source_kind": "client_declared", "primary_tag": "personal-memory"}
	o, err := originArguments(args)
	if err != nil || o.SourceProject != "personal-assistant" || o.RecordedAt == "" {
		t.Fatal(o, err)
	}
	p := map[string]interface{}{"origin": o}
	if importOrigin(p) != nil || originPayload(p).SourceProject != o.SourceProject {
		t.Fatal("origin roundtrip")
	}
	b, _ := json.Marshal(p)
	var decoded map[string]interface{}
	json.Unmarshal(b, &decoded)
	if importOrigin(decoded) != nil {
		t.Fatal("export/import")
	}
}
func TestOriginMissingOrInvalid(t *testing.T) {
	o, err := originArguments(map[string]interface{}{})
	if err != nil || o != nil {
		t.Fatal("legacy origin must stay absent")
	}
	for _, a := range []map[string]interface{}{{"source_kind": "client_declared"}, {"source_project": "p"}, {"source_project": "p", "source_kind": "trusted"}} {
		if _, err := originArguments(a); err == nil {
			t.Fatal("invalid declaration accepted")
		}
	}
	if importOrigin(map[string]interface{}{"origin": "invalid"}) == nil {
		t.Fatal("invalid import")
	}
}
