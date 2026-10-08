package contextcatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func validSnapshot() Snapshot {
	return Snapshot{SchemaVersion: 1, Version: "1.0.0", CreatedAt: "2026-10-07T10:00:00Z", Entries: []Entry{{
		Namespace: "projects", Tag: "personal-memory", Name: "Personal Memory", Summary: "Semantic memory service.",
		Aliases: []string{"memory"}, OwnedComponents: []string{"memory API"}, Boundaries: []string{"No task storage."},
		Uses: []string{"Fact recall"}, SharedWith: []string{}, PositiveExamples: []string{"Memory service architecture"},
		NegativeExamples: []string{"A reminder"}, EvidenceRefs: []string{"docs/overview.md"}, ReviewStatus: "approved",
	}}}
}

func TestHashDeterministicAndPreservesMeaningfulArrayOrder(t *testing.T) {
	s := validSnapshot()
	first, err := Hash(s)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Hash(s)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("hash changed: %s != %s", first, second)
	}
	s.Entries[0].Aliases = []string{"second", "first"}
	reordered, err := Hash(s)
	if err != nil {
		t.Fatal(err)
	}
	if first == reordered {
		t.Fatal("array reordering should change the reviewed snapshot identity")
	}
}

func TestValidateLimitsAndNamespace(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"unknown namespace", func(s *Snapshot) { s.Entries[0].Namespace = "default" }},
		{"bad tag", func(s *Snapshot) { s.Entries[0].Tag = "Personal_Memory" }},
		{"unknown status", func(s *Snapshot) { s.Entries[0].ReviewStatus = "published" }},
		{"oversized description", func(s *Snapshot) { s.Entries[0].Summary = strings.Repeat("x", MaxDescriptionLen+1) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := validSnapshot()
			tc.mutate(&s)
			if err := Validate(s); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	s := validSnapshot()
	s.Entries = make([]Entry, MaxEntries+1)
	if err := Validate(s); err == nil {
		t.Fatal("expected entry count error")
	}
}

func TestCanonicalHashIgnoresJSONObjectKeyOrder(t *testing.T) {
	a := `{"schema_version":1,"catalog_version":"1.0.0","parent_hash":"","created_at":"2026-10-07T10:00:00Z","entries":[]}`
	b := `{"entries":[],"created_at":"2026-10-07T10:00:00Z","parent_hash":"","catalog_version":"1.0.0","schema_version":1}`
	decode := func(raw string) Snapshot {
		var s Snapshot
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	ha, err := Hash(decode(a))
	if err != nil {
		t.Fatal(err)
	}
	hb, err := Hash(decode(b))
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("object key order changed hash: %s != %s", ha, hb)
	}
}

func TestValidateAllowsSanitizedDescriptionSourceOnly(t *testing.T) {
	s := validSnapshot()
	s.Entries[0].DescriptionSource = "model_derived"
	if err := Validate(s); err != nil {
		t.Fatalf("sanitized provenance-only entry rejected: %v", err)
	}
	s.Entries[0].DescriptionSource = "invented"
	if err := Validate(s); err == nil {
		t.Fatal("unknown description source accepted")
	}
}
