package contextcatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const journalDirName = "operations"
const policyDirName = "policies"
const maxVersionPage = 100

type PublishMetadata struct {
	OperationID      string `json:"operation_id,omitempty"`
	Kind             string `json:"kind,omitempty"`
	RunID            string `json:"run_id,omitempty"`
	ProposalChecksum string `json:"proposal_checksum,omitempty"`
	Model            string `json:"model,omitempty"`
	Rules            string `json:"rules,omitempty"`
	Reason           string `json:"reason,omitempty"`
}
type Version struct {
	SnapshotHash     string   `json:"snapshot_hash"`
	ParentHash       string   `json:"parent_hash"`
	Version          string   `json:"catalog_version"`
	CreatedAt        string   `json:"created_at"`
	Provenance       string   `json:"provenance"`
	ProjectID        string   `json:"project_id"`
	EntryHash        string   `json:"entry_hash"`
	Diff             []string `json:"diff,omitempty"`
	OperationID      string   `json:"operation_id,omitempty"`
	Kind             string   `json:"kind,omitempty"`
	RunID            string   `json:"run_id,omitempty"`
	ProposalChecksum string   `json:"proposal_checksum,omitempty"`
	Model            string   `json:"model,omitempty"`
	Rules            string   `json:"rules,omitempty"`
}
type VersionPage struct {
	Versions   []Version `json:"versions"`
	NextCursor string    `json:"next_cursor,omitempty"`
}
type RestoreRequest struct {
	Reason                   string `json:"reason"`
	OperationID              string `json:"operation_id"`
	ProjectID                string `json:"project_id"`
	HistoricalHash           string `json:"historical_hash"`
	HistoricalEntryHash      string `json:"historical_entry_hash"`
	ExpectedCurrentHash      string `json:"expected_current_hash"`
	ExpectedCurrentEntryHash string `json:"expected_current_entry_hash"`
	ExpectedPolicyRevision   uint64 `json:"expected_policy_revision"`
}
type RestorePreview struct {
	Status              string   `json:"status"`
	CurrentHash         string   `json:"current_hash"`
	CurrentEntryHash    string   `json:"current_entry_hash"`
	HistoricalEntryHash string   `json:"historical_entry_hash"`
	Entry               Entry    `json:"entry"`
	PolicyRevision      uint64   `json:"policy_revision"`
	ConflictReason      string   `json:"conflict_reason,omitempty"`
	FieldDiff           []string `json:"field_diff,omitempty"`
}
type RestoreResult struct {
	Status      string `json:"status"`
	OperationID string `json:"operation_id"`
	CatalogHash string `json:"catalog_hash,omitempty"`
}
type OperationRecord struct {
	OperationID    string          `json:"operation_id"`
	RequestHash    string          `json:"request_hash,omitempty"`
	Kind           string          `json:"kind"`
	Status         string          `json:"status"`
	SourceHash     string          `json:"source_hash,omitempty"`
	TargetHash     string          `json:"target_hash,omitempty"`
	ResultHash     string          `json:"result_hash,omitempty"`
	CreatedAt      string          `json:"created_at"`
	Metadata       PublishMetadata `json:"metadata,omitempty"`
	Changes        []CardChange    `json:"changes,omitempty"`
	PolicyRevision uint64          `json:"policy_revision,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	DesiredHold    bool            `json:"desired_hold,omitempty"`
}
type CardChange struct {
	ProjectID    string `json:"project_id"`
	OldEntryHash string `json:"old_entry_hash,omitempty"`
	NewEntryHash string `json:"new_entry_hash,omitempty"`
}
type ProjectPolicy struct {
	ProjectID        string `json:"project_id"`
	Revision         uint64 `json:"revision"`
	Hold             bool   `json:"hold"`
	HistoricalOnly   bool   `json:"historical_only"`
	CurrentEntryHash string `json:"current_entry_hash"`
	UpdatedAt        string `json:"updated_at"`
	Reason           string `json:"reason,omitempty"`
}
type ProjectPolicyGuard struct {
	ProjectID                string `json:"project_id"`
	ExpectedRevision         uint64 `json:"expected_revision"`
	ExpectedCurrentEntryHash string `json:"expected_current_entry_hash"`
}
type ReconcileResult struct {
	Reconciled int      `json:"reconciled"`
	Ambiguous  []string `json:"ambiguous,omitempty"`
}

func operationID(kind, parent, target string, m PublishMetadata) string {
	b, _ := json.Marshal([]any{kind, parent, target, m})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func snapshotChanges(dir, parent string, next Snapshot) ([]CardChange, error) {
	var old Snapshot
	if parent != "" {
		var err error
		old, err = LoadSnapshot(dir, parent)
		if err != nil {
			return nil, err
		}
	}
	by := map[string]Entry{}
	for _, e := range old.Entries {
		if e.ProjectID != "" {
			by[e.ProjectID] = e
		}
	}
	out := []CardChange{}
	seen := map[string]bool{}
	for _, e := range next.Entries {
		if e.ProjectID == "" {
			continue
		}
		seen[e.ProjectID] = true
		prior, ok := by[e.ProjectID]
		if !ok || entryHash(prior) != entryHash(e) {
			c := CardChange{ProjectID: e.ProjectID, NewEntryHash: entryHash(e)}
			if ok {
				c.OldEntryHash = entryHash(prior)
			}
			out = append(out, c)
		}
	}
	for id, e := range by {
		if !seen[id] {
			out = append(out, CardChange{ProjectID: id, OldEntryHash: entryHash(e)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProjectID < out[j].ProjectID })
	return out, nil
}
func entryHash(e Entry) string {
	b, _ := json.Marshal(e)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func opPaths(dir, id string) (string, string) {
	d := filepath.Join(dir, journalDirName)
	return filepath.Join(d, id+".intent.json"), filepath.Join(d, id+".receipt.json")
}
func ensurePrivateSubdir(path string) error {
	if err := mkdirPrivate(path); err != nil {
		return err
	}
	return rejectSymlink(path)
}
func writeImmutableJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := writeExclusive(path, b); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func readJSON(path string, dst any) error {
	f, err := openRegular(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 64*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("operation record must contain exactly one JSON value")
	}
	return nil
}
func writeOperationIntent(dir string, r OperationRecord) error {
	d := filepath.Join(dir, journalDirName)
	if err := ensurePrivateSubdir(d); err != nil {
		return err
	}
	p, _ := opPaths(dir, r.OperationID)
	if !sha256Pattern.MatchString(r.OperationID) {
		return errors.New("operation id is invalid")
	}
	if err := writeImmutableJSON(p, r); err != nil {
		if errors.Is(err, os.ErrExist) {
			var old OperationRecord
			if readJSON(p, &old) == nil && sameIntent(old, r) {
				return nil
			}
		}
		return err
	}
	return nil
}
func writeOperationReceipt(dir string, intent OperationRecord, status, result string) error {
	d := filepath.Join(dir, journalDirName)
	if err := ensurePrivateSubdir(d); err != nil {
		return err
	}
	r := intent
	r.Status = status
	r.ResultHash = result
	if err := validateOperationRecord(r, r.OperationID); err != nil {
		return err
	}
	p := filepath.Join(d, r.OperationID+".receipt.json")
	if err := writeImmutableJSON(p, r); err != nil {
		if errors.Is(err, os.ErrExist) {
			var old OperationRecord
			if readJSON(p, &old) == nil && sameOperation(old, r) {
				return nil
			}
		}
		return err
	}
	return nil
}
func sameOperation(a, b OperationRecord) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// ReconcileOperations resolves intents only when the hash-checked active ancestry
// proves whether the pointer advanced through that operation.
func ReconcileOperations(dir string) (ReconcileResult, error) {
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		if e := checkExistingAncestors(dir); e != nil {
			return ReconcileResult{}, e
		}
		return ReconcileResult{Ambiguous: []string{}}, nil
	} else if err != nil {
		return ReconcileResult{}, err
	}
	if err := checkDirTree(dir, false); err != nil {
		return ReconcileResult{}, err
	}
	u, e := acquireLock(filepath.Join(dir, lockName))
	if e != nil {
		return ReconcileResult{}, e
	}
	defer u()
	return reconcileLocked(dir)
}
func checkExistingAncestors(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	current := string(os.PathSeparator)
	parts := strings.Split(strings.TrimPrefix(abs, current), string(os.PathSeparator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, e := os.Lstat(current)
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("unsafe catalog path ancestor")
		}
	}
	return nil
}
func reconcileLocked(dir string) (ReconcileResult, error) {
	return reconcileLockedExcept(dir, "")
}
func reconcileLockedExcept(dir, skipID string) (ReconcileResult, error) {
	out := ReconcileResult{Ambiguous: []string{}}
	d := filepath.Join(dir, journalDirName)
	if err := rejectSymlink(d); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, err
	}
	files, err := filepath.Glob(filepath.Join(d, "*.intent.json"))
	if err != nil {
		return out, err
	}
	_, active, activeErr := LoadActive(dir)
	if errors.Is(activeErr, ErrNotFound) {
		active = ""
	} else if activeErr != nil {
		return out, activeErr
	}
	chain := map[string]bool{}
	cursor := active
	for cursor != "" && !chain[cursor] {
		chain[cursor] = true
		s, e := LoadSnapshot(dir, cursor)
		if e != nil {
			return out, e
		}
		cursor = s.ParentHash
	}
	for _, p := range files {
		var in OperationRecord
		id := strings.TrimSuffix(filepath.Base(p), ".intent.json")
		if err := readJSON(p, &in); err != nil {
			return out, err
		}
		if err := validateOperationRecord(in, id); err != nil {
			return out, err
		}
		if id == skipID {
			continue
		}
		receipt := filepath.Join(d, in.OperationID+".receipt.json")
		var existing OperationRecord
		if err := readJSON(receipt, &existing); err == nil {
			if err := validateOperationRecord(existing, in.OperationID); err != nil {
				return out, err
			}
			if !validReceipt(in, existing) {
				return out, errors.New("operation receipt does not match intent")
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return out, err
		}
		if in.Kind == "policy" || in.Kind == "hold" {
			if len(in.Changes) != 1 || active != in.SourceHash {
				out.Ambiguous = append(out.Ambiguous, in.OperationID)
				continue
			}
			policy, pe := LoadProjectPolicy(dir, in.Changes[0].ProjectID)
			if pe != nil {
				return out, pe
			}
			status := "not_applied"
			result := ""
			if policy.Revision == in.PolicyRevision && policy.Reason == in.Reason && (in.Kind != "hold" || policy.Hold == in.DesiredHold) {
				status = "applied"
				result = active
			}
			if err := writeOperationReceipt(dir, in, status, result); err != nil {
				return out, err
			}
			out.Reconciled++
			continue
		}
		if in.Kind != "publish" && in.Kind != "restore" {
			out.Ambiguous = append(out.Ambiguous, in.OperationID)
			continue
		}
		status := "not_applied"
		result := ""
		if chain[in.TargetHash] {
			status = "applied"
			result = in.TargetHash
		} else if active != in.SourceHash {
			out.Ambiguous = append(out.Ambiguous, in.OperationID)
			continue
		}
		if err := writeOperationReceipt(dir, in, status, result); err != nil {
			return out, err
		}
		out.Reconciled++
	}
	sort.Strings(out.Ambiguous)
	if len(out.Ambiguous) > 0 {
		return out, ErrOperationsAmbiguous
	}
	return out, nil
}

func LoadOperation(dir, opID string) (OperationRecord, error) {
	if !sha256Pattern.MatchString(opID) {
		return OperationRecord{}, errors.New("operation id is invalid")
	}
	_, receipt := opPaths(dir, opID)
	var r OperationRecord
	if err := readJSON(receipt, &r); err == nil {
		if err := validateOperationRecord(r, opID); err != nil {
			return OperationRecord{}, err
		}
		intent, _ := opPaths(dir, opID)
		var in OperationRecord
		if err := readJSON(intent, &in); err != nil {
			return OperationRecord{}, err
		}
		if !validReceipt(in, r) {
			return OperationRecord{}, errors.New("operation receipt does not match intent")
		}
		return r, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return OperationRecord{}, err
	}
	intent, _ := opPaths(dir, opID)
	if err := readJSON(intent, &r); err != nil {
		return OperationRecord{}, err
	}
	if err := validateOperationRecord(r, opID); err != nil {
		return OperationRecord{}, err
	}
	return r, nil
}
func validateOperationRecord(r OperationRecord, id string) error {
	if r.OperationID != id || !sha256Pattern.MatchString(id) {
		return errors.New("operation record identity is invalid")
	}
	if r.SourceHash != "" && !sha256Pattern.MatchString(r.SourceHash) {
		return errors.New("operation source hash is invalid")
	}
	if r.TargetHash != "" && !sha256Pattern.MatchString(r.TargetHash) {
		return errors.New("operation target hash is invalid")
	}
	if r.ResultHash != "" && !sha256Pattern.MatchString(r.ResultHash) {
		return errors.New("operation result hash is invalid")
	}
	if r.Status != "intent" && r.Status != "applied" && r.Status != "not_applied" && r.Status != "restored" {
		return errors.New("operation status is invalid")
	}
	return validMetadata(r.Metadata)
}
func sameIntent(a, b OperationRecord) bool {
	a.Status = ""
	b.Status = ""
	a.ResultHash = ""
	b.ResultHash = ""
	a.CreatedAt = ""
	b.CreatedAt = ""
	return sameOperation(a, b)
}
func validReceipt(intent, receipt OperationRecord) bool {
	if !sameIntent(intent, receipt) {
		return false
	}
	switch receipt.Status {
	case "applied", "restored":
		return receipt.ResultHash != "" && receipt.ResultHash == intent.TargetHash
	case "not_applied":
		return receipt.ResultHash == ""
	default:
		return false
	}
}

func ListVersions(dir, projectID, cursor string, limit int) (VersionPage, error) {
	if limit < 1 || limit > maxVersionPage {
		return VersionPage{}, fmt.Errorf("limit must be between 1 and %d", maxVersionPage)
	}
	if _, err := ReconcileOperations(dir); err != nil {
		return VersionPage{}, err
	}
	if projectID == "" {
		return VersionPage{}, errors.New("project id is required")
	}
	_, active, err := LoadActive(dir)
	if err != nil {
		return VersionPage{}, err
	}
	var hashes []string
	seen := map[string]bool{}
	for active != "" && !seen[active] {
		seen[active] = true
		hashes = append(hashes, active)
		s, e := LoadSnapshot(dir, active)
		if e != nil {
			return VersionPage{}, e
		}
		active = s.ParentHash
	}
	start := 0
	if cursor != "" {
		found := false
		for i, h := range hashes {
			if h == cursor {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return VersionPage{}, errors.New("version cursor is invalid")
		}
	}
	end := start + limit
	if end > len(hashes) {
		end = len(hashes)
	}
	out := VersionPage{Versions: []Version{}}
	for _, h := range hashes[start:end] {
		s, e := LoadSnapshot(dir, h)
		if e != nil {
			return VersionPage{}, e
		}
		idx := entryIndex(s, projectID)
		if idx < 0 {
			continue
		}
		entry := s.Entries[idx]
		prov := "legacy_unknown"
		v := Version{SnapshotHash: h, ParentHash: s.ParentHash, Version: s.Version, CreatedAt: s.CreatedAt, Provenance: prov, ProjectID: projectID, EntryHash: entryHash(entry), Diff: []string{}}
		if r, e := findAppliedOperation(dir, h, projectID); e == nil {
			prov = r.Kind
			v.OperationID = r.OperationID
			v.Kind = r.Metadata.Kind
			v.RunID = r.Metadata.RunID
			v.ProposalChecksum = r.Metadata.ProposalChecksum
			v.Model = r.Metadata.Model
			v.Rules = r.Metadata.Rules
			if r.Metadata.RunID != "" {
				prov = "maintenance"
			} else if r.Metadata.Kind == "restore" {
				prov = "restore"
			}
		}
		v.Provenance = prov
		if parent, pe := LoadSnapshot(dir, s.ParentHash); pe == nil {
			pi := entryIndex(parent, projectID)
			if pi >= 0 {
				v.Diff = entryDiff(parent.Entries[pi], entry)
			} else {
				v.Diff = []string{"created"}
			}
		} else if s.ParentHash == "" {
			v.Diff = []string{"created"}
		}
		if len(v.Diff) == 0 {
			continue
		}
		out.Versions = append(out.Versions, v)
	}
	if end < len(hashes) {
		out.NextCursor = hashes[end-1]
	}
	return out, nil
}
func findAppliedOperation(dir, hash, projectID string) (OperationRecord, error) {
	files, _ := filepath.Glob(filepath.Join(dir, journalDirName, "*.receipt.json"))
	for _, p := range files {
		base := filepath.Base(p)
		id := strings.TrimSuffix(base, ".receipt.json")
		r, e := LoadOperation(dir, id)
		if e != nil {
			continue
		}
		if (r.Kind != "publish" && r.Kind != "restore") || r.Status != "applied" || r.ResultHash != hash || r.TargetHash != hash {
			continue
		}
		for _, change := range r.Changes {
			if change.ProjectID == projectID {
				return r, nil
			}
		}
	}
	return OperationRecord{}, os.ErrNotExist
}
func entryDiff(old, next Entry) []string {
	out := []string{}
	if old.Summary != next.Summary {
		out = append(out, "summary")
	}
	if !equalStrings(old.Aliases, next.Aliases) {
		out = append(out, "aliases")
	}
	if !equalStrings(old.OwnedComponents, next.OwnedComponents) {
		out = append(out, "owned_components")
	}
	if !equalStrings(old.Boundaries, next.Boundaries) {
		out = append(out, "boundaries")
	}
	if !equalStrings(old.Uses, next.Uses) {
		out = append(out, "uses")
	}
	if !equalStrings(old.SharedWith, next.SharedWith) {
		out = append(out, "shared_with")
	}
	if !equalStrings(old.PositiveExamples, next.PositiveExamples) {
		out = append(out, "positive_examples")
	}
	if !equalStrings(old.NegativeExamples, next.NegativeExamples) {
		out = append(out, "negative_examples")
	}
	if !equalStrings(old.EvidenceRefs, next.EvidenceRefs) {
		out = append(out, "evidence_refs")
	}
	return out
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func LoadProjectPolicy(dir, projectID string) (ProjectPolicy, error) {
	if !sha256Pattern.MatchString(projectID) {
		return ProjectPolicy{}, errors.New("project id is invalid")
	}
	p := filepath.Join(dir, policyDirName, projectID+".json")
	if err := checkDirTree(dir, false); err != nil {
		return ProjectPolicy{}, err
	}
	dirInfo, err := os.Lstat(filepath.Join(dir, policyDirName))
	if errors.Is(err, os.ErrNotExist) {
		return ProjectPolicy{ProjectID: projectID}, nil
	}
	if err != nil {
		return ProjectPolicy{}, err
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 || dirInfo.Mode().Perm()&0077 != 0 {
		return ProjectPolicy{}, errors.New("policy directory is unsafe or not private")
	}
	var x ProjectPolicy
	if err := readJSON(p, &x); errors.Is(err, os.ErrNotExist) {
		return ProjectPolicy{ProjectID: projectID}, nil
	} else if err != nil {
		return x, err
	}
	if x.ProjectID != projectID {
		return x, errors.New("policy identity mismatch")
	}
	return x, nil
}
func SetProjectPolicy(dir string, p ProjectPolicy, expectedCurrentEntryHash string, expectedRevision uint64) (ProjectPolicy, error) {
	if !sha256Pattern.MatchString(p.ProjectID) {
		return p, errors.New("project id is invalid")
	}
	if err := checkDirTree(dir, false); err != nil {
		return p, err
	}
	u, e := acquireLock(filepath.Join(dir, lockName))
	if e != nil {
		return p, e
	}
	defer u()
	if _, e := reconcileLocked(dir); e != nil {
		return p, e
	}
	return setProjectPolicyLocked(dir, p, expectedCurrentEntryHash, expectedRevision)
}

// SetPublicationHold changes only the operational hold flag and reason while
// preserving HistoricalOnly and every other persisted policy field.
func SetPublicationHold(dir, projectID string, hold bool, reason, expectedCurrentEntryHash string, expectedRevision uint64) (ProjectPolicy, error) {
	if !sha256Pattern.MatchString(projectID) {
		return ProjectPolicy{}, errors.New("project id is invalid")
	}
	if err := checkDirTree(dir, false); err != nil {
		return ProjectPolicy{}, err
	}
	u, e := acquireLock(filepath.Join(dir, lockName))
	if e != nil {
		return ProjectPolicy{}, e
	}
	defer u()
	if _, e := reconcileLocked(dir); e != nil {
		return ProjectPolicy{}, e
	}
	p, e := LoadProjectPolicy(dir, projectID)
	if e != nil {
		return p, e
	}
	p.Hold = hold
	p.Reason = reason
	return setProjectPolicyLocked(dir, p, expectedCurrentEntryHash, expectedRevision)
}

func setProjectPolicyLocked(dir string, p ProjectPolicy, expectedCurrentEntryHash string, expectedRevision uint64) (ProjectPolicy, error) {
	s, active, e := LoadActive(dir)
	if e != nil {
		return p, e
	}
	idx := entryIndex(s, p.ProjectID)
	if idx < 0 {
		return p, ErrNotFound
	}
	h := entryHash(s.Entries[idx])
	old, e := LoadProjectPolicy(dir, p.ProjectID)
	if e != nil {
		return p, e
	}
	if active == "" || h != expectedCurrentEntryHash || old.Revision != expectedRevision {
		return p, ErrConflict
	}
	if len(p.Reason) > 512 || strings.TrimSpace(p.Reason) == "" || strings.ContainsRune(p.Reason, '\x00') {
		return p, errors.New("policy change reason must contain 1 to 512 safe bytes")
	}
	p.Revision = old.Revision + 1
	p.CurrentEntryHash = h
	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	d := filepath.Join(dir, policyDirName)
	if e := ensurePrivateSubdir(d); e != nil {
		return p, e
	}
	b, _ := json.Marshal(p)
	b = append(b, '\n')
	policySum := sha256.Sum256([]byte(p.ProjectID + fmt.Sprint(p.Revision) + p.Reason))
	meta := PublishMetadata{Kind: "policy", ProposalChecksum: hex.EncodeToString(policySum[:])}
	op := OperationRecord{OperationID: operationID("policy", active, expectedCurrentEntryHash, meta), Kind: "policy", Status: "intent", SourceHash: active, TargetHash: active, CreatedAt: p.UpdatedAt, Metadata: meta, PolicyRevision: p.Revision, Reason: p.Reason, Changes: []CardChange{{ProjectID: p.ProjectID}}}
	if err := writeOperationIntent(dir, op); err != nil {
		return p, err
	}
	if e := atomicReplace(d, p.ProjectID+".json", b); e != nil {
		return p, e
	}
	if e := writeOperationReceipt(dir, op, "applied", active); e != nil {
		return p, e
	}
	return p, nil
}
func entryIndex(s Snapshot, id string) int {
	for i, e := range s.Entries {
		if e.ProjectID == id {
			return i
		}
	}
	return -1
}

func PreviewRestore(dir string, r RestoreRequest) (RestorePreview, error) {
	if _, e := ReconcileOperations(dir); e != nil {
		return RestorePreview{}, e
	}
	cur, ch, e := LoadActive(dir)
	if e != nil {
		return RestorePreview{}, e
	}
	hi, e := LoadSnapshot(dir, r.HistoricalHash)
	if e != nil {
		return RestorePreview{}, e
	}
	ci, hiIndex := entryIndex(cur, r.ProjectID), entryIndex(hi, r.ProjectID)
	if ci < 0 || hiIndex < 0 {
		return RestorePreview{Status: "not_found", CurrentHash: ch}, nil
	}
	ce, he := cur.Entries[ci], hi.Entries[hiIndex]
	eh, hh := entryHash(ce), entryHash(he)
	status := "ready"
	if ch != r.ExpectedCurrentHash || eh != r.ExpectedCurrentEntryHash || hh != r.HistoricalEntryHash {
		status = "conflict"
	}
	policy, pe := LoadProjectPolicy(dir, r.ProjectID)
	if pe != nil {
		return RestorePreview{}, pe
	}
	if policy.Revision != r.ExpectedPolicyRevision {
		status = "conflict"
	}
	if !SameStableProjectIdentity(ce, he) || !SameReviewSourceIdentity(ce, he) {
		status = "source_changed"
	}
	if policy.HistoricalOnly && len(he.Uses) > 0 {
		status = "conflict"
	}
	return RestorePreview{Status: status, CurrentHash: ch, CurrentEntryHash: eh, HistoricalEntryHash: hh, Entry: cloneEntry(he), PolicyRevision: policy.Revision, ConflictReason: restoreConflictReason(status, policy.HistoricalOnly, len(he.Uses) > 0), FieldDiff: entryDiff(ce, he)}, nil
}

func Restore(dir string, r RestoreRequest) (RestoreResult, error) {
	if !sha256Pattern.MatchString(r.OperationID) {
		return RestoreResult{}, errors.New("operation id must be a lowercase SHA-256 digest")
	}
	if len(strings.TrimSpace(r.Reason)) < 1 || len(r.Reason) > 512 || strings.ContainsRune(r.Reason, '\x00') {
		return RestoreResult{}, errors.New("restore reason must contain 1 to 512 safe bytes")
	}
	u, e := acquireLock(filepath.Join(dir, lockName))
	if e != nil {
		return RestoreResult{}, e
	}
	defer u()
	if _, e := reconcileLocked(dir); e != nil {
		return RestoreResult{}, e
	}
	if existing, e := LoadOperation(dir, r.OperationID); e == nil {
		if existing.Metadata.Kind != "restore" || existing.RequestHash != restoreRequestHash(r) {
			return RestoreResult{Status: "conflict", OperationID: r.OperationID}, nil
		}
		status := existing.Status
		if status == "applied" {
			status = "restored"
		}
		return RestoreResult{Status: status, OperationID: r.OperationID, CatalogHash: existing.ResultHash}, nil
	}
	cur, ch, e := LoadActive(dir)
	if e != nil {
		return RestoreResult{}, e
	}
	hi, e := LoadSnapshot(dir, r.HistoricalHash)
	if e != nil {
		return RestoreResult{}, e
	}
	ci, ii := entryIndex(cur, r.ProjectID), entryIndex(hi, r.ProjectID)
	if ci < 0 || ii < 0 {
		return RestoreResult{Status: "not_found", OperationID: r.OperationID}, nil
	}
	current, historical := cur.Entries[ci], hi.Entries[ii]
	if ch != r.ExpectedCurrentHash || entryHash(current) != r.ExpectedCurrentEntryHash || entryHash(historical) != r.HistoricalEntryHash {
		return RestoreResult{Status: "conflict", OperationID: r.OperationID}, nil
	}
	if !SameStableProjectIdentity(current, historical) || !SameReviewSourceIdentity(current, historical) {
		return RestoreResult{Status: "source_changed", OperationID: r.OperationID}, nil
	}
	policy, e := LoadProjectPolicy(dir, r.ProjectID)
	if e != nil {
		return RestoreResult{}, e
	}
	if policy.Revision != r.ExpectedPolicyRevision {
		return RestoreResult{Status: "conflict", OperationID: r.OperationID}, nil
	}
	meta := PublishMetadata{Kind: "restore", OperationID: r.OperationID, ProposalChecksum: restoreRequestHash(r), Reason: r.Reason}
	if policy.HistoricalOnly && len(historical.Uses) > 0 {
		return RestoreResult{Status: "conflict", OperationID: r.OperationID}, nil
	}
	restored := restoreDescription(current, historical)
	cur.Entries[ci] = restored
	cur.ParentHash = ch
	cur.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	cur.Version = "restore-" + r.OperationID[:16]
	targetHash, e := Hash(cur)
	if e != nil {
		return RestoreResult{}, e
	}
	intent := OperationRecord{OperationID: r.OperationID, RequestHash: restoreRequestHash(r), Kind: "restore", Status: "intent", SourceHash: ch, TargetHash: targetHash, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: meta, Reason: r.Reason, Changes: []CardChange{{ProjectID: r.ProjectID, OldEntryHash: entryHash(current), NewEntryHash: entryHash(restored)}}}
	if e = writeOperationIntent(dir, intent); e != nil {
		return RestoreResult{}, e
	}
	policy.Hold = true
	policy.Reason = r.Reason
	policy.Revision++
	policy.CurrentEntryHash = entryHash(current)
	policy.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	pd := filepath.Join(dir, policyDirName)
	if e = ensurePrivateSubdir(pd); e != nil {
		return RestoreResult{}, e
	}
	holdMeta := PublishMetadata{Kind: "policy", ProposalChecksum: meta.ProposalChecksum}
	holdIntent := OperationRecord{OperationID: operationID("hold", ch, targetHash, holdMeta), Kind: "hold", Status: "intent", SourceHash: ch, TargetHash: ch, CreatedAt: policy.UpdatedAt, Metadata: holdMeta, Reason: r.Reason, PolicyRevision: policy.Revision, DesiredHold: true, Changes: []CardChange{{ProjectID: r.ProjectID}}}
	if e = writeOperationIntent(dir, holdIntent); e != nil {
		return RestoreResult{}, e
	}
	pb, _ := json.Marshal(policy)
	if e = atomicReplace(pd, r.ProjectID+".json", append(pb, '\n')); e != nil {
		return RestoreResult{}, e
	}
	if e = writeOperationReceipt(dir, holdIntent, "applied", ch); e != nil {
		return RestoreResult{}, e
	}
	pubMeta := meta
	hash, e := publishLockedMetadataSkipping(dir, cur, ch, pubMeta, r.OperationID)
	if e != nil {
		return RestoreResult{Status: "held", OperationID: r.OperationID}, e
	}
	policy.CurrentEntryHash = entryHash(restored)
	policy.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	pb, _ = json.Marshal(policy)
	if e = atomicReplace(pd, r.ProjectID+".json", append(pb, '\n')); e != nil {
		return RestoreResult{Status: "applied_policy_write_uncertain", OperationID: r.OperationID, CatalogHash: hash}, e
	}
	return RestoreResult{Status: "restored", OperationID: r.OperationID, CatalogHash: hash}, nil
}
func restoreRequestHash(r RestoreRequest) string {
	r.OperationID = ""
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func restoreDescription(current, historical Entry) Entry {
	out := cloneEntry(current)
	out.Summary = historical.Summary
	out.Aliases = append([]string{}, historical.Aliases...)
	out.OwnedComponents = append([]string{}, historical.OwnedComponents...)
	out.Boundaries = append([]string{}, historical.Boundaries...)
	out.Uses = append([]string{}, historical.Uses...)
	out.SharedWith = append([]string{}, historical.SharedWith...)
	out.PositiveExamples = append([]string{}, historical.PositiveExamples...)
	out.NegativeExamples = append([]string{}, historical.NegativeExamples...)
	out.EvidenceRefs = append([]string{}, historical.EvidenceRefs...)
	return out
}

func validMetadata(m PublishMetadata) error {
	if m.OperationID != "" && !sha256Pattern.MatchString(m.OperationID) {
		return errors.New("operation id metadata is invalid")
	}
	if m.Kind != "" && m.Kind != "manual" && m.Kind != "registration" && m.Kind != "maintenance" && m.Kind != "restore" && m.Kind != "policy" {
		return errors.New("publication kind is invalid")
	}
	for _, v := range []string{m.RunID, m.ProposalChecksum, m.Model, m.Rules, m.Reason} {
		if len(v) > 256 || strings.ContainsRune(v, '\x00') {
			return errors.New("publication metadata is invalid")
		}
	}
	if len(m.Reason) > 512 {
		return errors.New("publication reason is too long")
	}
	if m.ProposalChecksum != "" && !sha256Pattern.MatchString(m.ProposalChecksum) {
		return errors.New("proposal checksum is invalid")
	}
	return nil
}
func restoreConflictReason(status string, historicalOnly, incompatibleUses bool) string {
	if status == "source_changed" {
		return "source_identity_changed"
	}
	if incompatibleUses && (historicalOnly) {
		return "historical_only_incompatible"
	}
	if status == "conflict" {
		return "snapshot_or_policy_changed"
	}
	return ""
}

// PublishWithPolicyGuard validates source identity and persisted restrictions
// under the same exclusive lock used to publish the next generation.
func PublishWithPolicyGuard(dir string, snapshot Snapshot, expectedActiveHash string, metadata PublishMetadata, guards []ProjectPolicyGuard) (string, error) {
	if err := ensureDirTree(dir); err != nil {
		return "", err
	}
	u, err := acquireLock(filepath.Join(dir, lockName))
	if err != nil {
		return "", err
	}
	defer u()
	active, err := activeHash(dir)
	if errors.Is(err, ErrNotFound) {
		active = ""
		err = nil
	}
	if err != nil {
		return "", err
	}
	if active != expectedActiveHash {
		return "", ErrConflict
	}
	current, _, err := LoadActive(dir)
	if errors.Is(err, ErrNotFound) {
		current = Snapshot{Entries: []Entry{}}
		err = nil
	}
	if err != nil {
		return "", err
	}
	guarded := make(map[string]ProjectPolicyGuard, len(guards))
	for _, guard := range guards {
		if _, exists := guarded[guard.ProjectID]; exists {
			return "", errors.New("duplicate project policy guard")
		}
		guarded[guard.ProjectID] = guard
		idx := entryIndex(current, guard.ProjectID)
		if idx < 0 {
			return "", ErrConflict
		}
		policy, err := LoadProjectPolicy(dir, guard.ProjectID)
		if err != nil {
			return "", err
		}
		if policy.Hold || policy.Revision != guard.ExpectedRevision || entryHash(current.Entries[idx]) != guard.ExpectedCurrentEntryHash {
			return "", ErrConflict
		}
		next := entryIndex(snapshot, guard.ProjectID)
		if next < 0 || !SameStableProjectIdentity(current.Entries[idx], snapshot.Entries[next]) || !SameReviewSourceIdentity(current.Entries[idx], snapshot.Entries[next]) {
			return "", ErrConflict
		}
		if policy.HistoricalOnly && len(snapshot.Entries[next].Uses) > 0 {
			return "", errors.New("historical-only project cannot publish current uses")
		}
	}
	changes, err := snapshotChanges(dir, expectedActiveHash, snapshot)
	if err != nil {
		return "", err
	}
	for _, change := range changes {
		if _, ok := guarded[change.ProjectID]; !ok {
			return "", fmt.Errorf("project %s changed without a policy guard", change.ProjectID)
		}
	}
	metadata.Kind = "maintenance"
	return publishLockedMetadata(dir, snapshot, expectedActiveHash, metadata)
}
