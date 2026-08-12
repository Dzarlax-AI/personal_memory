package integrationbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

var canonicalInstructionPaths = map[conformance.ClientFamily][]string{
	conformance.ClientCodex:      {"codex/AGENTS.personal-memory.md", "codex/skills/personal-memory/SKILL.md"},
	conformance.ClientClaude:     {"claude/rules/personal-memory.md", "claude/skills/personal-memory/SKILL.md"},
	conformance.ClientChatGPT:    {"chatgpt/behavior-prompt.md"},
	conformance.ClientGenericMCP: {"generic-mcp/policy.json"},
}

var markdownPolicyPayload = regexp.MustCompile(`(?m)^<!-- PERSONAL_MEMORY_CANONICAL_POLICY_BASE64 ([A-Za-z0-9+/=]+) -->$`)

type templateData struct {
	BundleVersion           string
	ContractVersion         string
	Memory                  CapabilityState
	Documents               CapabilityState
	Todoist                 CapabilityState
	CanonicalPolicyMarkdown string
	CanonicalPolicyJSON     string
	CanonicalPolicyBase64   string
	MemoryToolsJSON         string
	DocumentsToolsJSON      string
	TodoistToolsJSON        string
}

func (b *Bundle) Render(config CapabilityConfig) ([]ArtifactSet, error) {
	if b == nil {
		return nil, fmt.Errorf("bundle must not be nil")
	}
	if !validConfigState(config.Memory) || !validConfigState(config.Documents) || !validConfigState(config.Todoist) {
		return nil, fmt.Errorf("capability configuration contains invalid state")
	}
	sets, err := b.renderUnchecked(config)
	if err != nil {
		return nil, err
	}
	if err := b.ValidateRendered(sets); err != nil {
		return nil, err
	}
	return sets, nil
}

func (b *Bundle) renderUnchecked(config CapabilityConfig) ([]ArtifactSet, error) {
	policyJSON, err := json.Marshal(b.policy)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical policy: %w", err)
	}
	memoryTools, documentsTools, todoistTools, err := b.capabilityToolJSON()
	if err != nil {
		return nil, err
	}
	data := templateData{
		BundleVersion: b.manifest.BundleVersion, ContractVersion: b.manifest.ContractVersion,
		Memory: config.Memory, Documents: config.Documents, Todoist: config.Todoist,
		CanonicalPolicyMarkdown: renderCanonicalPolicyMarkdown(b.policy), CanonicalPolicyJSON: string(policyJSON), CanonicalPolicyBase64: base64.StdEncoding.EncodeToString(policyJSON),
		MemoryToolsJSON: memoryTools, DocumentsToolsJSON: documentsTools, TodoistToolsJSON: todoistTools,
	}
	configDigest := capabilityConfigDigest(config)
	sets := make([]ArtifactSet, 0, len(b.manifest.Clients))
	for _, client := range b.manifest.Clients {
		set := ArtifactSet{ClientID: client.ID, BundleVersion: b.manifest.BundleVersion, ContractVersion: b.manifest.ContractVersion, ArtifactFormatVersion: client.ArtifactFormatVersion, Artifacts: make([]Artifact, 0, len(client.Artifacts)), CapabilityConfig: config, CapabilityConfigSHA256: configDigest, SourceIdentity: b.manifest.SourceIdentity, OverridePaths: append([]string(nil), client.OverridePaths...)}
		for _, owned := range client.Artifacts {
			content, err := b.renderTemplate(owned.Template, data)
			if err != nil {
				return nil, err
			}
			set.Artifacts = append(set.Artifacts, Artifact{Path: owned.Path, Content: content, DigestSHA256: digest(content)})
		}
		set.DigestSHA256 = artifactSetDigest(set.Artifacts)
		sets = append(sets, set)
	}
	return sets, nil
}

func renderCanonicalPolicyMarkdown(policy Policy) string {
	var out strings.Builder
	out.WriteString("<!-- BEGIN PERSONAL_MEMORY_CANONICAL_POLICY -->\n")
	for _, group := range policy.RuleGroups {
		out.WriteString("### ")
		out.WriteString(group.ID)
		out.WriteByte('\n')
		for _, rule := range group.Rules {
			fmt.Fprintf(&out, "- [%s] %s\n", rule.ID, rule.Text)
		}
	}
	out.WriteString("### retry_rules\n")
	for _, rule := range policy.RetryRules {
		fmt.Fprintf(&out, "- [%s] %s Max automatic retries: %d. Ambiguous outcome: %s.\n", rule.ID, rule.Text, rule.MaxAutomaticRetries, rule.AmbiguousOutcomeAction)
	}
	fmt.Fprintf(&out, "- [%s] Telemetry default enabled: %t. Exact allowlist: %s. Forbidden content categories: %s. Failure mode: %s.\n", policy.Telemetry.RuleID, policy.Telemetry.EnabledByDefault, strings.Join(policy.Telemetry.Allowlist, ","), strings.Join(policy.Telemetry.ForbiddenContentCategories, ","), policy.Telemetry.FailureMode)
	out.WriteString("<!-- END PERSONAL_MEMORY_CANONICAL_POLICY -->\n")
	return out.String()
}

func (b *Bundle) ValidateRendered(sets []ArtifactSet) error {
	if b == nil {
		return fmt.Errorf("bundle must not be nil")
	}
	if len(sets) != len(b.manifest.Clients) {
		return fmt.Errorf("rendered client count mismatch")
	}
	byPath := map[string][]byte{}
	for i, set := range sets {
		client := b.manifest.Clients[i]
		if set.ClientID != client.ID || set.BundleVersion != b.manifest.BundleVersion || set.ContractVersion != b.manifest.ContractVersion || set.ArtifactFormatVersion != client.ArtifactFormatVersion {
			return fmt.Errorf("rendered metadata mismatch for client %q", client.ID)
		}
		if len(set.Artifacts) != len(client.Artifacts) {
			return fmt.Errorf("rendered artifact count mismatch for client %q", client.ID)
		}
		for j, artifact := range set.Artifacts {
			if artifact.Path != client.Artifacts[j].Path {
				return fmt.Errorf("rendered artifact path %q is not manifest-owned", artifact.Path)
			}
			if artifact.DigestSHA256 != digest(artifact.Content) {
				return fmt.Errorf("rendered artifact checksum mismatch for %q", artifact.Path)
			}
			byPath[artifact.Path] = artifact.Content
		}
		if set.DigestSHA256 != artifactSetDigest(set.Artifacts) {
			return fmt.Errorf("rendered artifact set checksum mismatch for client %q", client.ID)
		}
		if !validConfigState(set.CapabilityConfig.Memory) || !validConfigState(set.CapabilityConfig.Documents) || !validConfigState(set.CapabilityConfig.Todoist) || set.CapabilityConfigSHA256 != capabilityConfigDigest(set.CapabilityConfig) {
			return fmt.Errorf("invalid capability configuration identity for client %q", client.ID)
		}
		if i > 0 && (set.CapabilityConfig != sets[0].CapabilityConfig || set.CapabilityConfigSHA256 != sets[0].CapabilityConfigSHA256) {
			return fmt.Errorf("artifact sets do not share one capability configuration")
		}
	}
	want := b.policy.AllRuleIDs()
	for client, paths := range canonicalInstructionPaths {
		for _, artifactPath := range paths {
			policy, err := embeddedCanonicalPolicy(byPath[artifactPath])
			if err != nil {
				return fmt.Errorf("semantic policy coverage for %s/%s: %w", client, artifactPath, err)
			}
			if !reflect.DeepEqual(policy, b.policy) {
				return fmt.Errorf("canonical policy in %s/%s does not deep-equal bundle policy", client, artifactPath)
			}
			got := policy.AllRuleIDs()
			if !reflect.DeepEqual(got, want) {
				return fmt.Errorf("semantic policy coverage for %s/%s is incomplete or reordered", client, artifactPath)
			}
		}
	}
	for _, client := range b.manifest.Clients {
		for _, owned := range client.Artifacts {
			if !b.matchesAllowedWrapper(owned.Template, byPath[owned.Path]) {
				return fmt.Errorf("rendered wrapper for %s is not allowlisted", owned.Path)
			}
		}
	}
	expected, err := b.renderUnchecked(sets[0].CapabilityConfig)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(sets, expected) {
		return fmt.Errorf("artifact sets do not match the recorded capability configuration")
	}
	return nil
}

func embeddedCanonicalPolicy(content []byte) (Policy, error) {
	var document struct {
		CanonicalPolicy json.RawMessage `json:"canonical_policy"`
	}
	if json.Unmarshal(content, &document) == nil && len(document.CanonicalPolicy) != 0 {
		var policy Policy
		if err := decodeStrict(document.CanonicalPolicy, &policy); err != nil {
			return Policy{}, err
		}
		return policy, nil
	}
	match := markdownPolicyPayload.FindSubmatch(content)
	if len(match) != 2 {
		return Policy{}, fmt.Errorf("canonical policy payload missing")
	}
	decoded, err := base64.StdEncoding.DecodeString(string(match[1]))
	if err != nil {
		return Policy{}, err
	}
	var policy Policy
	if err := decodeStrict(decoded, &policy); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func (b *Bundle) renderTemplate(name string, data templateData) ([]byte, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(string(b.templates[name]))
	if err != nil {
		return nil, fmt.Errorf("parse template %q: %w", name, err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, data); err != nil {
		return nil, fmt.Errorf("render template %q: %w", name, err)
	}
	return append([]byte(nil), rendered.Bytes()...), nil
}

func (b *Bundle) matchesAllowedWrapper(templateName string, content []byte) bool {
	policyJSON, _ := json.Marshal(b.policy)
	policyMarkdown := renderCanonicalPolicyMarkdown(b.policy)
	policyBase64 := base64.StdEncoding.EncodeToString(policyJSON)
	memoryTools, documentsTools, todoistTools, err := b.capabilityToolJSON()
	if err != nil {
		return false
	}
	states := []CapabilityState{CapabilityAvailable, CapabilityDisabled, CapabilityUnavailable}
	for _, memory := range states {
		for _, documents := range states {
			for _, todoist := range states {
				data := templateData{BundleVersion: b.manifest.BundleVersion, ContractVersion: b.manifest.ContractVersion, Memory: memory, Documents: documents, Todoist: todoist, CanonicalPolicyMarkdown: policyMarkdown, CanonicalPolicyJSON: string(policyJSON), CanonicalPolicyBase64: policyBase64, MemoryToolsJSON: memoryTools, DocumentsToolsJSON: documentsTools, TodoistToolsJSON: todoistTools}
				expected, err := b.renderTemplate(templateName, data)
				if err == nil && bytes.Equal(expected, content) {
					return true
				}
			}
		}
	}
	return false
}

func semanticRuleIDs(content []byte) ([]RuleID, error) {
	policy, err := embeddedCanonicalPolicy(content)
	if err != nil {
		return nil, err
	}
	return policy.AllRuleIDs(), nil
}

func validConfigState(state CapabilityState) bool {
	return state == CapabilityAvailable || state == CapabilityDisabled || state == CapabilityUnavailable
}

func artifactSetDigest(artifacts []Artifact) string {
	ordered := append([]Artifact(nil), artifacts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	h := sha256.New()
	for _, artifact := range ordered {
		writeFramed(h, []byte(artifact.Path))
		writeFramed(h, []byte(artifact.DigestSHA256))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeFramed(h io.Writer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(value)
}

func (b *Bundle) capabilityToolJSON() (string, string, string, error) {
	tools := map[conformance.Capability][]string{}
	for _, mapping := range b.manifest.OptionalCapabilities {
		tools[mapping.ID] = mapping.Tools
	}
	encode := func(capability conformance.Capability) (string, error) {
		data, err := json.Marshal(tools[capability])
		if err != nil {
			return "", fmt.Errorf("marshal %s capability tools: %w", capability, err)
		}
		return string(data), nil
	}
	memory, err := encode(conformance.CapabilityMemory)
	if err != nil {
		return "", "", "", err
	}
	documents, err := encode(conformance.CapabilityDocuments)
	if err != nil {
		return "", "", "", err
	}
	todoist, err := encode(conformance.CapabilityTodoist)
	if err != nil {
		return "", "", "", err
	}
	return memory, documents, todoist, nil
}

func capabilityConfigDigest(config CapabilityConfig) string {
	encoded, _ := json.Marshal(config)
	return digest(encoded)
}
