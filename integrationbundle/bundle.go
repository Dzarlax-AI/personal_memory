// Package integrationbundle loads and renders the versioned, client-specific
// Personal Memory integration bundle. It is intentionally filesystem-free:
// installation, update, and rollback belong to a separate layer.
package integrationbundle

import (
	"embed"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

const (
	currentBundleSchema = 1
	currentPolicySchema = 1
	BundleVersion       = "0.1.0"
	ContractVersion     = "1.0.0"
	SuiteVersion        = "1.1.0"
)

var artifactFormatVersions = map[conformance.ClientFamily]string{
	conformance.ClientCodex:      "1.0.0",
	conformance.ClientClaude:     "1.0.0",
	conformance.ClientChatGPT:    "1.0.0",
	conformance.ClientGenericMCP: "1.0.0",
}

var canonicalClientInventories = map[conformance.ClientFamily]ClientManifest{
	conformance.ClientCodex: {ID: conformance.ClientCodex, ArtifactFormatVersion: "1.0.0", Artifacts: []ArtifactOwnership{
		{Path: "codex/AGENTS.personal-memory.md", Template: "templates/codex-agents.md.tmpl"},
		{Path: "codex/skills/personal-memory/SKILL.md", Template: "templates/codex-skill.md.tmpl"},
	}, OverridePaths: []string{"overrides/codex/AGENTS.local.md"}},
	conformance.ClientClaude: {ID: conformance.ClientClaude, ArtifactFormatVersion: "1.0.0", Artifacts: []ArtifactOwnership{
		{Path: "claude/rules/personal-memory.md", Template: "templates/claude-rules.md.tmpl"},
		{Path: "claude/skills/personal-memory/SKILL.md", Template: "templates/claude-skill.md.tmpl"},
		{Path: "claude/settings.personal-memory.json", Template: "templates/claude-settings.json.tmpl"},
	}, OverridePaths: []string{"overrides/claude/personal-memory.local.md"}},
	conformance.ClientChatGPT: {ID: conformance.ClientChatGPT, ArtifactFormatVersion: "1.0.0", Artifacts: []ArtifactOwnership{
		{Path: "chatgpt/behavior-prompt.md", Template: "templates/chatgpt-prompt.md.tmpl"},
		{Path: "chatgpt/remote-mcp-registration.json", Template: "templates/chatgpt-registration.json.tmpl"},
	}, OverridePaths: []string{"overrides/chatgpt/behavior.local.md"}},
	conformance.ClientGenericMCP: {ID: conformance.ClientGenericMCP, ArtifactFormatVersion: "1.0.0", Artifacts: []ArtifactOwnership{
		{Path: "generic-mcp/policy.json", Template: "templates/generic-policy.json.tmpl"},
		{Path: "generic-mcp/tool-mapping.json", Template: "templates/generic-tools.json.tmpl"},
	}, OverridePaths: []string{"overrides/generic-mcp/policy.local.json"}},
}

//go:embed bundle/v1
var publicAssets embed.FS

type SourceIdentity struct {
	ContractSHA256         string `json:"contract_sha256"`
	ConformanceSuiteSHA256 string `json:"conformance_suite_sha256"`
}

type CapabilityMapping struct {
	ID    conformance.Capability `json:"id"`
	Tools []string               `json:"tools"`
}

type ArtifactOwnership struct {
	Path     string `json:"path"`
	Template string `json:"template"`
}

type ClientManifest struct {
	ID                    conformance.ClientFamily `json:"id"`
	ArtifactFormatVersion string                   `json:"artifact_format_version"`
	Artifacts             []ArtifactOwnership      `json:"artifacts"`
	OverridePaths         []string                 `json:"override_paths"`
}

type SourceAsset struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion           int                 `json:"schema_version"`
	BundleVersion           string              `json:"bundle_version"`
	ContractVersion         string              `json:"contract_version"`
	ConformanceSuiteVersion string              `json:"conformance_suite_version"`
	SourceIdentity          SourceIdentity      `json:"source_identity"`
	RequiredCapabilities    []CapabilityMapping `json:"required_capabilities"`
	OptionalCapabilities    []CapabilityMapping `json:"optional_capabilities"`
	Clients                 []ClientManifest    `json:"clients"`
	SourceAssets            []SourceAsset       `json:"source_assets"`
}

type RuleID string

type PolicyRule struct {
	ID                 RuleID                   `json:"id"`
	Text               string                   `json:"text"`
	ScenarioCategories []string                 `json:"scenario_categories"`
	Capabilities       []conformance.Capability `json:"capabilities"`
	Operations         []conformance.Operation  `json:"operations"`
}

type RuleGroup struct {
	ID    string       `json:"id"`
	Rules []PolicyRule `json:"rules"`
}

type RetryRule struct {
	ID                     RuleID                   `json:"id"`
	Text                   string                   `json:"text"`
	MaxAutomaticRetries    int                      `json:"max_automatic_retries"`
	AmbiguousOutcomeAction string                   `json:"ambiguous_outcome_action"`
	ScenarioCategories     []string                 `json:"scenario_categories"`
	Capabilities           []conformance.Capability `json:"capabilities"`
	Operations             []conformance.Operation  `json:"operations"`
}

type TelemetryPolicy struct {
	RuleID                     RuleID   `json:"rule_id"`
	EnabledByDefault           bool     `json:"enabled_by_default"`
	Allowlist                  []string `json:"allowlist"`
	ForbiddenContentCategories []string `json:"forbidden_content_categories"`
	FailureMode                string   `json:"failure_mode"`
}

type ScenarioMapping struct {
	ScenarioID  string      `json:"scenario_id"`
	PolicyRefs  []string    `json:"policy_refs"`
	TraceRecipe TraceRecipe `json:"trace_recipe"`
}

// TraceRecipe is bundle-owned, enum-only artifact-conformance evidence. It is
// deliberately independent from the public suite's expected assertions.
type TraceRecipe struct {
	Observed []conformance.Observation `json:"observed"`
	Events   []conformance.Event       `json:"events"`
}

type Policy struct {
	SchemaVersion         int               `json:"schema_version"`
	BundleVersion         string            `json:"bundle_version"`
	ContractVersion       string            `json:"contract_version"`
	RuleGroups            []RuleGroup       `json:"rule_groups"`
	RetryRules            []RetryRule       `json:"retry_rules"`
	Telemetry             TelemetryPolicy   `json:"telemetry"`
	ScenarioMappings      []ScenarioMapping `json:"scenario_mappings"`
	ScenarioBindingSHA256 string            `json:"scenario_binding_sha256"`
}

func (p Policy) AllRuleIDs() []RuleID {
	ids := make([]RuleID, 0)
	for _, group := range p.RuleGroups {
		for _, rule := range group.Rules {
			ids = append(ids, rule.ID)
		}
	}
	for _, rule := range p.RetryRules {
		ids = append(ids, rule.ID)
	}
	ids = append(ids, p.Telemetry.RuleID)
	return ids
}

type Bundle struct {
	manifest Manifest
	policy   Policy
	suite    conformance.Suite

	manifestSource  []byte
	policySource    []byte
	templates       map[string][]byte
	telemetryTuples map[string]map[string]bool
}

type CapabilityState string

const (
	CapabilityAvailable   CapabilityState = "available"
	CapabilityDisabled    CapabilityState = "disabled"
	CapabilityUnavailable CapabilityState = "unavailable"
)

type CapabilityConfig struct {
	Memory    CapabilityState
	Documents CapabilityState
	Todoist   CapabilityState
}

type Artifact struct {
	Path         string
	Content      []byte
	DigestSHA256 string
}

// ArtifactSet is an in-memory owned artifact set bound to one validated
// capability configuration. DigestSHA256 uses length-prefixed framing over
// ordered artifact paths and content digests. This avoids putting a
// capability-dependent generated-content checksum into its source manifest.
type ArtifactSet struct {
	ClientID               conformance.ClientFamily
	BundleVersion          string
	ContractVersion        string
	ArtifactFormatVersion  string
	Artifacts              []Artifact
	DigestSHA256           string
	CapabilityConfig       CapabilityConfig
	CapabilityConfigSHA256 string
	SourceIdentity         SourceIdentity
	OverridePaths          []string
}

func (b *Bundle) ClientIDs() []conformance.ClientFamily {
	ids := make([]conformance.ClientFamily, len(b.manifest.Clients))
	for i := range b.manifest.Clients {
		ids[i] = b.manifest.Clients[i].ID
	}
	return ids
}

// Manifest returns a deep copy; validated bundle authority is never aliased.
func (b *Bundle) Manifest() Manifest { var out Manifest; cloneJSON(b.manifest, &out); return out }

// Policy returns a deep copy; validated bundle authority is never aliased.
func (b *Bundle) Policy() Policy { var out Policy; cloneJSON(b.policy, &out); return out }
