// Package contextcatalog stores reviewed, versioned context used by optional
// memory features. It has no network or database dependencies.
package contextcatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	SchemaVersion        = 1
	MaxEntries           = 32
	MaxSnapshotBytes     = 64 * 1024
	MaxDescriptionLen    = 4 * 1024
	MaxSummaryBytes      = 2 * 1024
	MaxRegistrationBytes = 8 * 1024
	MaxEvidenceBytes     = 4 * 1024
	MaxEvidenceItems     = 4
)

var (
	ErrConflict      = errors.New("active catalog changed; reload and review before publishing")
	ErrNotFound      = errors.New("no active catalog")
	tagPattern       = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	sha256Pattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	allowedNamespace = map[string]struct{}{"personal": {}, "work": {}, "projects": {}, "job-search": {}, "tech": {}}
)

// Entry describes one reviewed namespace/tag context. List order is preserved
// and participates in the snapshot hash; it is never silently normalized.
type Entry struct {
	Namespace         string     `json:"namespace"`
	Tag               string     `json:"tag"`
	Name              string     `json:"name"`
	Summary           string     `json:"summary"`
	Aliases           []string   `json:"aliases"`
	OwnedComponents   []string   `json:"owned_components"`
	Boundaries        []string   `json:"boundaries"`
	Uses              []string   `json:"uses"`
	SharedWith        []string   `json:"shared_with"`
	PositiveExamples  []string   `json:"positive_examples"`
	NegativeExamples  []string   `json:"negative_examples"`
	EvidenceRefs      []string   `json:"evidence_refs"`
	ReviewStatus      string     `json:"review_status"`
	ProjectID         string     `json:"project_id,omitempty"`
	ProjectKey        string     `json:"project_key,omitempty"`
	Owner             string     `json:"owner,omitempty"`
	ClientEvidence    []Evidence `json:"client_evidence,omitempty"`
	DescriptionSource string     `json:"description_source,omitempty"`
	DeclaredSummary   string     `json:"declared_summary,omitempty"`
}

// Evidence is a bounded, untrusted excerpt supplied by an MCP client.
type Evidence struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// RegistrationInput is the caller-declared project identity and description.
type RegistrationInput struct {
	ProjectKey string     `json:"project_key"`
	Namespace  string     `json:"namespace"`
	Name       string     `json:"name"`
	Tag        string     `json:"tag"`
	Summary    string     `json:"summary"`
	Evidence   []Evidence `json:"evidence"`
}

// RegistrationResult describes the outcome without implying human approval.
type RegistrationResult struct {
	Status      string `json:"status"`
	ProjectID   string `json:"project_id,omitempty"`
	Tag         string `json:"tag,omitempty"`
	CatalogHash string `json:"catalog_hash,omitempty"`
}

// Snapshot is a complete catalog generation. ParentHash is empty only for an
// initial publication. CreatedAt must be an RFC3339 timestamp.
type Snapshot struct {
	SchemaVersion int     `json:"schema_version"`
	Version       string  `json:"catalog_version"`
	ParentHash    string  `json:"parent_hash"`
	CreatedAt     string  `json:"created_at"`
	Entries       []Entry `json:"entries"`
}

// Validate checks the portable v1 shape and limits. Draft and unresolved
// entries may be validated/exported, but Publish accepts approved entries only.
func Validate(s Snapshot) error {
	if s.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version must be %d", SchemaVersion)
	}
	if strings.TrimSpace(s.Version) == "" || len(s.Version) > 128 {
		return errors.New("catalog_version must contain 1 to 128 characters")
	}
	if s.ParentHash != "" && !sha256Pattern.MatchString(s.ParentHash) {
		return errors.New("parent_hash must be empty or a lowercase SHA-256 hex digest")
	}
	if _, err := time.Parse(time.RFC3339, s.CreatedAt); err != nil {
		return errors.New("created_at must be an RFC3339 timestamp")
	}
	if len(s.Entries) > MaxEntries {
		return fmt.Errorf("entries exceeds maximum of %d", MaxEntries)
	}
	seen := make(map[string]struct{}, len(s.Entries))
	for i, e := range s.Entries {
		prefix := fmt.Sprintf("entries[%d]", i)
		if _, ok := allowedNamespace[e.Namespace]; !ok {
			return fmt.Errorf("%s.namespace is not a supported namespace", prefix)
		}
		if !tagPattern.MatchString(e.Tag) {
			return fmt.Errorf("%s.tag must be lowercase kebab-case", prefix)
		}
		key := e.Namespace + "\x00" + e.Tag
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate catalog entry %s/%s", e.Namespace, e.Tag)
		}
		seen[key] = struct{}{}
		if err := validateDescription(prefix+".name", e.Name, 1); err != nil {
			return err
		}
		if err := validateDescription(prefix+".summary", e.Summary, 1); err != nil {
			return err
		}
		if e.ReviewStatus == "declared" && len(e.Summary) > MaxSummaryBytes {
			return fmt.Errorf("%s.summary exceeds maximum of %d bytes", prefix, MaxSummaryBytes)
		}
		if err := validateOptionalIdentity(prefix, e); err != nil {
			return err
		}
		switch e.ReviewStatus {
		case "approved", "declared", "draft", "unresolved":
		default:
			return fmt.Errorf("%s.review_status must be approved, draft, or unresolved", prefix)
		}
		lists := []struct {
			name   string
			values []string
		}{
			{"aliases", e.Aliases}, {"owned_components", e.OwnedComponents}, {"boundaries", e.Boundaries},
			{"uses", e.Uses}, {"shared_with", e.SharedWith}, {"positive_examples", e.PositiveExamples},
			{"negative_examples", e.NegativeExamples}, {"evidence_refs", e.EvidenceRefs},
		}
		for _, list := range lists {
			if len(list.values) > 32 {
				return fmt.Errorf("%s.%s exceeds maximum of 32 items", prefix, list.name)
			}
			for j, value := range list.values {
				if err := validateDescription(fmt.Sprintf("%s.%s[%d]", prefix, list.name, j), value, 1); err != nil {
					return err
				}
			}
		}
	}
	b, err := canonicalJSON(s)
	if err != nil {
		return err
	}
	if len(b) > MaxSnapshotBytes {
		return fmt.Errorf("encoded snapshot exceeds maximum of %d bytes", MaxSnapshotBytes)
	}
	return nil
}

// Eligible is the shared publication and read-context predicate. A client
// declaration is usable as declared context without implying human approval.
func Eligible(e Entry) bool {
	return e.ReviewStatus == "approved" || e.ReviewStatus == "declared"
}

func validateOptionalIdentity(prefix string, e Entry) error {
	noIdentity := e.ProjectID == "" && e.ProjectKey == "" && e.Owner == "" && len(e.ClientEvidence) == 0 && e.DeclaredSummary == ""
	if noIdentity {
		switch e.DescriptionSource {
		case "":
			return nil
		case "client_declared", "manual", "model_derived":
			return nil // Safe provider projection may retain provenance alone.
		default:
			return fmt.Errorf("%s.description_source is invalid", prefix)
		}
	}
	if e.Namespace != "projects" || !sha256Pattern.MatchString(e.ProjectID) || !validProjectKey(e.ProjectKey) || strings.TrimSpace(e.Owner) == "" || len(e.Owner) > 128 {
		return fmt.Errorf("%s project identity metadata is invalid", prefix)
	}
	if e.DescriptionSource != "client_declared" && e.DescriptionSource != "manual" && e.DescriptionSource != "model_derived" {
		return fmt.Errorf("%s.description_source is invalid", prefix)
	}
	if e.DeclaredSummary != "" && (len(e.DeclaredSummary) > MaxSummaryBytes || strings.ContainsRune(e.DeclaredSummary, '\x00')) {
		return fmt.Errorf("%s.declared_summary is invalid", prefix)
	}
	if len(e.ClientEvidence) > MaxEvidenceItems {
		return fmt.Errorf("%s.client_evidence exceeds maximum of %d items", prefix, MaxEvidenceItems)
	}
	total := 0
	for i, ev := range e.ClientEvidence {
		if !validEvidenceKind(ev.Kind) || strings.TrimSpace(ev.Text) == "" || len(ev.Text) > MaxEvidenceBytes {
			return fmt.Errorf("%s.client_evidence[%d] is invalid", prefix, i)
		}
		total += len(ev.Text)
	}
	if total > MaxEvidenceBytes {
		return fmt.Errorf("%s.client_evidence exceeds maximum of %d bytes", prefix, MaxEvidenceBytes)
	}
	return nil
}

func validateDescription(field, value string, min int) error {
	if len(strings.TrimSpace(value)) < min || len(value) > MaxDescriptionLen {
		return fmt.Errorf("%s must contain %d to %d bytes", field, min, MaxDescriptionLen)
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s cannot contain NUL", field)
	}
	return nil
}

// Hash returns SHA-256 over deterministic struct-ordered JSON. Array order is
// meaningful and retained; callers must submit the exact reviewed order.
func Hash(s Snapshot) (string, error) {
	if err := Validate(s); err != nil {
		return "", err
	}
	b, err := canonicalJSON(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalJSON(s Snapshot) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(s); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
}

func decodeSnapshot(r io.Reader) (Snapshot, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxSnapshotBytes+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read catalog snapshot: %w", err)
	}
	if len(raw) > MaxSnapshotBytes {
		return Snapshot{}, fmt.Errorf("encoded snapshot exceeds maximum of %d bytes", MaxSnapshotBytes)
	}
	var s Snapshot
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return Snapshot{}, fmt.Errorf("decode catalog snapshot: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Snapshot{}, errors.New("snapshot must contain exactly one JSON value")
	}
	if err := Validate(s); err != nil {
		return Snapshot{}, err
	}
	return cloneSnapshot(s), nil
}

func cloneSnapshot(s Snapshot) Snapshot {
	if s.Entries != nil {
		s.Entries = append([]Entry{}, s.Entries...)
	}
	for i := range s.Entries {
		e := &s.Entries[i]
		e.Aliases = cloneStrings(e.Aliases)
		e.OwnedComponents = cloneStrings(e.OwnedComponents)
		e.Boundaries = cloneStrings(e.Boundaries)
		e.Uses = cloneStrings(e.Uses)
		e.SharedWith = cloneStrings(e.SharedWith)
		e.PositiveExamples = cloneStrings(e.PositiveExamples)
		e.NegativeExamples = cloneStrings(e.NegativeExamples)
		e.EvidenceRefs = cloneStrings(e.EvidenceRefs)
		if e.ClientEvidence != nil {
			e.ClientEvidence = append([]Evidence{}, e.ClientEvidence...)
		}
	}
	return s
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}
