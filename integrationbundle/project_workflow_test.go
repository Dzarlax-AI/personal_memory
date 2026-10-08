package integrationbundle

import (
	"strings"
	"testing"
)

func TestProjectRegistrationSyntheticWorkflowCases(t *testing.T) {
	valid := ProjectRegistrationResult{Status: "created", ProjectID: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", Tag: "personal-memory", CatalogHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}

	// These eight cases exercise the optional workflow contract. They are
	// synthetic contract evidence, not observations from proprietary clients.
	cases := []struct {
		name         string
		decision     ProjectRegistrationDecision
		result       ProjectRegistrationResult
		wantCall     bool
		wantIdentity bool
	}{
		{"enabled tool before first call", ProjectRegistrationDecision{true, true, true}, valid, true, true},
		{"missing tool keeps baseline", ProjectRegistrationDecision{true, false, true}, ProjectRegistrationResult{Status: "unavailable"}, false, false},
		{"disabled tool keeps baseline", ProjectRegistrationDecision{true, false, true}, ProjectRegistrationResult{Status: "disabled"}, false, false},
		{"cannot persist stable key skips registration", ProjectRegistrationDecision{true, true, false}, ProjectRegistrationResult{Status: "unavailable"}, false, false},
		{"ambiguous result yields no identity", ProjectRegistrationDecision{true, true, true}, ProjectRegistrationResult{Status: "ambiguous", ProjectID: valid.ProjectID, Tag: valid.Tag, CatalogHash: valid.CatalogHash}, true, false},
		{"proposed update is not active identity", ProjectRegistrationDecision{true, true, true}, ProjectRegistrationResult{Status: "proposed_update", ProjectID: valid.ProjectID, Tag: valid.Tag, CatalogHash: valid.CatalogHash}, true, false},
		{"unavailable result yields no identity", ProjectRegistrationDecision{true, true, true}, ProjectRegistrationResult{Status: "unavailable"}, true, false},
		{"worktree reuses the caller-persisted key", ProjectRegistrationDecision{true, true, true}, ProjectRegistrationResult{Status: "existing", ProjectID: valid.ProjectID, Tag: valid.Tag, CatalogHash: valid.CatalogHash}, true, true},
		{"origin tag does not replace subject tag", ProjectRegistrationDecision{true, true, true}, valid, true, true},
	}
	if len(cases) != 9 {
		t.Fatalf("synthetic project workflow case count = %d, want 9", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldEnsureProject(tc.decision); got != tc.wantCall {
				t.Fatalf("ShouldEnsureProject = %t, want %t", got, tc.wantCall)
			}
			id, tag, ok := ProjectRegistrationIdentity(tc.result)
			if ok != tc.wantIdentity {
				t.Fatalf("ProjectRegistrationIdentity ok = %t, want %t", ok, tc.wantIdentity)
			}
			if ok && (id != tc.result.ProjectID || tag != tc.result.Tag) {
				t.Fatalf("identity = (%q, %q), want returned identity", id, tag)
			}
		})
	}
}

func TestProjectRegistrationRenderedRuleConformance(t *testing.T) {
	for _, text := range []string{"Ordinary life facts need no project registration", "subject_scope", "2048 UTF-8 bytes", "namespace_scope_mismatch", "discovered schema"} {
		if !strings.Contains(projectRegistrationInstructions, text) {
			t.Fatal("missing subject contract", text)
		}
	}
	if projectRegistrationInstructions == "" || !strings.Contains(projectRegistrationInstructions, "separate call before the first") || !strings.Contains(projectRegistrationInstructions, "hooks never register or store facts automatically") {
		t.Fatal("registration workflow lost its separate-call or no-auto-store rule")
	}
}

func TestProjectRegistrationInstructionsAreConsistentAndSeparateMutation(t *testing.T) {
	b := loadTestBundle(t)
	sets, err := b.Render(CapabilityConfig{Memory: CapabilityAvailable, Documents: CapabilityAvailable, Todoist: CapabilityDisabled})
	if err != nil {
		t.Fatal(err)
	}
	for client, paths := range canonicalInstructionPaths {
		for _, path := range paths {
			content := artifactContent(t, sets, client, path)
			for _, required := range []string{"ensure_project", "currently available and enabled", "before the first", "source_project", "project_context_id", "primary_tag", "hooks never register or store facts automatically"} {
				if !strings.Contains(content, required) {
					t.Fatalf("%s/%s missing workflow contract text %q", client, path, required)
				}
			}
		}
	}
}

func TestProjectRegistrationRejectsIncompleteOrInvalidIdentity(t *testing.T) {
	base := ProjectRegistrationResult{Status: "existing", ProjectID: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", Tag: "personal-memory", CatalogHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	for name, mutate := range map[string]func(*ProjectRegistrationResult){
		"missing id":           func(r *ProjectRegistrationResult) { r.ProjectID = "" },
		"invalid tag":          func(r *ProjectRegistrationResult) { r.Tag = "../private" },
		"invalid catalog hash": func(r *ProjectRegistrationResult) { r.CatalogHash = "hash" },
		"invalid project id":   func(r *ProjectRegistrationResult) { r.ProjectID = "project-7" },
	} {
		t.Run(name, func(t *testing.T) {
			result := base
			mutate(&result)
			if _, _, ok := ProjectRegistrationIdentity(result); ok {
				t.Fatal("incomplete or invalid identity was accepted")
			}
		})
	}
}
