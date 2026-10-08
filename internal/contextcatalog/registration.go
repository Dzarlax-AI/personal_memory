package contextcatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	proposalDirName  = "proposals"
	maxProposals     = 64
	maxProposalBytes = 512 * 1024
)

var (
	projectKeyPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)
	secretPattern        = regexp.MustCompile(`(?i)(api[_-]?key|token|password|secret|authorization)\s*[:=]\s*[^\s]+|\bbearer\s+[a-z0-9._~-]{8,}|\b(?:sk-[a-z0-9]{12,}|gh[pousr]_[a-z0-9]{12,}|xox[baprs]-[a-z0-9-]{12,})`)
	credentialURLPattern = regexp.MustCompile(`(?i)https?://[^/@\s]+:[^/@\s]+@`)
	absolutePathPattern  = regexp.MustCompile(`(?:^|[\s"'=])/(?:(?:[A-Za-z0-9._~-]+/)+[A-Za-z0-9._~-]+|(?:Users|home|root|private|tmp|etc|var)(?:/|\b))|(?:^|[\s"'=])[A-Za-z]:\\`)
)

type updateProposal struct {
	ProjectID string     `json:"project_id"`
	Owner     string     `json:"owner"`
	BaseHash  string     `json:"base_hash"`
	Name      string     `json:"name"`
	Summary   string     `json:"summary"`
	Evidence  []Evidence `json:"evidence"`
	CreatedAt string     `json:"created_at"`
}

// EnsureProject registers a caller-declared project in the owner-scoped
// projects registry. It never grants human approval or rewrites a description.
func EnsureProject(dir, owner string, input RegistrationInput) (RegistrationResult, error) {
	if err := validateRegistration(owner, input); err != nil {
		return RegistrationResult{}, err
	}
	if err := checkDirTree(dir, true); err != nil {
		return RegistrationResult{Status: "unavailable"}, err
	}
	unlock, err := acquireLock(filepath.Join(dir, lockName))
	if err != nil {
		return RegistrationResult{Status: "unavailable"}, err
	}
	defer unlock()

	s, hash, err := LoadActive(dir)
	if errors.Is(err, ErrNotFound) {
		s = Snapshot{SchemaVersion: SchemaVersion, Version: "registry-1", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		hash = ""
	} else if err != nil {
		return RegistrationResult{Status: "unavailable"}, err
	}
	projectID := scopedProjectID(owner, input.Namespace, input.ProjectKey)
	for i := range s.Entries {
		e := &s.Entries[i]
		if e.ProjectID == projectID {
			if e.Owner != owner || e.ProjectKey != input.ProjectKey || e.Namespace != input.Namespace || e.Tag != input.Tag || normalizeName(e.Name) != normalizeName(input.Name) {
				return RegistrationResult{Status: "ambiguous", ProjectID: projectID, CatalogHash: hash}, nil
			}
			if descriptionMatches(*e, input) {
				return RegistrationResult{Status: "existing", ProjectID: projectID, Tag: e.Tag, CatalogHash: hash}, nil
			}
			if err := saveProposal(dir, updateProposal{ProjectID: projectID, Owner: owner, BaseHash: hash, Name: input.Name, Summary: input.Summary, Evidence: cloneEvidence(input.Evidence), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
				return RegistrationResult{Status: "unavailable", ProjectID: projectID, Tag: e.Tag, CatalogHash: hash}, err
			}
			return RegistrationResult{Status: "proposed_update", ProjectID: projectID, Tag: e.Tag, CatalogHash: hash}, nil
		}
		if e.Namespace != input.Namespace || e.Tag != input.Tag {
			continue
		}
		if e.ProjectID != "" {
			if e.Owner != owner || e.ProjectKey != input.ProjectKey || normalizeName(e.Name) != normalizeName(input.Name) {
				return RegistrationResult{Status: "ambiguous", ProjectID: projectID, CatalogHash: hash}, nil
			}
			return RegistrationResult{Status: "ambiguous", ProjectID: projectID, CatalogHash: hash}, nil
		}
		// A legacy manual card is bindable only by the caller's exact tag and
		// consistent name. Similar names and aliases are never consulted.
		if normalizeName(e.Name) != normalizeName(input.Name) {
			return RegistrationResult{Status: "ambiguous", ProjectID: projectID, CatalogHash: hash}, nil
		}
		if e.Summary != input.Summary {
			if err := saveProposal(dir, updateProposal{ProjectID: projectID, Owner: owner, BaseHash: hash, Name: input.Name, Summary: input.Summary, Evidence: cloneEvidence(input.Evidence), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
				return RegistrationResult{Status: "unavailable", ProjectID: projectID, Tag: e.Tag, CatalogHash: hash}, err
			}
			return RegistrationResult{Status: "proposed_update", ProjectID: projectID, Tag: e.Tag, CatalogHash: hash}, nil
		}
		candidate := *e
		candidate.ProjectID, candidate.ProjectKey, candidate.Owner = projectID, input.ProjectKey, owner
		candidate.ClientEvidence = cloneEvidence(input.Evidence)
		candidate.DescriptionSource = "client_declared"
		candidate.DeclaredSummary = input.Summary
		if !descriptionMatches(candidate, input) {
			if err := saveProposal(dir, updateProposal{ProjectID: projectID, Owner: owner, BaseHash: hash, Name: input.Name, Summary: input.Summary, Evidence: cloneEvidence(input.Evidence), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
				return RegistrationResult{Status: "unavailable", ProjectID: projectID, Tag: e.Tag, CatalogHash: hash}, err
			}
			return RegistrationResult{Status: "proposed_update", ProjectID: projectID, Tag: e.Tag, CatalogHash: hash}, nil
		}
		*e = candidate
		return publishRegistration(dir, s, hash, RegistrationResult{Status: "existing", ProjectID: projectID, Tag: e.Tag})
	}
	for _, e := range s.Entries {
		if e.Namespace == input.Namespace && e.Tag == input.Tag {
			return RegistrationResult{Status: "ambiguous", ProjectID: projectID, CatalogHash: hash}, nil
		}
	}
	if len(s.Entries) >= MaxEntries {
		return RegistrationResult{Status: "unavailable", ProjectID: projectID}, fmt.Errorf("catalog entry limit of %d reached", MaxEntries)
	}
	s.Entries = append(s.Entries, Entry{Namespace: input.Namespace, Tag: input.Tag, Name: input.Name, Summary: input.Summary,
		Aliases: []string{}, OwnedComponents: []string{}, Boundaries: []string{}, Uses: []string{}, SharedWith: []string{}, PositiveExamples: []string{}, NegativeExamples: []string{}, EvidenceRefs: []string{}, ReviewStatus: "declared",
		ProjectID: projectID, ProjectKey: input.ProjectKey, Owner: owner, ClientEvidence: cloneEvidence(input.Evidence), DescriptionSource: "client_declared"})
	return publishRegistration(dir, s, hash, RegistrationResult{Status: "created", ProjectID: projectID, Tag: input.Tag})
}

func publishRegistration(dir string, s Snapshot, parent string, result RegistrationResult) (RegistrationResult, error) {
	s.ParentHash = parent
	s.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	base := sha256.Sum256([]byte(parent + "\x00" + result.ProjectID + "\x00" + s.CreatedAt))
	s.Version = "registry-" + hex.EncodeToString(base[:8])
	hash, err := publishLocked(dir, s, parent)
	if err != nil {
		result.Status = "unavailable"
		return result, err
	}
	result.CatalogHash = hash
	return result, nil
}

// ResolveProject resolves only eligible entries owned by owner.
func ResolveProject(dir, owner, projectID string) (Entry, string, error) {
	if strings.TrimSpace(owner) == "" || len(owner) > 128 {
		return Entry{}, "", errors.New("owner is invalid")
	}
	if !sha256Pattern.MatchString(projectID) {
		return Entry{}, "", errors.New("project id is invalid")
	}
	s, hash, err := LoadActive(dir)
	if err != nil {
		return Entry{}, "", err
	}
	for _, e := range s.Entries {
		if e.ProjectID == projectID && e.Owner == owner && e.Namespace == "projects" && Eligible(e) {
			return cloneEntry(e), hash, nil
		}
	}
	return Entry{}, hash, ErrNotFound
}

func validateRegistration(owner string, in RegistrationInput) error {
	if strings.TrimSpace(owner) == "" || len(owner) > 128 {
		return errors.New("owner is invalid")
	}
	if in.Namespace != "projects" {
		return errors.New("registration namespace must be projects")
	}
	if !validProjectKey(in.ProjectKey) {
		return errors.New("project_key must contain 8 to 128 safe identifier characters")
	}
	if secretPattern.MatchString(in.ProjectKey) {
		return errors.New("project_key contains credential-like material")
	}
	if !tagPattern.MatchString(in.Tag) || len(in.Tag) > 64 {
		return errors.New("tag must be lowercase kebab-case up to 64 characters")
	}
	if secretPattern.MatchString(in.Tag) {
		return errors.New("tag contains credential-like material")
	}
	if err := validateInputText("name", in.Name, 1, 128); err != nil {
		return err
	}
	if err := validateInputText("summary", in.Summary, 1, MaxSummaryBytes); err != nil {
		return err
	}
	if len(in.Evidence) > MaxEvidenceItems {
		return fmt.Errorf("evidence exceeds maximum of %d items", MaxEvidenceItems)
	}
	total := 0
	for i, e := range in.Evidence {
		if !validEvidenceKind(e.Kind) {
			return fmt.Errorf("evidence[%d].kind is unsupported", i)
		}
		if err := validateInputText(fmt.Sprintf("evidence[%d].text", i), e.Text, 1, MaxEvidenceBytes); err != nil {
			return err
		}
		total += len(e.Text)
	}
	if total > MaxEvidenceBytes {
		return fmt.Errorf("evidence exceeds maximum of %d bytes", MaxEvidenceBytes)
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if len(b) > MaxRegistrationBytes {
		return fmt.Errorf("registration exceeds maximum of %d bytes", MaxRegistrationBytes)
	}
	return nil
}

// ValidateRegistration applies the bounded input contract without touching the
// registry directory. Owner is supplied by server authentication/configuration.
func ValidateRegistration(owner string, in RegistrationInput) error {
	return validateRegistration(owner, in)
}

func validateInputText(field, value string, min, max int) error {
	if len(strings.TrimSpace(value)) < min || len(value) > max || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s must contain %d to %d safe bytes", field, min, max)
	}
	if secretPattern.MatchString(value) || credentialURLPattern.MatchString(value) || absolutePathPattern.MatchString(value) {
		return fmt.Errorf("%s contains disallowed credential or path material", field)
	}
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err == nil && (u.User != nil || (u.Scheme != "https" && u.Scheme != "http")) {
			return fmt.Errorf("%s contains a disallowed URL", field)
		}
	}
	return nil
}

func validEvidenceKind(kind string) bool {
	return kind == "client_readme" || kind == "client_agents" || kind == "user_declared"
}
func validProjectKey(key string) bool { return projectKeyPattern.MatchString(key) }
func scopedProjectID(owner, namespace, key string) string {
	sum := sha256.Sum256([]byte(owner + "\x00" + namespace + "\x00" + key))
	return hex.EncodeToString(sum[:])
}
func normalizeName(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
func descriptionMatches(e Entry, in RegistrationInput) bool {
	declaredSummary := e.DeclaredSummary
	if declaredSummary == "" {
		declaredSummary = e.Summary
	}
	return e.Name == in.Name && declaredSummary == in.Summary && equalEvidence(e.ClientEvidence, in.Evidence)
}
func equalEvidence(a, b []Evidence) bool { return bytes.Equal(mustJSON(a), mustJSON(b)) }
func mustJSON(v any) []byte              { b, _ := json.Marshal(v); return b }
func cloneEvidence(in []Evidence) []Evidence {
	if in == nil {
		return nil
	}
	return append([]Evidence{}, in...)
}
func cloneEntry(e Entry) Entry {
	e.ClientEvidence = cloneEvidence(e.ClientEvidence)
	e.Aliases = cloneStrings(e.Aliases)
	e.OwnedComponents = cloneStrings(e.OwnedComponents)
	e.Boundaries = cloneStrings(e.Boundaries)
	e.Uses = cloneStrings(e.Uses)
	e.SharedWith = cloneStrings(e.SharedWith)
	e.PositiveExamples = cloneStrings(e.PositiveExamples)
	e.NegativeExamples = cloneStrings(e.NegativeExamples)
	e.EvidenceRefs = cloneStrings(e.EvidenceRefs)
	return e
}

func saveProposal(dir string, p updateProposal) error {
	encoded, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(encoded) > MaxRegistrationBytes+512 {
		return errors.New("update proposal exceeds size limit")
	}
	proposalDir := filepath.Join(dir, proposalDirName)
	if err := os.Mkdir(proposalDir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := rejectSymlink(proposalDir); err != nil {
		return err
	}
	if err := os.Chmod(proposalDir, 0700); err != nil {
		return err
	}
	stable := p
	stable.CreatedAt = ""
	stableEncoded, err := json.Marshal(stable)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(append(append([]byte(p.ProjectID+"\x00"+p.BaseHash+"\x00"), stableEncoded...), []byte("\x00update")...))
	path := filepath.Join(proposalDir, hex.EncodeToString(sum[:])+".json")
	if f, err := openRegular(path, unix.O_RDONLY, 0); err == nil {
		_ = f.Close()
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(proposalDir)
	if err != nil {
		return err
	}
	if len(entries) >= maxProposals {
		return errors.New("catalog proposal limit reached")
	}
	total := 0
	for _, entry := range entries {
		if info, e := entry.Info(); e == nil {
			total += int(info.Size())
		}
	}
	if total+len(encoded) > maxProposalBytes {
		return errors.New("catalog proposal storage limit reached")
	}
	return writeExclusive(path, encoded)
}
