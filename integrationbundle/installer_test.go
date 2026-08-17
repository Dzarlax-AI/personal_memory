package integrationbundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

func testSet(t *testing.T, client conformance.ClientFamily) ArtifactSet {
	t.Helper()
	contract, err := os.ReadFile(filepath.Join("..", "docs", "model-usage-contract.md"))
	if err != nil {
		t.Fatal(err)
	}
	suite, err := os.ReadFile(filepath.Join("..", "conformancedata", "public", "v1", "scenarios.json"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load(contract, suite)
	if err != nil {
		t.Fatal(err)
	}
	sets, err := b.Render(CapabilityConfig{Memory: CapabilityUnavailable, Documents: CapabilityUnavailable, Todoist: CapabilityDisabled})
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range sets {
		if set.ClientID == client {
			return set
		}
	}
	t.Fatal("client not rendered")
	return ArtifactSet{}
}

func TestInstallVerifyIdempotentAndRollback(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	result, err := Install(InstallOptions{TargetRoot: root, set: set})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusInstalled {
		t.Fatalf("status = %s", result.Status)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(VerifyOptions{TargetRoot: root, set: set})
	if err != nil || verified.Status != StatusInstalled {
		t.Fatalf("verify = %#v, %v", verified, err)
	}
	result, err = Update(InstallOptions{TargetRoot: root, set: set})
	if err != nil || result.Changed {
		t.Fatalf("idempotent update = %#v, %v", result, err)
	}
	if _, err := Rollback(RollbackOptions{TargetRoot: root, Client: set.ClientID, set: set}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("fresh-install rollback did not restore absence")
	}
}

func TestUpgradeAndRollbackRestore(t *testing.T) {
	root := t.TempDir()
	old := testSet(t, conformance.ClientCodex)
	if _, err := Install(InstallOptions{TargetRoot: root, set: old}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "overrides", "AGENTS.local.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(InstallOptions{TargetRoot: root, set: old}); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(RollbackOptions{TargetRoot: root, Client: old.ClientID, set: old}); err != nil {
		t.Fatal(err)
	}
	got, err := safeRead(root, "AGENTS.md")
	managed, _ := codexManaged(got)
	active, _ := activate(old)
	if err != nil || digest(managed) != active[0].ManagedDigest {
		t.Fatal("rollback did not restore previous artifact")
	}
}

func TestApplyFailureAutomaticallyRestores(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientClaude)
	if _, err := Install(InstallOptions{TargetRoot: root, set: set, FailAfterWrites: 1}); err == nil {
		t.Fatal("expected injected failure")
	}
	active, _ := activate(set)
	for _, a := range active {
		if _, err := safeRead(root, a.Path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("artifact remained: %s", a.Path)
		}
	}
}

func TestClaudePreservesSurroundingSettingsAndIgnoresTemp(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, "settings.json")
	os.WriteFile(settings, []byte(`{"unknown":true,"hooks":{"Other":[]}}`), 0o600)
	os.WriteFile(filepath.Join(root, ".personal-memory-tmp-interrupted"), []byte("ignore"), 0o600)
	if _, err := Install(InstallOptions{TargetRoot: root, set: testSet(t, conformance.ClientClaude)}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(settings)
	var decoded map[string]json.RawMessage
	if json.Unmarshal(got, &decoded) != nil || string(decoded["unknown"]) != "true" {
		t.Fatal("Claude unrelated setting changed")
	}
	if _, err := claudeManaged(got); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedInstallReportsRetentionWarningWithoutFailure(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	result, err := Install(InstallOptions{TargetRoot: root, set: set, prune: func(*rootContext, conformance.ClientFamily, string) error {
		return errors.New("injected retention failure")
	}})
	if err != nil {
		t.Fatalf("committed install reported failure: %v", err)
	}
	if result.Status != StatusInstalled || !result.Changed || !reflect.DeepEqual(result.Warnings, []string{"backup_retention_failed"}) {
		t.Fatalf("unexpected committed result: %+v", result)
	}
	verified, err := Verify(VerifyOptions{TargetRoot: root, set: set})
	if err != nil || verified.Status != StatusInstalled {
		t.Fatalf("committed install is not verifiable: result=%+v err=%v", verified, err)
	}
}

func TestBackupTamperRefused(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	rt, _ := openValidatedRoot(root)
	defer func() { _ = rt.Close() }()
	st, _, _ := readState(rt, set.ClientID)
	manifest := filepath.Join(root, filepath.FromSlash(stateDir(set.ClientID)+"/backups/"+st.PreviousTransaction+"/manifest.json"))
	if err := os.WriteFile(manifest, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(RollbackOptions{TargetRoot: root, Client: set.ClientID, set: set}); err == nil {
		t.Fatal("accepted tampered backup")
	}
}

func TestOwnedDriftAndOverridePreservation(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	override := filepath.Join(root, "overrides", "AGENTS.local.md")
	if err := os.WriteFile(override, []byte("custom"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(override); string(got) != "custom" {
		t.Fatal("override changed")
	}
	owned := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(owned, []byte("user edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(InstallOptions{TargetRoot: root, set: set}); err == nil {
		t.Fatal("expected drift conflict")
	}
}

func TestRollbackFailureAutomaticallyRecoversCurrent(t *testing.T) {
	root := t.TempDir()
	old := testSet(t, conformance.ClientClaude)
	if _, err := Install(InstallOptions{TargetRoot: root, set: old}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "overrides", "personal-memory.local.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(InstallOptions{TargetRoot: root, set: old}); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(RollbackOptions{TargetRoot: root, Client: old.ClientID, set: old, FailAfterWrites: 1}); err == nil {
		t.Fatal("expected rollback failure")
	}
	active, _ := activate(old)
	for _, a := range active {
		got, err := safeRead(root, a.Path)
		managed := got
		if a.Kind == kindClaudeSettings {
			managed, _ = claudeManaged(got)
		}
		if err != nil || digest(managed) != a.ManagedDigest {
			t.Fatalf("current installation not recovered: %s", a.Path)
		}
	}
}

func TestSymlinkEscapeRejected(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "skills")); err != nil {
		t.Skip(err)
	}
	if _, err := Install(InstallOptions{TargetRoot: root, set: testSet(t, conformance.ClientCodex)}); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestChatGPTManualAndMixedCapabilities(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientChatGPT)
	r, err := Install(InstallOptions{TargetRoot: root, set: set})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusManualActionRequired {
		t.Fatalf("status = %s", r.Status)
	}
	v, err := Verify(VerifyOptions{TargetRoot: root, set: set, Discovery: Discovery{Performed: true, Tools: []string{"recall_facts"}}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Capabilities.Todoist != CapabilityDisabled {
		t.Fatal("disabled Todoist changed")
	}
}

func TestCodexActiveManagedBlockPreservesSurroundingAndRejectsMarkerDrift(t *testing.T) {
	root := t.TempDir()
	original := []byte("before\n\nafter\n")
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	set := testSet(t, conformance.ClientCodex)
	if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if !bytes.HasPrefix(got, original) || bytes.Count(got, []byte(codexBegin)) != 1 || bytes.Count(got, []byte(codexEnd)) != 1 {
		t.Fatal("active managed block invalid or surrounding bytes changed")
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "personal-memory", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if !bytes.Equal(got, got2) {
		t.Fatal("idempotent update changed AGENTS.md")
	}
	tampered := bytes.Replace(got, []byte(codexEnd), []byte(codexBegin), 1)
	os.WriteFile(filepath.Join(root, "AGENTS.md"), tampered, 0o600)
	v, err := Verify(VerifyOptions{TargetRoot: root, set: set})
	if err != nil {
		t.Fatal(err)
	} else if v.Status != StatusDrifted {
		t.Fatalf("status=%s", v.Status)
	}
}

func TestCodexDuplicateMarkersRefused(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(codexBegin+"\n"+codexEnd+"\n"+codexBegin+"\n"+codexEnd), 0o600)
	if _, err := Install(InstallOptions{TargetRoot: root, set: testSet(t, conformance.ClientCodex)}); err == nil {
		t.Fatal("duplicate markers accepted")
	}
}

func TestClaudeActiveMergeRollbackPreservesUnrelatedChanges(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, "settings.json")
	initial := []byte(`{"theme":"dark","hooks":{"Other":[{"x":1}],"UserPromptSubmit":[{"matcher":"user","hooks":[]}]}}`)
	os.WriteFile(settings, initial, 0o600)
	set := testSet(t, conformance.ClientClaude)
	if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	installed, _ := os.ReadFile(settings)
	var m map[string]json.RawMessage
	if json.Unmarshal(installed, &m) != nil || string(m["theme"]) != `"dark"` {
		t.Fatal("unknown settings not preserved")
	}
	var hooks map[string][]json.RawMessage
	if json.Unmarshal(m["hooks"], &hooks) != nil || len(hooks["Other"]) != 1 || len(hooks["UserPromptSubmit"]) != 2 {
		t.Fatal("unrelated hooks not preserved")
	}
	m["new_user_field"] = json.RawMessage(`true`)
	changed, _ := json.Marshal(m)
	os.WriteFile(settings, changed, 0o600)
	if _, err := Rollback(RollbackOptions{TargetRoot: root, Client: set.ClientID, set: set}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(settings)
	if bytes.Contains(after, []byte(claudeHookCommandIdentity)) || !bytes.Contains(after, []byte(`"new_user_field": true`)) {
		t.Fatalf("rollback did not preserve user settings: %s", after)
	}
}

func TestOverrideDeletionIsPlannedAndRepaired(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	override := "overrides/AGENTS.local.md"
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(override))); err != nil {
		t.Fatal(err)
	}
	p, err := PlanUpdate(InstallOptions{TargetRoot: root, set: set})
	if err != nil {
		t.Fatal(err)
	}
	a := findAction(p.Actions, override)
	if a.Action != "create_override" {
		t.Fatalf("override action=%s", a.Action)
	}
	if _, err = Update(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	if got, err := safeRead(root, override); err != nil || !bytes.Equal(got, overrideStub(override)) {
		t.Fatal("override not repaired")
	}
}

func TestDiscoveryTriStateAndDisabledTodoist(t *testing.T) {
	set := testSet(t, conformance.ClientGenericMCP)
	set.CapabilityConfig.Memory = CapabilityAvailable
	set.CapabilityConfigSHA256 = capabilityConfigDigest(set.CapabilityConfig)
	for root, discovery := range map[string]Discovery{t.TempDir(): {}, t.TempDir(): {Performed: true}} {
		if _, err := Install(InstallOptions{TargetRoot: root, set: set, Discovery: discovery}); err == nil {
			t.Fatal("incomplete discovery accepted")
		}
	}
	tools := []string{"recall_facts", "store_fact", "update_fact", "set_fact_lifecycle"}
	if _, err := Install(InstallOptions{TargetRoot: t.TempDir(), set: set, Discovery: Discovery{Performed: true, Tools: tools}}); err != nil {
		t.Fatal(err)
	}
	if set.CapabilityConfig.Todoist != CapabilityDisabled {
		t.Fatal("Todoist invariant changed")
	}
}

func TestMutationAfterPlanAbortsWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	user := []byte("concurrent user bytes")
	_, err := Install(InstallOptions{TargetRoot: root, set: set, beforeApply: func(r *rootContext) error {
		f, e := r.OpenFile("AGENTS.md", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if e != nil {
			return e
		}
		_, e = f.Write(user)
		return errors.Join(e, f.Close())
	}})
	if err == nil {
		t.Fatal("concurrent mutation accepted")
	}
	got, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if !bytes.Equal(got, user) {
		t.Fatal("concurrent content overwritten")
	}
}

func TestCompoundRecoveryErrorIsReported(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientClaude)
	_, err := Install(InstallOptions{TargetRoot: root, set: set, FailAfterWrites: 1, failRecovery: true})
	if err == nil || !strings.Contains(err.Error(), "automatic recovery failed") || !strings.Contains(err.Error(), "injected apply failure") {
		t.Fatalf("compound error not reported: %v", err)
	}
}

func TestApplyFailureAfterOverrideCreationRemovesNewStub(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	_, err := Install(InstallOptions{TargetRoot: root, set: set, FailAfterWrites: 3})
	if err == nil {
		t.Fatal("expected failure after override creation")
	}
	if _, err = safeRead(root, "overrides/AGENTS.local.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("new override stub survived failed transaction")
	}
}

func unavailableConfig() CapabilityConfig {
	return CapabilityConfig{Memory: CapabilityUnavailable, Documents: CapabilityUnavailable, Todoist: CapabilityDisabled}
}
func TestPublicMutationAPIUsesBundleAuthority(t *testing.T) {
	b := loadTestBundle(t)
	root := t.TempDir()
	if _, err := Install(InstallOptions{TargetRoot: root, Bundle: b, Client: conformance.ClientCodex, Config: unavailableConfig()}); err != nil {
		t.Fatal(err)
	}
	v, err := Verify(VerifyOptions{TargetRoot: root, Bundle: b, Client: conformance.ClientCodex, Config: unavailableConfig()})
	if err != nil || v.Status != StatusInstalled {
		t.Fatalf("verify=%+v err=%v", v, err)
	}
	if _, err := Install(InstallOptions{TargetRoot: t.TempDir(), Client: conformance.ClientCodex, Config: unavailableConfig()}); err == nil {
		t.Fatal("nil bundle accepted")
	}
}

func TestFreshInstallRejectsEveryExistingOwnedDestination(t *testing.T) {
	for _, client := range []conformance.ClientFamily{conformance.ClientCodex, conformance.ClientClaude, conformance.ClientChatGPT, conformance.ClientGenericMCP} {
		set := testSet(t, client)
		active, err := activate(set)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range active {
			if a.Kind != kindFile {
				continue
			}
			t.Run(string(client)+"/"+strings.ReplaceAll(a.Path, "/", "_"), func(t *testing.T) {
				root := t.TempDir()
				dest := filepath.Join(root, filepath.FromSlash(a.Path))
				os.MkdirAll(filepath.Dir(dest), 0o700)
				os.WriteFile(dest, []byte("unowned"), 0o600)
				if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err == nil {
					t.Fatal("existing unowned destination accepted")
				}
			})
		}
	}
}

func TestFreshInstallRejectsUnownedManagedIdentity(t *testing.T) {
	codex := testSet(t, conformance.ClientCodex)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(codexBegin+"\nforeign\n"+codexEnd+"\n"), 0o600)
	if _, err := Install(InstallOptions{TargetRoot: root, set: codex}); err == nil {
		t.Fatal("unowned Codex markers adopted")
	}
	claude := testSet(t, conformance.ClientClaude)
	source := t.TempDir()
	if _, err := Install(InstallOptions{TargetRoot: source, set: claude}); err != nil {
		t.Fatal(err)
	}
	settings, _ := os.ReadFile(filepath.Join(source, "settings.json"))
	fresh := t.TempDir()
	os.WriteFile(filepath.Join(fresh, "settings.json"), settings, 0o600)
	if _, err := Install(InstallOptions{TargetRoot: fresh, set: claude}); err == nil {
		t.Fatal("unowned Claude hook adopted")
	}
}

func TestStateMutationAfterPlanAborts(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	updated := set
	updated.Artifacts = append([]Artifact(nil), set.Artifacts...)
	updated.Artifacts[0].Content = append(append([]byte(nil), updated.Artifacts[0].Content...), '\n')
	updated.Artifacts[0].DigestSHA256 = digest(updated.Artifacts[0].Content)
	updated.DigestSHA256 = artifactSetDigest(updated.Artifacts)
	_, err := Update(InstallOptions{TargetRoot: root, set: updated, beforeApply: func(r *rootContext) error { return atomicWriteRoot(r, statePath(set.ClientID), []byte("{}\n"), 0o600) }})
	if err == nil || !strings.Contains(err.Error(), "state changed") {
		t.Fatalf("state mutation accepted: %v", err)
	}
}

func TestRootReplacementCannotRedirectTransaction(t *testing.T) {
	root := t.TempDir()
	moved := root + "-moved"
	set := testSet(t, conformance.ClientCodex)
	_, err := Install(InstallOptions{TargetRoot: root, set: set, beforeApply: func(_ *rootContext) error {
		if err := os.Rename(root, moved); err != nil {
			return err
		}
		return os.Mkdir(root, 0o700)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "AGENTS.md")); err != nil {
		t.Fatal("anchored original root not mutated")
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("replacement root was mutated")
	}
}

func TestFreshClaudeRollbackRestoresAbsentSettings(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientClaude)
	if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(RollbackOptions{TargetRoot: root, Client: set.ClientID, set: set}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("empty managed-only settings file not removed")
	}
}

func rewriteStateForTest(t *testing.T, root string, st installState) {
	t.Helper()
	st.DigestSHA256 = stateDigest(st)
	b, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(statePath(st.Client))), append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
func rewriteManifestForTest(t *testing.T, root string, c conformance.ClientFamily, tx string, m backupManifest) {
	t.Helper()
	m.DigestSHA256 = backupDigest(m)
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(stateDir(c)+"/backups/"+tx+"/manifest.json")), append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackRejectsNoncanonicalCurrentInventoryAndMissingOverride(t *testing.T) {
	for _, mode := range []string{"inventory", "override"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			set := testSet(t, conformance.ClientCodex)
			if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
				t.Fatal(err)
			}
			if mode == "inventory" {
				r, _ := openValidatedRoot(root)
				st, _, _ := readState(r, set.ClientID)
				r.Close()
				st.Owned = append(st.Owned, ownedState{Path: "unrelated.txt", Kind: kindFile, ManagedDigest: digest(nil)})
				rewriteStateForTest(t, root, st)
			} else {
				os.Remove(filepath.Join(root, "overrides", "AGENTS.local.md"))
			}
			if _, err := Rollback(RollbackOptions{TargetRoot: root, Client: set.ClientID, set: set}); err == nil {
				t.Fatal("rollback accepted invalid installed state")
			}
		})
	}
}

func TestRollbackRejectsDuplicateOrMismatchedBackupEntries(t *testing.T) {
	for _, mode := range []string{"duplicate", "kind"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			set := testSet(t, conformance.ClientCodex)
			if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
				t.Fatal(err)
			}
			r, _ := openValidatedRoot(root)
			st, _, _ := readState(r, set.ClientID)
			m, err := loadBackup(r, set.ClientID, st.PreviousTransaction, set, nil)
			r.Close()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "duplicate" {
				m.Files = append(m.Files, m.Files[0])
			} else {
				m.Files[0].Kind = kindFile
			}
			rewriteManifestForTest(t, root, set.ClientID, st.PreviousTransaction, m)
			if _, err := Rollback(RollbackOptions{TargetRoot: root, Client: set.ClientID, set: set}); err == nil {
				t.Fatal("rollback accepted inconsistent backup inventory")
			}
		})
	}
}

func TestRollbackRejectsPriorStateManagedDigestMismatch(t *testing.T) {
	root := t.TempDir()
	old := testSet(t, conformance.ClientCodex)
	if _, err := Install(InstallOptions{TargetRoot: root, set: old}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "overrides", "AGENTS.local.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(InstallOptions{TargetRoot: root, set: old}); err != nil {
		t.Fatal(err)
	}
	r, _ := openValidatedRoot(root)
	st, _, _ := readState(r, old.ClientID)
	m, err := loadBackup(r, old.ClientID, st.PreviousTransaction, old, nil)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	for i := range m.Files {
		if m.Files[i].Kind == kindCodexBlock {
			p := filepath.Join(root, filepath.FromSlash(backupFilePath(old.ClientID, st.PreviousTransaction, m.Files[i].Path)))
			data, _ := os.ReadFile(p)
			data = bytes.Replace(data, []byte("Personal Memory"), []byte("Personal Mem0ry"), 1)
			os.WriteFile(p, data, 0o600)
			m.Files[i].DigestSHA256 = digest(data)
		}
	}
	rewriteManifestForTest(t, root, old.ClientID, st.PreviousTransaction, m)
	if _, err := Rollback(RollbackOptions{TargetRoot: root, Client: old.ClientID, set: old}); err == nil {
		t.Fatal("rollback accepted backup content inconsistent with prior state")
	}
}

func TestRollbackRejectsPairedPriorStateAndBackupContentMutation(t *testing.T) {
	root := t.TempDir()
	b := loadTestBundle(t)
	config := unavailableConfig()
	if _, err := Install(InstallOptions{TargetRoot: root, Bundle: b, Client: conformance.ClientCodex, Config: config}); err != nil {
		t.Fatal(err)
	}
	set, err := renderClient(b, conformance.ClientCodex, config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, "overrides", "AGENTS.local.md")); err != nil {
		t.Fatal(err)
	}
	if _, err = Update(InstallOptions{TargetRoot: root, Bundle: b, Client: set.ClientID, Config: config}); err != nil {
		t.Fatal(err)
	}
	r, _ := openValidatedRoot(root)
	current, _, _ := readState(r, set.ClientID)
	m, err := loadBackup(r, set.ClientID, current.PreviousTransaction, set, b)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	for i := range m.Files {
		if m.Files[i].Kind != kindCodexBlock {
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(backupFilePath(set.ClientID, current.PreviousTransaction, m.Files[i].Path)))
		data, _ := os.ReadFile(p)
		data = bytes.Replace(data, []byte("Personal Memory"), []byte("Personal Mem0ry"), 1)
		os.WriteFile(p, data, 0o600)
		m.Files[i].DigestSHA256 = digest(data)
		managed, e := codexManaged(data)
		if e != nil {
			t.Fatal(e)
		}
		for j := range m.State.Owned {
			if m.State.Owned[j].Path == m.Files[i].Path {
				m.State.Owned[j].ManagedDigest = digest(managed)
			}
		}
	}
	m.State.DigestSHA256 = stateDigest(*m.State)
	rewriteManifestForTest(t, root, set.ClientID, current.PreviousTransaction, m)
	if _, err = Rollback(RollbackOptions{TargetRoot: root, Bundle: b, Client: set.ClientID, Config: config}); err == nil {
		t.Fatal("rollback accepted paired prior-state and backup-content mutation")
	}
}

func TestInventoryDuplicateCannotSubstituteMissingEntry(t *testing.T) {
	set := testSet(t, conformance.ClientCodex)
	active, _ := activate(set)
	st := stateFor(set, active, "tx")
	st.Owned = []ownedState{st.Owned[0], st.Owned[0]}
	st.DigestSHA256 = stateDigest(st)
	if inventoryMatches(st, active) || inventoryShapeMatches(st, active) || compatibleState(st, set.ClientID) == nil {
		t.Fatal("duplicate inventory substituted for missing canonical entry")
	}
}

func TestInstallerLockCancellationAndRelease(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	r, err := openValidatedRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	held, err := r.mutation.lock(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Install(InstallOptions{TargetRoot: root, set: set, Context: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lock cancellation=%v", err)
	}
	if err = held.Close(); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if _, err = Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
}

func TestLoadInstalledConfigRequiresVerifiedState(t *testing.T) {
	b := loadTestBundle(t)
	root := t.TempDir()
	cfg := CapabilityConfig{Memory: CapabilityAvailable, Documents: CapabilityAvailable, Todoist: CapabilityDisabled}
	discovery := Discovery{Performed: true, Tools: []string{"recall_facts", "store_fact", "update_fact", "set_fact_lifecycle", "search_documents"}}
	if _, err := Install(InstallOptions{TargetRoot: root, Bundle: b, Client: conformance.ClientCodex, Config: cfg, Discovery: discovery}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadInstalledConfig(root, b, conformance.ClientCodex, nil)
	if err != nil || got != cfg {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "personal-memory", "SKILL.md"), []byte("drift\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInstalledConfig(root, b, conformance.ClientCodex, nil); err == nil {
		t.Fatal("drifted state accepted")
	}
}

func TestConcurrentInstallerCannotUndoPeer(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := Install(InstallOptions{TargetRoot: root, set: set, beforeApply: func(*rootContext) error { close(entered); <-release; return nil }})
		firstDone <- err
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := Install(InstallOptions{TargetRoot: root, set: set, Context: ctx}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("peer did not block on lock: %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	v, err := Verify(VerifyOptions{TargetRoot: root, set: set})
	if err != nil || v.Status != StatusInstalled {
		t.Fatalf("peer state lost: %+v %v", v, err)
	}
}

func TestCodexMarkersStandaloneAndCRLFRoundtrip(t *testing.T) {
	root := t.TempDir()
	ordinary := []byte("prose " + codexBegin + " not a marker\r\nnext\r\n")
	os.WriteFile(filepath.Join(root, "AGENTS.md"), ordinary, 0o600)
	set := testSet(t, conformance.ClientCodex)
	if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if !bytes.HasPrefix(got, ordinary) || bytes.Contains(bytes.ReplaceAll(got, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("CRLF convention or ordinary substring changed")
	}
}

func TestClaudeSubstringCommandIsUnrelated(t *testing.T) {
	root := t.TempDir()
	settings := []byte(`{"hooks":{"UserPromptSubmit":[{"matcher":"x","hooks":[{"type":"command","command":"echo Apply Personal Memory bundle unrelated"}]}]}}`)
	os.WriteFile(filepath.Join(root, "settings.json"), settings, 0o600)
	set := testSet(t, conformance.ClientClaude)
	if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "settings.json"))
	if !bytes.Contains(got, []byte("echo Apply Personal Memory bundle unrelated")) {
		t.Fatal("unrelated substring hook removed")
	}
}

func TestManifestOverrideMappingAndDrift(t *testing.T) {
	set := testSet(t, conformance.ClientCodex)
	if got, err := activeOverridePaths(set); err != nil || !reflect.DeepEqual(got, []string{"overrides/AGENTS.local.md"}) {
		t.Fatalf("mapping=%v", got)
	}
	set.OverridePaths = []string{"overrides/claude/wrong.md"}
	if got, err := activeOverridePaths(set); err == nil {
		t.Fatalf("manifest drift accepted: %v", got)
	}
	if _, err := Install(InstallOptions{TargetRoot: t.TempDir(), set: set}); err == nil {
		t.Fatal("installer accepted private artifact-set inventory drift")
	}
}

func TestPlanDryRunAndVerifyCreateNoEntries(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	infoBefore, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = PlanInstall(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	if _, err = Install(InstallOptions{TargetRoot: root, set: set, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(VerifyOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	infoAfter, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 || !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Fatalf("read-only operation changed target: entries=%v before=%v after=%v", entries, infoBefore.ModTime(), infoAfter.ModTime())
	}
}

func TestRollbackWaitsForLockAndHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	r, err := openValidatedRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	held, err := r.mutation.lock(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Rollback(RollbackOptions{TargetRoot: root, Client: set.ClientID, set: set, Context: ctx}); !errors.Is(err, context.Canceled) {
		t.Fatalf("rollback cancellation=%v", err)
	}
}

func TestRecoveryRestoresOnlyTransactionMutations(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	override := filepath.Join(root, "overrides", "AGENTS.local.md")
	if err := os.Remove(override); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(root, "AGENTS.md")
	userEdit := []byte("user edit outside transaction\n")
	_, err := Update(InstallOptions{TargetRoot: root, set: set, FailAfterWrites: 1, afterWrite: func(string) error {
		return os.WriteFile(agents, userEdit, 0o600)
	}})
	if err == nil {
		t.Fatal("injected failure succeeded")
	}
	got, readErr := os.ReadFile(agents)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, userEdit) {
		t.Fatalf("recovery overwrote unchanged path: %q", got)
	}
	if _, statErr := os.Stat(override); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("transaction-created override was not recovered: %v", statErr)
	}
}

func TestBackupRetentionIsBoundedAndCurrentRollbackSurvives(t *testing.T) {
	root := t.TempDir()
	set := testSet(t, conformance.ClientCodex)
	if _, err := Install(InstallOptions{TargetRoot: root, set: set}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < keepCompletedBackups+3; i++ {
		override := filepath.Join(root, "overrides", "AGENTS.local.md")
		os.Remove(override)
		if _, err := Update(InstallOptions{TargetRoot: root, set: set}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(stateDir(set.ClientID)+"/backups")))
	if err != nil {
		t.Fatal(err)
	}
	complete := 0
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(stateDir(set.ClientID)+"/backups/"+e.Name()+"/manifest.json"))); err == nil {
				complete++
			}
		}
	}
	if complete > keepCompletedBackups {
		t.Fatalf("backups=%d", complete)
	}
	if _, err := Rollback(RollbackOptions{TargetRoot: root, Client: set.ClientID, set: set}); err != nil {
		t.Fatalf("current rollback pruned: %v", err)
	}
}
