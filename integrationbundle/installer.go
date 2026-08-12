package integrationbundle

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

const installerStateSchema = 1
const codexBegin = "<!-- BEGIN PERSONAL_MEMORY_INTEGRATION v1 -->"
const codexEnd = "<!-- END PERSONAL_MEMORY_INTEGRATION v1 -->"
const claudeHookCommandIdentity = "Apply Personal Memory bundle "
const currentContractSHA256 = "b425bb25ea0ecaa85ddaafacb9e5af9970d11a985a482b8d8fabcc612fa69668"
const currentSuiteSHA256 = "bd10ce7d22054210ad6798b3a75f48379ccb8063a52d620677b67b36509fcd1f"
const keepCompletedBackups = 3

var errDiscoveryRequired = errors.New("tool discovery must be explicitly performed")

type InstallStatus string

const (
	StatusInstalled            InstallStatus = "installed"
	StatusDrifted              InstallStatus = "drifted"
	StatusMissing              InstallStatus = "missing"
	StatusIncompatible         InstallStatus = "incompatible"
	StatusManualActionRequired InstallStatus = "manual_action_required"
	StatusPlanned              InstallStatus = "planned"
)

type Discovery struct {
	Performed bool     `json:"performed"`
	Tools     []string `json:"tools,omitempty"`
}
type PlanAction struct {
	Path             string `json:"path"`
	Action           string `json:"action"`
	DigestSHA256     string `json:"digest_sha256,omitempty"`
	expectedDigest   string
	expectedExists   bool
	expectedIdentity fileIdentity
}
type fileIdentity struct {
	Dev   uint64
	Ino   uint64
	Size  int64
	Mtime int64
	Mode  fs.FileMode
}
type InstallPlan struct {
	Client                conformance.ClientFamily `json:"client"`
	TargetRoot            string                   `json:"target_root"`
	Actions               []PlanAction             `json:"actions"`
	NoChanges             bool                     `json:"no_changes"`
	stateExpectedDigest   string
	stateExpectedExists   bool
	stateExpectedIdentity fileIdentity
}
type Result struct {
	Status       InstallStatus            `json:"status"`
	Client       conformance.ClientFamily `json:"client"`
	Changed      bool                     `json:"changed"`
	Actions      []PlanAction             `json:"actions,omitempty"`
	Capabilities CapabilityConfig         `json:"capabilities"`
	MissingTools []string                 `json:"missing_tools,omitempty"`
	Warnings     []string                 `json:"warnings,omitempty"`
}
type InstallOptions struct {
	TargetRoot      string
	Bundle          *Bundle
	Client          conformance.ClientFamily
	Config          CapabilityConfig
	Discovery       Discovery
	Context         context.Context
	DryRun          bool
	FailAfterWrites int
	beforeApply     func(*rootContext) error
	afterWrite      func(string) error
	prune           func(*rootContext, conformance.ClientFamily, string) error
	failRecovery    bool
	set             ArtifactSet
	root            *rootContext
	lockHeld        bool
}
type VerifyOptions struct {
	TargetRoot string
	Bundle     *Bundle
	Client     conformance.ClientFamily
	Config     CapabilityConfig
	Discovery  Discovery
	Context    context.Context
	set        ArtifactSet
}
type RollbackOptions struct {
	TargetRoot      string
	Bundle          *Bundle
	Client          conformance.ClientFamily
	Config          CapabilityConfig
	Context         context.Context
	FailAfterWrites int
	prune           func(*rootContext, conformance.ClientFamily, string) error
	failRecovery    bool
	set             ArtifactSet
}

type artifactKind string

const (
	kindFile           artifactKind = "file"
	kindCodexBlock     artifactKind = "codex_managed_block"
	kindClaudeSettings artifactKind = "claude_settings_hook"
)

type activeArtifact struct {
	Path          string
	Content       []byte
	Kind          artifactKind
	ManagedDigest string
}
type ownedState struct {
	Path          string       `json:"path"`
	Kind          artifactKind `json:"kind"`
	ManagedDigest string       `json:"managed_digest"`
}
type overrideState struct {
	Path      string `json:"path"`
	UserOwned bool   `json:"user_owned"`
}
type installState struct {
	SchemaVersion          int                      `json:"schema_version"`
	Client                 conformance.ClientFamily `json:"client"`
	BundleVersion          string                   `json:"bundle_version"`
	ContractVersion        string                   `json:"contract_version"`
	ArtifactFormatVersion  string                   `json:"artifact_format_version"`
	CapabilityConfig       CapabilityConfig         `json:"capability_config"`
	CapabilityConfigSHA256 string                   `json:"capability_config_sha256"`
	Owned                  []ownedState             `json:"owned"`
	Overrides              []overrideState          `json:"overrides"`
	PreviousTransaction    string                   `json:"previous_transaction,omitempty"`
	DigestSHA256           string                   `json:"digest_sha256"`
	ContractSHA256         string                   `json:"contract_sha256"`
	SuiteSHA256            string                   `json:"suite_sha256"`
}
type backupFile struct {
	Path         string       `json:"path"`
	Kind         artifactKind `json:"kind"`
	Existed      bool         `json:"existed"`
	DigestSHA256 string       `json:"digest_sha256,omitempty"`
}
type backupManifest struct {
	SchemaVersion  int                      `json:"schema_version"`
	Client         conformance.ClientFamily `json:"client"`
	TargetIdentity string                   `json:"target_identity"`
	Files          []backupFile             `json:"files"`
	State          *installState            `json:"state,omitempty"`
	DigestSHA256   string                   `json:"digest_sha256"`
}
type rootContext struct {
	*os.Root
	mutation *mutationRoot
	identity string
}

type mutationCommittedError struct{ err error }

func (e *mutationCommittedError) Error() string { return e.err.Error() }
func (e *mutationCommittedError) Unwrap() error { return e.err }

func (r *rootContext) Close() error { return errors.Join(r.mutation.Close(), r.Root.Close()) }

func PlanInstall(o InstallOptions) (InstallPlan, error) { return plan(o, false) }
func PlanUpdate(o InstallOptions) (InstallPlan, error)  { return plan(o, true) }
func Install(o InstallOptions) (Result, error)          { return apply(o, false) }
func Update(o InstallOptions) (Result, error)           { return apply(o, true) }

func plan(o InstallOptions, update bool) (InstallPlan, error) {
	set, err := resolveInstallSet(o)
	if err != nil {
		return InstallPlan{}, err
	}
	r := o.root
	if r == nil {
		r, err = openValidatedRoot(o.TargetRoot)
		if err != nil {
			return InstallPlan{}, err
		}
		defer func() { _ = r.Close() }()
	}
	if !o.lockHeld {
		lock, lockErr := r.mutation.lock(o.Context, false)
		if lockErr != nil {
			return InstallPlan{}, lockErr
		}
		defer func() { _ = lock.Close() }()
	}
	if err = validateSet(set, o.Discovery); err != nil {
		return InstallPlan{}, err
	}
	active, err := activate(set)
	if err != nil {
		return InstallPlan{}, err
	}
	st, exists, err := readState(r, set.ClientID)
	if err != nil {
		return InstallPlan{}, err
	}
	if exists {
		if err = compatibleState(st, set.ClientID); err != nil {
			return InstallPlan{}, err
		}
		if err = verifyOwned(r, st); err != nil {
			return InstallPlan{}, err
		}
	} else if update {
		return InstallPlan{}, fmt.Errorf("installation state is missing")
	}
	if exists && !inventoryShapeMatches(st, active) {
		return InstallPlan{}, fmt.Errorf("installation ownership inventory is incompatible")
	}
	var actions []PlanAction
	for _, a := range active {
		current, exists, err := readOptional(r, a.Path)
		if err != nil {
			return InstallPlan{}, err
		}
		if !stExistsAndOwned(exists, st, a.Path) {
			if a.Kind == kindFile && exists {
				return InstallPlan{}, fmt.Errorf("unowned destination conflict at %q", a.Path)
			}
			if a.Kind == kindCodexBlock && exists && (len(markerLines(current, codexBegin)) > 0 || len(markerLines(current, codexEnd)) > 0) {
				return InstallPlan{}, fmt.Errorf("unowned managed marker conflict at %q", a.Path)
			}
			if a.Kind == kindClaudeSettings && exists && containsOwnedClaudeHook(current) {
				return InstallPlan{}, fmt.Errorf("unowned managed hook conflict at %q", a.Path)
			}
		}
		desired, err := mergeActive(a, current, exists)
		if err != nil {
			return InstallPlan{}, err
		}
		verb := "create"
		if exists {
			verb = "update"
		}
		if exists && bytes.Equal(current, desired) {
			verb = "unchanged"
		}
		id, idErr := identityOptional(r, a.Path, exists)
		if idErr != nil {
			return InstallPlan{}, idErr
		}
		actions = append(actions, PlanAction{Path: a.Path, Action: verb, DigestSHA256: digest(desired), expectedDigest: digest(current), expectedExists: exists, expectedIdentity: id})
	}
	overridePaths, err := activeOverridePaths(set)
	if err != nil {
		return InstallPlan{}, err
	}
	for _, p := range overridePaths {
		current, exists, err := readOptional(r, p)
		if err != nil {
			return InstallPlan{}, err
		}
		verb := "create_override"
		if exists {
			verb = "unchanged"
		}
		id, idErr := identityOptional(r, p, exists)
		if idErr != nil {
			return InstallPlan{}, idErr
		}
		actions = append(actions, PlanAction{Path: p, Action: verb, DigestSHA256: digest(overrideStub(p)), expectedDigest: digest(current), expectedExists: exists, expectedIdentity: id})
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].Path < actions[j].Path })
	no := true
	for _, a := range actions {
		if a.Action != "unchanged" {
			no = false
		}
	}
	stateBytes, stateExists, stateErr := readOptional(r, statePath(set.ClientID))
	if stateErr != nil {
		return InstallPlan{}, stateErr
	}
	stateID, idErr := identityOptional(r, statePath(set.ClientID), stateExists)
	if idErr != nil {
		return InstallPlan{}, idErr
	}
	return InstallPlan{Client: set.ClientID, TargetRoot: filepath.Clean(o.TargetRoot), Actions: actions, NoChanges: no, stateExpectedExists: stateExists, stateExpectedDigest: digest(stateBytes), stateExpectedIdentity: stateID}, nil
}

func apply(o InstallOptions, update bool) (Result, error) {
	set, err := resolveInstallSet(o)
	if err != nil {
		return Result{}, err
	}
	r, err := openValidatedRoot(o.TargetRoot)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = r.Close() }()
	lock, err := r.mutation.lock(o.Context, true)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = lock.Close() }()
	o.root = r
	o.lockHeld = true
	p, err := plan(o, update)
	if err != nil {
		return Result{}, err
	}
	status := StatusInstalled
	if set.ClientID == conformance.ClientChatGPT {
		status = StatusManualActionRequired
	}
	res := Result{Status: status, Client: set.ClientID, Changed: !p.NoChanges, Actions: p.Actions, Capabilities: set.CapabilityConfig}
	if o.DryRun || p.NoChanges {
		if o.DryRun {
			res.Status = StatusPlanned
		}
		return res, nil
	}
	if o.beforeApply != nil {
		if err = o.beforeApply(r); err != nil {
			return Result{}, err
		}
	}
	if err = revalidate(r, PlanAction{Path: statePath(set.ClientID), expectedExists: p.stateExpectedExists, expectedDigest: p.stateExpectedDigest, expectedIdentity: p.stateExpectedIdentity}); err != nil {
		return Result{}, fmt.Errorf("installation state changed after planning: %w", err)
	}
	active, _ := activate(set)
	old, oldExists, err := readState(r, set.ClientID)
	if err != nil {
		return Result{}, err
	}
	tx, bm, err := createBackup(r, set.ClientID, active, old, oldExists)
	if err != nil {
		return Result{}, err
	}
	var createdOverrides []string
	writes := 0
	postWrite := map[string]PlanAction{}
	stateWriteAttempted := false
	intendedStateDigest := ""
	recoverApply := func(cause error) (Result, error) {
		if writes == 0 {
			return Result{}, cause
		}
		var casErr error
		for _, expected := range postWrite {
			casErr = errors.Join(casErr, revalidate(r, expected))
		}
		if casErr != nil {
			return Result{}, errors.Join(cause, fmt.Errorf("automatic recovery refused changed transaction output: %w", casErr))
		}
		if stateWriteAttempted {
			stateData, stateExists, stateErr := readOptional(r, statePath(set.ClientID))
			if stateErr != nil {
				return Result{}, errors.Join(cause, fmt.Errorf("automatic recovery could not inspect state: %w", stateErr))
			}
			stateDigestNow := digest(stateData)
			if stateExists != p.stateExpectedExists || stateDigestNow != p.stateExpectedDigest {
				if !stateExists || stateDigestNow != intendedStateDigest {
					return Result{}, errors.Join(cause, fmt.Errorf("automatic recovery refused changed installation state"))
				}
			}
		}
		var cleanupErr error
		for _, p0 := range createdOverrides {
			data, ok, e := readOptional(r, p0)
			if e != nil {
				cleanupErr = errors.Join(cleanupErr, e)
				continue
			}
			if ok && bytes.Equal(data, overrideStub(p0)) {
				cleanupErr = errors.Join(cleanupErr, removeRelative(r.mutation, p0))
			}
		}
		recoveryManifest := bm
		recoveryManifest.Files = nil
		for _, f := range bm.Files {
			if _, mutated := postWrite[f.Path]; mutated {
				recoveryManifest.Files = append(recoveryManifest.Files, f)
			}
		}
		re := restoreBackup(r, tx, recoveryManifest, 0, false, stateWriteAttempted)
		re = errors.Join(cleanupErr, re)
		if o.failRecovery {
			re = errors.Join(re, fmt.Errorf("injected recovery failure"))
		}
		if re != nil {
			return Result{}, errors.Join(cause, fmt.Errorf("automatic recovery failed: %w", re))
		}
		return Result{}, cause
	}
	recordCommittedWrite := func(path string) error {
		writes++
		snapshot, snapshotErr := snapshotAction(r, path)
		if snapshotErr != nil {
			return fmt.Errorf("record committed write identity: %w", snapshotErr)
		}
		postWrite[path] = snapshot
		return nil
	}
	for _, a := range active {
		pa := findAction(p.Actions, a.Path)
		if err = revalidate(r, pa); err != nil {
			return recoverApply(err)
		}
		current, exists, err := readOptional(r, a.Path)
		if err != nil {
			return recoverApply(err)
		}
		desired, err := mergeActive(a, current, exists)
		if err != nil {
			return recoverApply(err)
		}
		if !bytes.Equal(current, desired) {
			if err = atomicWriteRoot(r, a.Path, desired, 0o600); err != nil {
				var committed *mutationCommittedError
				if errors.As(err, &committed) {
					if recordErr := recordCommittedWrite(a.Path); recordErr != nil {
						return recoverApply(errors.Join(err, recordErr))
					}
				}
				return recoverApply(err)
			}
			if err = recordCommittedWrite(a.Path); err != nil {
				return recoverApply(err)
			}
			if o.afterWrite != nil {
				if err = o.afterWrite(a.Path); err != nil {
					return recoverApply(err)
				}
			}
			if o.FailAfterWrites > 0 && writes >= o.FailAfterWrites {
				return recoverApply(fmt.Errorf("injected apply failure"))
			}
		}
	}
	overridePaths, err := activeOverridePaths(set)
	if err != nil {
		return recoverApply(err)
	}
	for _, p0 := range overridePaths {
		pa := findAction(p.Actions, p0)
		if pa.Action == "create_override" {
			if err = revalidate(r, pa); err != nil {
				return recoverApply(err)
			}
			if err = atomicWriteRoot(r, p0, overrideStub(p0), 0o600); err != nil {
				var committed *mutationCommittedError
				if errors.As(err, &committed) {
					createdOverrides = append(createdOverrides, p0)
					if recordErr := recordCommittedWrite(p0); recordErr != nil {
						return recoverApply(errors.Join(err, recordErr))
					}
				}
				return recoverApply(err)
			}
			createdOverrides = append(createdOverrides, p0)
			if err = recordCommittedWrite(p0); err != nil {
				return recoverApply(err)
			}
			if o.afterWrite != nil {
				if err = o.afterWrite(p0); err != nil {
					return recoverApply(err)
				}
			}
			if o.FailAfterWrites > 0 && writes >= o.FailAfterWrites {
				return recoverApply(fmt.Errorf("injected apply failure"))
			}
		}
	}
	for _, a := range active {
		data, ok, e := readOptional(r, a.Path)
		if e != nil || !ok {
			return recoverApply(fmt.Errorf("final active output validation failed"))
		}
		managed := data
		switch a.Kind {
		case kindCodexBlock:
			managed, e = codexManaged(data)
		case kindClaudeSettings:
			managed, e = claudeManaged(data)
		}
		if e != nil || managedDigest(a.Kind, managed) != a.ManagedDigest {
			return recoverApply(fmt.Errorf("final active output validation failed"))
		}
	}
	for _, p0 := range overridePaths {
		if _, ok, e := readOptional(r, p0); e != nil || !ok {
			return recoverApply(fmt.Errorf("final override validation failed"))
		}
	}
	st := stateFor(set, active, tx)
	stateBytes, _ := json.Marshal(st)
	intendedStateDigest = digest(append(stateBytes, '\n'))
	stateWriteAttempted = true
	if err = writeState(r, st); err != nil {
		return recoverApply(err)
	}
	prune := pruneBackups
	if o.prune != nil {
		prune = o.prune
	}
	if err = prune(r, set.ClientID, tx); err != nil {
		res.Warnings = append(res.Warnings, "backup_retention_failed")
	}
	return res, nil
}

func Verify(o VerifyOptions) (Result, error) {
	set, resolveErr := resolveVerifySet(o)
	r, err := openValidatedRoot(o.TargetRoot)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = r.Close() }()
	lock, err := r.mutation.lock(o.Context, false)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = lock.Close() }()
	res := Result{Status: StatusMissing, Client: o.Client, Capabilities: o.Config}
	if resolveErr != nil {
		res.Status = StatusIncompatible
		return res, nil
	}
	res.Client = set.ClientID
	res.Capabilities = set.CapabilityConfig
	if err = validateSetStructure(set); err != nil {
		res.Status = StatusIncompatible
		return res, nil
	}
	st, exists, err := readState(r, set.ClientID)
	if err != nil {
		res.Status = StatusIncompatible
		return res, nil
	}
	if !exists {
		return res, nil
	}
	if err = validateInstalledState(r, st, set, true); err != nil {
		if errors.Is(err, errOverrideMissing) {
			res.Status = StatusDrifted
		} else {
			res.Status = StatusIncompatible
		}
		return res, nil
	}
	if err = verifyOwned(r, st); err != nil {
		res.Status = StatusDrifted
		return res, nil
	}
	if anyAvailable(set.CapabilityConfig) && !o.Discovery.Performed {
		res.Status = StatusDrifted
		return res, nil
	}
	res.MissingTools = missingTools(set.CapabilityConfig, o.Discovery.Tools)
	sort.Strings(res.MissingTools)
	if len(res.MissingTools) > 0 {
		res.Status = StatusDrifted
		return res, nil
	}
	res.Status = StatusInstalled
	if set.ClientID == conformance.ClientChatGPT {
		res.Status = StatusManualActionRequired
	}
	return res, nil
}

func Rollback(o RollbackOptions) (Result, error) {
	set := o.set
	var err error
	if set.ClientID == "" {
		set, err = renderClient(o.Bundle, o.Client, o.Config)
		if err != nil {
			return Result{}, err
		}
	}
	r, err := openValidatedRoot(o.TargetRoot)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = r.Close() }()
	lock, err := r.mutation.lock(o.Context, true)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = lock.Close() }()
	st, exists, err := readState(r, o.Client)
	if err != nil || !exists {
		return Result{}, fmt.Errorf("verified installation state is required")
	}
	if err = validateInstalledState(r, st, set, true); err != nil {
		return Result{}, err
	}
	if err = verifyOwned(r, st); err != nil {
		return Result{}, err
	}
	if !safeToken(st.PreviousTransaction) {
		return Result{}, fmt.Errorf("previous transaction is unavailable")
	}
	bm, err := loadBackup(r, o.Client, st.PreviousTransaction, set, o.Bundle)
	if err != nil {
		return Result{}, err
	}
	if bm.Client != o.Client || bm.TargetIdentity != r.identity {
		return Result{}, fmt.Errorf("backup identity mismatch")
	}
	if bm.State != nil {
		if err = compatibleState(*bm.State, o.Client); err != nil {
			return Result{}, fmt.Errorf("incompatible prior state: %w", err)
		}
	}
	current := activeFromState(st)
	recoveryTx, recovery, err := createBackup(r, o.Client, current, st, true)
	if err != nil {
		return Result{}, err
	}
	if err = restoreBackup(r, st.PreviousTransaction, bm, o.FailAfterWrites, true, true); err != nil {
		re := restoreBackup(r, recoveryTx, recovery, 0, false, true)
		if o.failRecovery {
			re = errors.Join(re, fmt.Errorf("injected recovery failure"))
		}
		if re != nil {
			return Result{}, errors.Join(err, fmt.Errorf("automatic recovery failed: %w", re))
		}
		if pruneErr := pruneBackups(r, o.Client, st.PreviousTransaction); pruneErr != nil {
			return Result{}, errors.Join(err, fmt.Errorf("backup retention after recovery: %w", pruneErr))
		}
		return Result{}, err
	}
	referenced := ""
	if bm.State != nil {
		referenced = bm.State.PreviousTransaction
	}
	status := StatusInstalled
	if o.Client == conformance.ClientChatGPT {
		status = StatusManualActionRequired
	}
	res := Result{Status: status, Client: o.Client, Changed: true}
	prune := pruneBackups
	if o.prune != nil {
		prune = o.prune
	}
	if err = prune(r, o.Client, referenced); err != nil {
		res.Warnings = append(res.Warnings, "backup_retention_failed")
	}
	return res, nil
}

func WriteRendered(root string, bundle *Bundle, client conformance.ClientFamily, config CapabilityConfig) (Result, error) {
	set, err := renderClient(bundle, client, config)
	if err != nil {
		return Result{}, err
	}
	r, err := openValidatedRoot(root)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = r.Close() }()
	if err = validateSet(set, Discovery{}); err != nil && !errors.Is(err, errDiscoveryRequired) {
		return Result{}, err
	}
	for _, a := range set.Artifacts {
		if _, ok, e := readOptional(r, a.Path); e != nil {
			return Result{}, e
		} else if ok {
			return Result{}, fmt.Errorf("render destination exists")
		}
	}
	for _, a := range set.Artifacts {
		if err = atomicWriteRoot(r, a.Path, a.Content, 0o600); err != nil {
			return Result{}, err
		}
	}
	return Result{Status: StatusMissing, Client: set.ClientID, Changed: true, Capabilities: set.CapabilityConfig}, nil
}

func activate(set ArtifactSet) ([]activeArtifact, error) {
	by := map[string]Artifact{}
	for _, a := range set.Artifacts {
		by[a.Path] = a
	}
	pick := func(p string) (Artifact, error) {
		a, ok := by[p]
		if !ok {
			return Artifact{}, fmt.Errorf("required source artifact missing")
		}
		return a, nil
	}
	var out []activeArtifact
	switch set.ClientID {
	case conformance.ClientCodex:
		agents, e := pick("codex/AGENTS.personal-memory.md")
		if e != nil {
			return nil, e
		}
		skill, e := pick("codex/skills/personal-memory/SKILL.md")
		if e != nil {
			return nil, e
		}
		block := []byte(codexBegin + "\n" + string(agents.Content) + codexEnd + "\n")
		out = []activeArtifact{{"AGENTS.md", block, kindCodexBlock, digest(bytes.TrimSuffix(block, []byte("\n")))}, {"skills/personal-memory/SKILL.md", skill.Content, kindFile, skill.DigestSHA256}}
	case conformance.ClientClaude:
		rules, e := pick("claude/rules/personal-memory.md")
		if e != nil {
			return nil, e
		}
		skill, e := pick("claude/skills/personal-memory/SKILL.md")
		if e != nil {
			return nil, e
		}
		settings, e := pick("claude/settings.personal-memory.json")
		if e != nil {
			return nil, e
		}
		hook, err := extractOwnedHook(settings.Content)
		if err != nil {
			return nil, err
		}
		out = []activeArtifact{{"rules/personal-memory.md", rules.Content, kindFile, rules.DigestSHA256}, {"skills/personal-memory/SKILL.md", skill.Content, kindFile, skill.DigestSHA256}, {"settings.json", hook, kindClaudeSettings, digest(hook)}}
	default:
		for _, a := range set.Artifacts {
			out = append(out, activeArtifact{a.Path, a.Content, kindFile, a.DigestSHA256})
		}
	}
	return out, nil
}

func mergeActive(a activeArtifact, current []byte, exists bool) ([]byte, error) {
	switch a.Kind {
	case kindFile:
		return append([]byte(nil), a.Content...), nil
	case kindCodexBlock:
		return mergeCodex(current, a.Content)
	case kindClaudeSettings:
		return mergeClaude(current, a.Content)
	}
	return nil, fmt.Errorf("unknown artifact kind")
}
func mergeCodex(current, block []byte) ([]byte, error) {
	begins, ends := markerLines(current, codexBegin), markerLines(current, codexEnd)
	if len(begins) != len(ends) || len(begins) > 1 {
		return nil, fmt.Errorf("invalid Codex managed markers")
	}
	if len(begins) == 1 {
		b := begins[0][0]
		e := ends[0][0]
		if e < b {
			return nil, fmt.Errorf("invalid Codex managed markers")
		}
		e = ends[0][1]
		if e < len(current) && current[e] == '\r' {
			e++
		}
		if e < len(current) && current[e] == '\n' {
			e++
		}
		block = codexLineEnding(block, current)
		return append(append([]byte(nil), current[:b]...), append(block, current[e:]...)...), nil
	}
	out := append([]byte(nil), current...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, codexLineEnding(block, current)...), nil
}
func codexManaged(content []byte) ([]byte, error) {
	begins, ends := markerLines(content, codexBegin), markerLines(content, codexEnd)
	if len(begins) != 1 || len(ends) != 1 {
		return nil, fmt.Errorf("missing or duplicated Codex managed block")
	}
	b := begins[0][0]
	e := ends[0][0]
	if e < b {
		return nil, fmt.Errorf("reordered Codex markers")
	}
	return content[b : e+len(codexEnd)], nil
}
func markerLines(content []byte, marker string) [][2]int {
	var out [][2]int
	start := 0
	for start <= len(content) {
		end := bytes.IndexByte(content[start:], '\n')
		lineEnd := len(content)
		if end >= 0 {
			lineEnd = start + end
		}
		line := content[start:lineEnd]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if bytes.Equal(line, []byte(marker)) {
			out = append(out, [2]int{start, start + len(marker)})
		}
		if end < 0 {
			break
		}
		start = lineEnd + 1
	}
	return out
}
func codexLineEnding(block, current []byte) []byte {
	if bytes.Contains(current, []byte("\r\n")) && !bytes.Contains(bytes.ReplaceAll(current, []byte("\r\n"), nil), []byte("\n")) {
		return bytes.ReplaceAll(block, []byte("\n"), []byte("\r\n"))
	}
	return block
}

func decodeSettings(data []byte) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid Claude settings")
	}
	return m, nil
}
func extractOwnedHook(data []byte) ([]byte, error) {
	m, err := decodeSettings(data)
	if err != nil {
		return nil, err
	}
	hooks := m["hooks"]
	if len(hooks) == 0 {
		return nil, fmt.Errorf("missing Claude hook")
	}
	var hm map[string][]json.RawMessage
	if err = json.Unmarshal(hooks, &hm); err != nil {
		return nil, err
	}
	entries := hm["UserPromptSubmit"]
	if len(entries) != 1 {
		return nil, fmt.Errorf("multiple Claude owned hooks are not allowed")
	}
	canonical, err := canonicalJSON(entries[0])
	if err != nil {
		return nil, err
	}
	if !ownedClaudeHook(canonical) {
		return nil, fmt.Errorf("missing Claude hook identity")
	}
	return canonical, nil
}
func mergeClaude(current, hook []byte) ([]byte, error) {
	m, err := decodeSettings(current)
	if err != nil {
		return nil, err
	}
	hm := map[string][]json.RawMessage{}
	if raw := m["hooks"]; len(raw) > 0 {
		if err = json.Unmarshal(raw, &hm); err != nil {
			return nil, fmt.Errorf("invalid Claude hooks")
		}
	}
	var kept []json.RawMessage
	found := 0
	for _, entry := range hm["UserPromptSubmit"] {
		canonical, _ := canonicalJSON(entry)
		if ownedClaudeHook(canonical) {
			found++
			continue
		}
		kept = append(kept, entry)
	}
	if found > 1 {
		return nil, fmt.Errorf("duplicate Claude owned hooks")
	}
	kept = append(kept, json.RawMessage(hook))
	hm["UserPromptSubmit"] = kept
	raw, _ := json.Marshal(hm)
	m["hooks"] = raw
	out, _ := json.MarshalIndent(m, "", "  ")
	return append(out, '\n'), nil
}
func claudeManaged(content []byte) ([]byte, error) {
	m, err := decodeSettings(content)
	if err != nil {
		return nil, err
	}
	var hm map[string][]json.RawMessage
	if err = json.Unmarshal(m["hooks"], &hm); err != nil {
		return nil, fmt.Errorf("missing Claude hooks")
	}
	var found []byte
	for _, e := range hm["UserPromptSubmit"] {
		c, _ := canonicalJSON(e)
		if ownedClaudeHook(c) {
			if found != nil {
				return nil, fmt.Errorf("duplicate Claude owned hooks")
			}
			found = c
		}
	}
	if found == nil {
		return nil, fmt.Errorf("missing Claude owned hook")
	}
	return found, nil
}
func ownedClaudeHook(data []byte) bool {
	var entry struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if json.Unmarshal(data, &entry) != nil || entry.Matcher != "" || len(entry.Hooks) != 1 || entry.Hooks[0].Type != "command" {
		return false
	}
	return strings.HasPrefix(entry.Hooks[0].Command, "printf '%s\\n' '{\"hookSpecificOutput\":{\"hookEventName\":\"UserPromptSubmit\",\"additionalContext\":\""+claudeHookCommandIdentity) && strings.HasSuffix(entry.Hooks[0].Command, "\"}}'")
}
func containsOwnedClaudeHook(data []byte) bool {
	m, err := decodeSettings(data)
	if err != nil {
		return false
	}
	var hm map[string][]json.RawMessage
	if json.Unmarshal(m["hooks"], &hm) != nil {
		return false
	}
	for _, e := range hm["UserPromptSubmit"] {
		c, _ := canonicalJSON(e)
		if ownedClaudeHook(c) {
			return true
		}
	}
	return false
}
func canonicalJSON(data []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func validateSet(set ArtifactSet, d Discovery) error {
	if err := validateSetStructure(set); err != nil {
		return err
	}
	if anyAvailable(set.CapabilityConfig) && !d.Performed {
		return errDiscoveryRequired
	}
	if d.Performed && len(missingTools(set.CapabilityConfig, d.Tools)) > 0 {
		return fmt.Errorf("required discovered tools are missing")
	}
	return nil
}
func validateSetStructure(set ArtifactSet) error {
	formatVersion, supported := artifactFormatVersion(set.ClientID)
	if !supported || set.BundleVersion != BundleVersion || set.ContractVersion != ContractVersion || set.ArtifactFormatVersion != formatVersion || set.SourceIdentity.ContractSHA256 != currentContractSHA256 || set.SourceIdentity.ConformanceSuiteSHA256 != currentSuiteSHA256 || artifactSetDigest(set.Artifacts) != set.DigestSHA256 || capabilityConfigDigest(set.CapabilityConfig) != set.CapabilityConfigSHA256 {
		return fmt.Errorf("invalid artifact set")
	}
	want := canonicalClientInventories[set.ClientID]
	if len(set.Artifacts) != len(want.Artifacts) || !reflect.DeepEqual(set.OverridePaths, want.OverridePaths) {
		return fmt.Errorf("invalid artifact set inventory")
	}
	for index, artifact := range set.Artifacts {
		if artifact.Path != want.Artifacts[index].Path || artifact.DigestSHA256 != digest(artifact.Content) {
			return fmt.Errorf("invalid artifact set inventory")
		}
	}
	if _, err := activeOverridePaths(set); err != nil {
		return fmt.Errorf("invalid artifact set inventory: %w", err)
	}
	return nil
}
func renderClient(b *Bundle, c conformance.ClientFamily, config CapabilityConfig) (ArtifactSet, error) {
	if b == nil {
		return ArtifactSet{}, fmt.Errorf("validated bundle is required")
	}
	sets, err := b.Render(config)
	if err != nil {
		return ArtifactSet{}, err
	}
	for _, set := range sets {
		if set.ClientID == c {
			return set, nil
		}
	}
	return ArtifactSet{}, fmt.Errorf("unsupported client")
}
func resolveInstallSet(o InstallOptions) (ArtifactSet, error) {
	if o.set.ClientID != "" {
		return o.set, nil
	}
	return renderClient(o.Bundle, o.Client, o.Config)
}
func resolveVerifySet(o VerifyOptions) (ArtifactSet, error) {
	if o.set.ClientID != "" {
		return o.set, nil
	}
	return renderClient(o.Bundle, o.Client, o.Config)
}
func stExistsAndOwned(exists bool, st installState, p string) bool {
	if !exists {
		return false
	}
	for _, o := range st.Owned {
		if o.Path == p {
			return true
		}
	}
	return false
}
func inventoryMatches(st installState, active []activeArtifact) bool {
	if len(st.Owned) != len(active) {
		return false
	}
	want := map[string]activeArtifact{}
	for _, a := range active {
		want[a.Path] = a
	}
	seen := map[string]bool{}
	for _, o := range st.Owned {
		a, ok := want[o.Path]
		if !ok || seen[o.Path] || a.Kind != o.Kind || a.ManagedDigest != o.ManagedDigest {
			return false
		}
		seen[o.Path] = true
	}
	return len(seen) == len(want)
}
func inventoryShapeMatches(st installState, active []activeArtifact) bool {
	if len(st.Owned) != len(active) {
		return false
	}
	want := map[string]artifactKind{}
	for _, a := range active {
		want[a.Path] = a.Kind
	}
	seen := map[string]bool{}
	for _, o := range st.Owned {
		if seen[o.Path] || want[o.Path] != o.Kind {
			return false
		}
		seen[o.Path] = true
	}
	return len(seen) == len(want)
}
func anyAvailable(c CapabilityConfig) bool {
	return c.Memory == CapabilityAvailable || c.Documents == CapabilityAvailable || c.Todoist == CapabilityAvailable
}
func missingTools(c CapabilityConfig, got []string) []string {
	have := map[string]bool{}
	for _, x := range got {
		have[x] = true
	}
	var req []string
	if c.Memory == CapabilityAvailable {
		req = append(req, "recall_facts", "store_fact", "update_fact", "set_fact_lifecycle")
	}
	if c.Documents == CapabilityAvailable {
		req = append(req, "search_documents")
	}
	if c.Todoist == CapabilityAvailable {
		req = append(req, "get_tasks", "create_task", "update_task", "complete_task", "delete_task")
	}
	var out []string
	for _, x := range req {
		if !have[x] {
			out = append(out, x)
		}
	}
	return out
}

func stateFor(set ArtifactSet, active []activeArtifact, tx string) installState {
	st := installState{SchemaVersion: installerStateSchema, Client: set.ClientID, BundleVersion: set.BundleVersion, ContractVersion: set.ContractVersion, ArtifactFormatVersion: set.ArtifactFormatVersion, CapabilityConfig: set.CapabilityConfig, CapabilityConfigSHA256: set.CapabilityConfigSHA256, PreviousTransaction: tx, ContractSHA256: set.SourceIdentity.ContractSHA256, SuiteSHA256: set.SourceIdentity.ConformanceSuiteSHA256}
	for _, a := range active {
		st.Owned = append(st.Owned, ownedState{a.Path, a.Kind, a.ManagedDigest})
	}
	overridePaths, _ := activeOverridePaths(set)
	for _, p := range overridePaths {
		st.Overrides = append(st.Overrides, overrideState{p, true})
	}
	st.DigestSHA256 = stateDigest(st)
	return st
}
func stateDigest(st installState) string {
	st.DigestSHA256 = ""
	b, _ := json.Marshal(st)
	return digest(b)
}
func compatibleState(st installState, c conformance.ClientFamily) error {
	formatVersion, supported := artifactFormatVersion(c)
	if st.SchemaVersion != installerStateSchema || st.Client != c || !supported || st.BundleVersion != BundleVersion || st.ContractVersion != ContractVersion || st.ArtifactFormatVersion != formatVersion || st.ContractSHA256 != currentContractSHA256 || st.SuiteSHA256 != currentSuiteSHA256 || !validConfigState(st.CapabilityConfig.Memory) || !validConfigState(st.CapabilityConfig.Documents) || !validConfigState(st.CapabilityConfig.Todoist) || st.CapabilityConfigSHA256 != capabilityConfigDigest(st.CapabilityConfig) || st.DigestSHA256 != stateDigest(st) || len(st.Owned) < 1 || len(st.Owned) > 16 {
		return fmt.Errorf("incompatible installation state")
	}
	seenOwned := map[string]bool{}
	for _, o := range st.Owned {
		if seenOwned[o.Path] || !installerSafePath(o.Path) || (o.Kind != kindFile && o.Kind != kindCodexBlock && o.Kind != kindClaudeSettings) || len(o.ManagedDigest) != 64 {
			return fmt.Errorf("incompatible installation state")
		}
		seenOwned[o.Path] = true
	}
	return nil
}

var errOverrideMissing = errors.New("required user override is missing")

func validateInstalledState(r *rootContext, st installState, set ArtifactSet, requireOverrides bool) error {
	if err := validateSetStructure(set); err != nil {
		return err
	}
	if err := compatibleState(st, set.ClientID); err != nil {
		return err
	}
	active, err := activate(set)
	if err != nil {
		return err
	}
	if !inventoryMatches(st, active) || st.ContractSHA256 != set.SourceIdentity.ContractSHA256 || st.SuiteSHA256 != set.SourceIdentity.ConformanceSuiteSHA256 || st.BundleVersion != set.BundleVersion || st.ContractVersion != set.ContractVersion || st.ArtifactFormatVersion != set.ArtifactFormatVersion || st.CapabilityConfig != set.CapabilityConfig || st.CapabilityConfigSHA256 != set.CapabilityConfigSHA256 {
		return fmt.Errorf("installation state does not match authoritative bundle")
	}
	wantOverrides, err := activeOverridePaths(set)
	if err != nil {
		return err
	}
	if len(st.Overrides) != len(wantOverrides) {
		return fmt.Errorf("override inventory is incompatible")
	}
	seen := map[string]bool{}
	for _, o := range st.Overrides {
		if !o.UserOwned || seen[o.Path] {
			return fmt.Errorf("override inventory is incompatible")
		}
		seen[o.Path] = true
	}
	for _, p := range wantOverrides {
		if !seen[p] {
			return fmt.Errorf("override inventory is incompatible")
		}
		if requireOverrides {
			if _, ok, e := readOptional(r, p); e != nil {
				return e
			} else if !ok {
				return errOverrideMissing
			}
		}
	}
	return nil
}
func verifyOwned(r *rootContext, st installState) error {
	for _, o := range st.Owned {
		data, err := readRoot(r, o.Path)
		if err != nil {
			return fmt.Errorf("owned artifact conflict at %q", o.Path)
		}
		managed := data
		switch o.Kind {
		case kindCodexBlock:
			managed, err = codexManaged(data)
		case kindClaudeSettings:
			managed, err = claudeManaged(data)
		}
		if err != nil || managedDigest(o.Kind, managed) != o.ManagedDigest {
			return fmt.Errorf("owned artifact conflict at %q", o.Path)
		}
	}
	return nil
}
func readState(r *rootContext, c conformance.ClientFamily) (installState, bool, error) {
	data, ok, err := readOptional(r, statePath(c))
	if err != nil || !ok {
		return installState{}, ok, err
	}
	var st installState
	if err = decodeStrict(data, &st); err != nil {
		return st, false, err
	}
	return st, true, nil
}
func writeState(r *rootContext, st installState) error {
	st.DigestSHA256 = stateDigest(st)
	b, _ := json.Marshal(st)
	return atomicWriteRoot(r, statePath(st.Client), append(b, '\n'), 0o600)
}
func stateDir(c conformance.ClientFamily) string  { return ".personal-memory-integration/" + string(c) }
func statePath(c conformance.ClientFamily) string { return stateDir(c) + "/state.json" }
func activeFromState(st installState) []activeArtifact {
	var out []activeArtifact
	for _, o := range st.Owned {
		out = append(out, activeArtifact{Path: o.Path, Kind: o.Kind, ManagedDigest: o.ManagedDigest})
	}
	return out
}

func createBackup(r *rootContext, c conformance.ClientFamily, active []activeArtifact, old installState, oldExists bool) (string, backupManifest, error) {
	var nonce [4]byte
	_, _ = rand.Read(nonce[:])
	tx := fmt.Sprintf("%d-%x", time.Now().UTC().UnixNano(), nonce[:])
	m := backupManifest{SchemaVersion: 1, Client: c, TargetIdentity: r.identity}
	if oldExists {
		x := old
		m.State = &x
	}
	for _, a := range active {
		data, ok, err := readOptional(r, a.Path)
		if err != nil {
			return "", m, err
		}
		f := backupFile{Path: a.Path, Kind: a.Kind, Existed: ok}
		if ok {
			f.DigestSHA256 = digest(data)
			if err = atomicWriteRoot(r, backupFilePath(c, tx, a.Path), data, 0o600); err != nil {
				return "", m, err
			}
		}
		m.Files = append(m.Files, f)
	}
	m.DigestSHA256 = backupDigest(m)
	b, _ := json.Marshal(m)
	if err := atomicWriteRoot(r, stateDir(c)+"/backups/"+tx+"/manifest.json", append(b, '\n'), 0o600); err != nil {
		return "", m, err
	}
	return tx, m, nil
}
func backupFilePath(c conformance.ClientFamily, tx, p string) string {
	return stateDir(c) + "/backups/" + tx + "/files/" + p
}
func pruneBackups(r *rootContext, c conformance.ClientFamily, current string) error {
	base := stateDir(c) + "/backups"
	entries, err := fs.ReadDir(r.FS(), base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var complete []string
	for _, e := range entries {
		if !e.IsDir() || !safeToken(e.Name()) {
			continue
		}
		if _, readErr := readRoot(r, base+"/"+e.Name()+"/manifest.json"); readErr == nil {
			complete = append(complete, e.Name())
		} else if errors.Is(readErr, fs.ErrNotExist) {
			if removeErr := removeTreeRelative(r, e.Name(), base); removeErr != nil {
				return removeErr
			}
		}
	}
	sort.Strings(complete)
	for len(complete) > keepCompletedBackups {
		index := 0
		for index < len(complete) && complete[index] == current {
			index++
		}
		if index == len(complete) {
			break
		}
		name := complete[index]
		complete = append(complete[:index], complete[index+1:]...)
		if err := removeTreeRelative(r, name, base); err != nil {
			return err
		}
	}
	return nil
}
func removeTreeRelative(r *rootContext, name, base string) error {
	root := base + "/" + name
	var paths []string
	err := fs.WalkDir(r.FS(), root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		paths = append(paths, p)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	sort.Slice(paths, func(i, j int) bool { return strings.Count(paths[i], "/") > strings.Count(paths[j], "/") })
	for _, p := range paths {
		if err = removeAnyRelative(r.mutation, p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
func backupDigest(m backupManifest) string {
	m.DigestSHA256 = ""
	b, _ := json.Marshal(m)
	return digest(b)
}
func loadBackup(r *rootContext, c conformance.ClientFamily, tx string, set ArtifactSet, bundle *Bundle) (backupManifest, error) {
	var m backupManifest
	b, err := readRoot(r, stateDir(c)+"/backups/"+tx+"/manifest.json")
	if err != nil {
		return m, err
	}
	if err = decodeStrict(b, &m); err != nil || m.SchemaVersion != 1 || m.DigestSHA256 != backupDigest(m) || len(m.Files) < 1 || len(m.Files) > 16 {
		return m, fmt.Errorf("backup manifest invalid or tampered")
	}
	active, activationErr := activate(set)
	if activationErr != nil {
		return m, activationErr
	}
	want := map[string]artifactKind{}
	for _, a := range active {
		want[a.Path] = a.Kind
	}
	if len(m.Files) != len(want) {
		return m, fmt.Errorf("backup inventory does not match authoritative client")
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if !installerSafePath(f.Path) || (f.Kind != kindFile && f.Kind != kindCodexBlock && f.Kind != kindClaudeSettings) {
			return m, fmt.Errorf("backup manifest invalid or tampered")
		}
		if seen[f.Path] || want[f.Path] != f.Kind {
			return m, fmt.Errorf("backup inventory does not match authoritative client")
		}
		seen[f.Path] = true
		if f.Existed {
			d, e := readRoot(r, backupFilePath(c, tx, f.Path))
			if e != nil || digest(d) != f.DigestSHA256 {
				return m, fmt.Errorf("backup content invalid or tampered")
			}
		}
	}
	if m.State != nil {
		priorSet := set
		if bundle != nil {
			priorSet, err = renderClient(bundle, c, m.State.CapabilityConfig)
			if err != nil {
				return m, fmt.Errorf("backup prior state incompatible")
			}
		}
		if err = validatePriorState(*m.State, priorSet); err != nil {
			return m, fmt.Errorf("backup prior state incompatible")
		}
		prior := map[string]ownedState{}
		for _, o := range m.State.Owned {
			prior[o.Path] = o
		}
		for _, f := range m.Files {
			if !f.Existed {
				return m, fmt.Errorf("backup absent file conflicts with prior state")
			}
			data, e := readRoot(r, backupFilePath(c, tx, f.Path))
			if e != nil {
				return m, e
			}
			managed := data
			switch f.Kind {
			case kindCodexBlock:
				managed, e = codexManaged(data)
			case kindClaudeSettings:
				managed, e = claudeManaged(data)
			}
			if e != nil || managedDigest(f.Kind, managed) != prior[f.Path].ManagedDigest {
				return m, fmt.Errorf("backup content conflicts with prior state")
			}
		}
	} else {
		for _, f := range m.Files {
			if f.Kind == kindFile && f.Existed {
				return m, fmt.Errorf("fresh backup unexpectedly contains owned file")
			}
			if f.Existed && (f.Kind == kindCodexBlock || f.Kind == kindClaudeSettings) {
				data, e := readRoot(r, backupFilePath(c, tx, f.Path))
				if e != nil {
					return m, e
				}
				if f.Kind == kindCodexBlock {
					if _, e = codexManaged(data); e == nil {
						return m, fmt.Errorf("fresh backup contains managed block")
					}
				} else if _, e = claudeManaged(data); e == nil {
					return m, fmt.Errorf("fresh backup contains managed hook")
				}
			}
		}
	}
	return m, nil
}
func validatePriorState(st installState, set ArtifactSet) error {
	if err := compatibleState(st, set.ClientID); err != nil {
		return err
	}
	active, err := activate(set)
	if err != nil {
		return err
	}
	if !inventoryMatches(st, active) || st.ContractSHA256 != set.SourceIdentity.ContractSHA256 || st.SuiteSHA256 != set.SourceIdentity.ConformanceSuiteSHA256 || st.BundleVersion != set.BundleVersion || st.ContractVersion != set.ContractVersion || st.ArtifactFormatVersion != set.ArtifactFormatVersion || st.CapabilityConfig != set.CapabilityConfig || st.CapabilityConfigSHA256 != set.CapabilityConfigSHA256 {
		return fmt.Errorf("prior state does not match authoritative bundle shape")
	}
	want, err := activeOverridePaths(set)
	if err != nil {
		return err
	}
	if len(st.Overrides) != len(want) {
		return fmt.Errorf("prior override inventory incompatible")
	}
	seen := map[string]bool{}
	for _, o := range st.Overrides {
		if !o.UserOwned || seen[o.Path] {
			return fmt.Errorf("prior override inventory incompatible")
		}
		seen[o.Path] = true
	}
	for _, p := range want {
		if !seen[p] {
			return fmt.Errorf("prior override inventory incompatible")
		}
	}
	return nil
}
func restoreBackup(r *rootContext, tx string, m backupManifest, failAfter int, preserveSurrounding, restoreState bool) error {
	expected := map[string]PlanAction{}
	for _, f := range m.Files {
		data, ok, err := readOptional(r, f.Path)
		if err != nil {
			return err
		}
		id, idErr := identityOptional(r, f.Path, ok)
		if idErr != nil {
			return idErr
		}
		expected[f.Path] = PlanAction{Path: f.Path, expectedExists: ok, expectedDigest: digest(data), expectedIdentity: id}
	}
	for i, f := range m.Files {
		if err := revalidate(r, expected[f.Path]); err != nil {
			return fmt.Errorf("destination changed during recovery: %w", err)
		}
		current, exists, err := readOptional(r, f.Path)
		if err != nil {
			return err
		}
		if f.Existed {
			prior, err := readRoot(r, backupFilePath(m.Client, tx, f.Path))
			if err != nil || digest(prior) != f.DigestSHA256 {
				return fmt.Errorf("backup content invalid or tampered")
			}
			desired := prior
			if preserveSurrounding && exists {
				switch f.Kind {
				case kindCodexBlock:
					block, be := codexManaged(prior)
					if be == nil {
						desired, err = mergeCodex(current, append(block, '\n'))
					} else {
						desired, err = removeCodex(current)
					}
				case kindClaudeSettings:
					hook, he := claudeManaged(prior)
					if he == nil {
						desired, err = mergeClaude(current, hook)
					} else {
						desired, err = removeClaude(current)
					}
				}
			}
			if err != nil {
				return err
			}
			if err = atomicWriteRoot(r, f.Path, desired, 0o600); err != nil {
				return err
			}
		} else if exists {
			switch f.Kind {
			case kindCodexBlock:
				desired, e := removeCodex(current)
				if e != nil {
					return e
				}
				if len(desired) == 0 {
					err = removeRelative(r.mutation, f.Path)
				} else {
					err = atomicWriteRoot(r, f.Path, desired, 0o600)
				}
			case kindClaudeSettings:
				desired, e := removeClaude(current)
				if e != nil {
					return e
				}
				if claudeSettingsEmpty(desired) {
					err = removeRelative(r.mutation, f.Path)
				} else {
					err = atomicWriteRoot(r, f.Path, desired, 0o600)
				}
			default:
				err = removeRelative(r.mutation, f.Path)
			}
			if err != nil {
				return err
			}
		}
		if failAfter > 0 && i+1 >= failAfter {
			return fmt.Errorf("injected rollback failure")
		}
	}
	if !restoreState {
		return nil
	}
	if m.State == nil {
		if err := removeRelative(r.mutation, statePath(m.Client)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	} else if err := writeState(r, *m.State); err != nil {
		return err
	}
	return nil
}
func removeCodex(current []byte) ([]byte, error) {
	begins, ends := markerLines(current, codexBegin), markerLines(current, codexEnd)
	if len(begins) != 1 || len(ends) != 1 {
		return nil, fmt.Errorf("invalid Codex managed markers")
	}
	b := begins[0][0]
	e := ends[0][1]
	if e < len(current) && current[e] == '\r' {
		e++
	}
	if e < len(current) && current[e] == '\n' {
		e++
	}
	return append(append([]byte(nil), current[:b]...), current[e:]...), nil
}
func removeClaude(current []byte) ([]byte, error) {
	m, err := decodeSettings(current)
	if err != nil {
		return nil, err
	}
	var hm map[string][]json.RawMessage
	if raw := m["hooks"]; len(raw) > 0 {
		if err = json.Unmarshal(raw, &hm); err != nil {
			return nil, err
		}
	}
	var kept []json.RawMessage
	for _, e := range hm["UserPromptSubmit"] {
		c, _ := canonicalJSON(e)
		if !ownedClaudeHook(c) {
			kept = append(kept, e)
		}
	}
	if len(kept) == 0 {
		delete(hm, "UserPromptSubmit")
	} else {
		hm["UserPromptSubmit"] = kept
	}
	if len(hm) == 0 {
		delete(m, "hooks")
	} else {
		raw, _ := json.Marshal(hm)
		m["hooks"] = raw
	}
	out, _ := json.MarshalIndent(m, "", "  ")
	return append(out, '\n'), nil
}
func claudeSettingsEmpty(data []byte) bool {
	m, err := decodeSettings(data)
	return err == nil && len(m) == 0
}

func openValidatedRoot(name string) (*rootContext, error) {
	if name == "" || !filepath.IsAbs(name) {
		return nil, fmt.Errorf("explicit absolute target root is required")
	}
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("target root must be a real directory")
	}
	read, err := os.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	readInfo, err := read.Stat(".")
	if err != nil {
		read.Close()
		return nil, err
	}
	rd, ri := platformFileIdentity(readInfo)
	mutation, err := openMutationRoot(name, rd, ri)
	if err != nil {
		read.Close()
		return nil, err
	}
	md, mi, err := mutation.identity()
	if err != nil || rd != md || ri != mi {
		mutation.Close()
		read.Close()
		return nil, fmt.Errorf("root read and mutation handles do not identify the same directory")
	}
	return &rootContext{Root: read, mutation: mutation, identity: fmt.Sprintf("%x:%x", rd, ri)}, nil
}
func readRoot(r *rootContext, p string) ([]byte, error) {
	if err := rejectSymlinks(r, p, false); err != nil {
		return nil, err
	}
	return fs.ReadFile(r.FS(), p)
}
func safeRead(rootName, p string) ([]byte, error) {
	r, err := openValidatedRoot(rootName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return readRoot(r, p)
}
func readOptional(r *rootContext, p string) ([]byte, bool, error) {
	b, err := readRoot(r, p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return b, err == nil, err
}
func rejectSymlinks(r *rootContext, p string, allowMissing bool) error {
	if !installerSafePath(p) {
		return fmt.Errorf("unsafe path")
	}
	parts := strings.Split(p, "/")
	for i := range parts {
		name := strings.Join(parts[:i+1], "/")
		info, err := r.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) && allowMissing {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path rejected")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("non-directory path component")
		}
	}
	return nil
}
func mkdirRoot(r *rootContext, dir string) error {
	if dir == "." {
		return nil
	}
	if err := rejectSymlinks(r, dir, true); err != nil {
		return err
	}
	if err := ensureDirRelative(r.mutation, dir); err != nil {
		return err
	}
	return rejectSymlinks(r, dir, false)
}
func atomicWriteRoot(r *rootContext, p string, data []byte, mode fs.FileMode) error {
	if !installerSafePath(p) {
		return fmt.Errorf("unsafe path")
	}
	if err := mkdirRoot(r, path.Dir(p)); err != nil {
		return err
	}
	if err := rejectSymlinks(r, p, true); err != nil {
		return err
	}
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	tmp := path.Join(path.Dir(p), ".personal-memory-tmp-"+hex.EncodeToString(nonce[:]))
	return atomicWriteRelative(r.mutation, p, data, mode, path.Base(tmp))
}
func revalidate(r *rootContext, a PlanAction) error {
	b, ok, err := readOptional(r, a.Path)
	if err != nil {
		return err
	}
	id, idErr := identityOptional(r, a.Path, ok)
	if idErr != nil {
		return idErr
	}
	if ok != a.expectedExists || digest(b) != a.expectedDigest || ok && !reflect.DeepEqual(id, a.expectedIdentity) {
		return fmt.Errorf("destination changed after planning at %q", a.Path)
	}
	return nil
}
func snapshotAction(r *rootContext, p string) (PlanAction, error) {
	b, ok, err := readOptional(r, p)
	if err != nil {
		return PlanAction{}, err
	}
	id, err := identityOptional(r, p, ok)
	if err != nil {
		return PlanAction{}, err
	}
	return PlanAction{Path: p, expectedExists: ok, expectedDigest: digest(b), expectedIdentity: id}, nil
}

func managedDigest(kind artifactKind, content []byte) string {
	if kind == kindCodexBlock {
		content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	}
	return digest(content)
}
func identityOptional(r *rootContext, p string, exists bool) (fileIdentity, error) {
	if !exists {
		return fileIdentity{}, nil
	}
	info, err := r.Lstat(p)
	if err != nil {
		return fileIdentity{}, err
	}
	id := fileIdentity{Size: info.Size(), Mtime: info.ModTime().UnixNano(), Mode: info.Mode()}
	dev, ino := platformFileIdentity(info)
	id.Dev = dev
	id.Ino = ino
	return id, nil
}
func findAction(actions []PlanAction, p string) PlanAction {
	for _, a := range actions {
		if a.Path == p {
			return a
		}
	}
	return PlanAction{Path: p}
}
func installerSafePath(v string) bool {
	if len(v) < 1 || len(v) > 240 || !safeRelativePath(v) {
		return false
	}
	for _, p := range strings.Split(v, "/") {
		if len(p) > 100 {
			return false
		}
	}
	return true
}
func safeToken(s string) bool {
	if len(s) < 1 || len(s) > 96 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
func validClient(c conformance.ClientFamily) bool {
	return c == conformance.ClientCodex || c == conformance.ClientClaude || c == conformance.ClientChatGPT || c == conformance.ClientGenericMCP
}
func activeOverridePaths(set ArtifactSet) ([]string, error) {
	prefix := "overrides/" + strings.ReplaceAll(string(set.ClientID), "_", "-") + "/"
	out := make([]string, 0, len(set.OverridePaths))
	for _, p := range set.OverridePaths {
		if !strings.HasPrefix(p, prefix) {
			return nil, fmt.Errorf("override path is outside the client inventory")
		}
		mapped := "overrides/" + strings.TrimPrefix(p, prefix)
		if !installerSafePath(mapped) {
			return nil, fmt.Errorf("unsafe active override path")
		}
		out = append(out, mapped)
	}
	return out, nil
}
func overrideStub(p string) []byte {
	if strings.HasSuffix(p, ".json") {
		return []byte("{}\n")
	}
	return []byte("# User-owned Personal Memory overrides. This file is never overwritten.\n")
}
