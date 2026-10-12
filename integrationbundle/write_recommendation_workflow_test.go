package integrationbundle

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

func TestWriteRecommendationSyntheticReadCases(t *testing.T) {
	ref := "0123456789abcdef0123456789abcdef"
	cases := []struct {
		name string
		in   WriteRecommendationReadRequest
		want bool
	}{
		{"successful eligible store", WriteRecommendationReadRequest{true, true, ref, 0, ""}, true},
		{"tool absent", WriteRecommendationReadRequest{true, false, ref, 0, ""}, false},
		{"write failed", WriteRecommendationReadRequest{false, true, ref, 0, ""}, false},
		{"missing reference", WriteRecommendationReadRequest{true, true, "", 0, ""}, false},
		{"malformed reference", WriteRecommendationReadRequest{true, true, "../private", 0, ""}, false},
		{"uppercase reference", WriteRecommendationReadRequest{true, true, strings.ToUpper(ref), 0, ""}, false},
		{"one delayed pending read", WriteRecommendationReadRequest{true, true, ref, 1, "pending"}, true},
		{"unsuccessful store never qualifies", WriteRecommendationReadRequest{false, true, ref, 1, "pending"}, false},
		{"stop after second pending read", WriteRecommendationReadRequest{true, true, ref, 2, "pending"}, false},
		{"terminal result is not reread", WriteRecommendationReadRequest{true, true, ref, 1, "decided"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldReadWriteRecommendation(tc.in); got != tc.want {
				t.Fatalf("ShouldReadWriteRecommendation = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestPinnedV02RenderedInventoryAcrossConfigurations(t *testing.T) {
	legacy, err := loadPinnedPredecessorV02()
	if err != nil {
		t.Fatal(err)
	}
	type ownedEntry struct {
		Path          string       `json:"path"`
		Kind          artifactKind `json:"kind"`
		ManagedDigest string       `json:"managed_digest"`
	}
	type overrideEntry struct {
		Path      string `json:"path"`
		UserOwned bool   `json:"user_owned"`
	}
	type inventory struct {
		Owned     []ownedEntry    `json:"Owned"`
		Overrides []overrideEntry `json:"Overrides"`
	}
	states := []CapabilityState{CapabilityAvailable, CapabilityDisabled, CapabilityUnavailable}
	all := make(map[string]inventory)
	for _, memory := range states {
		for _, documents := range states {
			for _, todoist := range states {
				config := CapabilityConfig{Memory: memory, Documents: documents, Todoist: todoist}
				sets, err := legacy.renderUnchecked(config)
				if err != nil {
					t.Fatal(err)
				}
				for _, set := range sets {
					active, err := activate(set)
					if err != nil {
						t.Fatal(err)
					}
					entry := inventory{}
					for _, artifact := range active {
						entry.Owned = append(entry.Owned, ownedEntry{artifact.Path, artifact.Kind, artifact.ManagedDigest})
					}
					overrides, err := activeOverridePaths(set)
					if err != nil {
						t.Fatal(err)
					}
					for _, path := range overrides {
						entry.Overrides = append(entry.Overrides, overrideEntry{path, true})
					}
					key := fmt.Sprintf("%s:%s:%s:%s", set.ClientID, memory, documents, todoist)
					all[key] = entry
				}
			}
		}
	}
	if len(all) != 108 {
		t.Fatalf("pinned predecessor inventory cases = %d, want 108", len(all))
	}
	encoded, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%x", sha256.Sum256(encoded))
	const want = "8070de979d85b0c0efb513a6fe7d764b346868448ffba3d335f61fd597e03ff2"
	if got != want {
		t.Fatalf("0.2.0 rendered inventory checksum = %s, want %s", got, want)
	}
}

func TestCompatiblePredecessorRequiresExactRenderedOwnedAndOverrideInventory(t *testing.T) {
	legacy, err := loadPinnedPredecessorV02()
	if err != nil {
		t.Fatal(err)
	}
	config := CapabilityConfig{Memory: CapabilityDisabled, Documents: CapabilityDisabled, Todoist: CapabilityDisabled}
	sets, err := legacy.renderUnchecked(config)
	if err != nil {
		t.Fatal(err)
	}
	var state installState
	for _, set := range sets {
		if set.ClientID != conformance.ClientCodex {
			continue
		}
		active, err := activate(set)
		if err != nil {
			t.Fatal(err)
		}
		state = stateFor(set, active, "previous")
		break
	}
	if err := compatibleState(state, conformance.ClientCodex); err != nil {
		t.Fatalf("exact shipped predecessor rejected: %v", err)
	}
	tampered := state
	tampered.Owned = append([]ownedState(nil), state.Owned...)
	tampered.Owned[0].ManagedDigest = strings.Repeat("f", 64)
	tampered.DigestSHA256 = stateDigest(tampered)
	if err := compatibleState(tampered, conformance.ClientCodex); err == nil {
		t.Fatal("recomputed state digest allowed noncanonical predecessor artifact")
	}
	tampered = state
	tampered.Overrides = append([]overrideState(nil), state.Overrides...)
	tampered.Overrides[0].Path = "overrides/arbitrary.local.md"
	tampered.DigestSHA256 = stateDigest(tampered)
	if err := compatibleState(tampered, conformance.ClientCodex); err == nil {
		t.Fatal("recomputed state digest allowed noncanonical predecessor override")
	}
	for _, version := range []string{"0.1.0", "0.4.0"} {
		tampered = state
		tampered.BundleVersion = version
		tampered.DigestSHA256 = stateDigest(tampered)
		if err := compatibleState(tampered, conformance.ClientCodex); err == nil {
			t.Errorf("unsupported predecessor version %s was accepted", version)
		}
	}
}

func TestWriteRecommendationWorkflowRenderedAndMachineReadable(t *testing.T) {
	b := loadTestBundle(t)
	workflows := b.Manifest().OptionalWorkflows
	if len(workflows) != 2 {
		t.Fatalf("optional workflows = %d, want project registration and write recommendation", len(workflows))
	}
	w := workflows[1]
	if w.ID != "write_recommendation" || w.Tool != "get_write_recommendation" || w.Capability != "get_write_recommendation" || w.ReadLimit != 1 || w.PendingReadLimit != 1 || w.ApplyBehavior != "never_apply_suggested_or_related_tags_automatically" || w.ConsentBehavior != "never_add_allow_ai_processing_or_infer_consent" {
		t.Fatalf("write recommendation contract = %+v", w)
	}
	for _, status := range []string{"pending", "decided", "abstained", "failed", "outdated", "not_found", "unavailable"} {
		if !containsString(w.Statuses, status) {
			t.Errorf("status %q missing from contract", status)
		}
	}
	sets, err := b.Render(CapabilityConfig{Memory: CapabilityAvailable, Documents: CapabilityAvailable, Todoist: CapabilityDisabled})
	if err != nil {
		t.Fatal(err)
	}
	for client, paths := range canonicalInstructionPaths {
		for _, path := range paths {
			content := artifactContent(t, sets, client, path)
			for _, required := range []string{"Discover the exact get_write_recommendation tool", "ai.operation_ref", "ai_operation_ref", "at most one delayed read", "Never retry inference automatically", "applied=false", "Do not apply suggested or related tags automatically", "Explicit client tags and primary tags remain authoritative", "or add allow_ai_processing"} {
				if !strings.Contains(content, required) {
					t.Errorf("%s/%s missing workflow rule %q", client, path, required)
				}
			}
		}
	}
	var genericTools struct {
		Workflows []ClientWorkflow `json:"optional_workflows"`
	}
	if err := json.Unmarshal([]byte(artifactContent(t, sets, conformance.ClientGenericMCP, "generic-mcp/tool-mapping.json")), &genericTools); err != nil {
		t.Fatal(err)
	}
	if len(genericTools.Workflows) != 2 || genericTools.Workflows[1].Tool != "get_write_recommendation" {
		t.Fatalf("generic machine-readable workflow missing recommendation: %+v", genericTools.Workflows)
	}
}

func TestPinnedV02UpgradeRollbackRestoresExactPredecessor(t *testing.T) {
	legacy, err := loadPinnedPredecessorV02()
	if err != nil {
		t.Fatal(err)
	}
	current := loadTestBundle(t)
	config := CapabilityConfig{Memory: CapabilityUnavailable, Documents: CapabilityUnavailable, Todoist: CapabilityDisabled}
	oldSets, err := legacy.renderUnchecked(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, oldSet := range oldSets {
		t.Run(string(oldSet.ClientID), func(t *testing.T) {
			root := t.TempDir()
			newSet, err := renderClient(current, oldSet.ClientID, config)
			if err != nil {
				t.Fatal(err)
			}
			// Create installer directories, then seed the independently pinned old artifacts.
			if _, err = Install(InstallOptions{TargetRoot: root, set: newSet}); err != nil {
				t.Fatal(err)
			}
			active, err := activate(oldSet)
			if err != nil {
				t.Fatal(err)
			}
			before := make(map[string][]byte)
			for _, artifact := range active {
				data, err := mergeActive(artifact, nil, false)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, filepath.FromSlash(artifact.Path))
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				before[artifact.Path] = data
			}
			oldState := stateFor(oldSet, active, "")
			rewriteStateForTest(t, root, oldState)
			installedConfig, err := LoadInstalledConfig(root, current, oldSet.ClientID, nil)
			if err != nil || installedConfig != config {
				t.Fatalf("preserve predecessor config = %#v, %v", installedConfig, err)
			}
			if result, err := Update(InstallOptions{TargetRoot: root, set: newSet}); err != nil || !result.Changed {
				t.Fatalf("upgrade = %#v, %v", result, err)
			}
			if result, err := Verify(VerifyOptions{TargetRoot: root, set: newSet}); err != nil || (result.Status != StatusInstalled && result.Status != StatusManualActionRequired) {
				t.Fatalf("verify upgrade = %#v, %v", result, err)
			}
			if _, err := Rollback(RollbackOptions{TargetRoot: root, Client: oldSet.ClientID, Bundle: current, Config: config}); err != nil {
				t.Fatal(err)
			}
			for path, expected := range before {
				got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
				if err != nil || string(got) != string(expected) {
					t.Fatalf("rollback did not restore %s: %v", path, err)
				}
			}
			r, err := openValidatedRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			restored, exists, err := readState(r, oldSet.ClientID)
			if err != nil || !exists || restored.BundleVersion != predecessorBundleVersion {
				t.Fatalf("old state not restored: %v", err)
			}
			if err := compatibleState(restored, oldSet.ClientID); err != nil {
				t.Fatal(err)
			}
			if err := verifyOwned(r, restored); err != nil {
				t.Fatal(err)
			}
			if _, err := Update(InstallOptions{TargetRoot: root, set: newSet}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
