package integrationbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/Dzarlax-AI/personal-memory/internal/conformance"
)

var (
	policyReference = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	hexDigest       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	windowsDriveAbs = regexp.MustCompile(`(?i)(^|[\s"'=:\(])([a-z]:[\\/])`)
	urlValue        = regexp.MustCompile(`(?i)https?://[^\s"']+`)
	privateEndpoint = regexp.MustCompile(`(?i)(postgres(?:ql)?|ssh|git\+ssh)://[^\s"']+`)
	uriCredentials  = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s/:@]+:[^\s/@]+@`)
	credentialValue = regexp.MustCompile(`(?i)(sk-[a-z0-9]{8,}|ghp_[a-z0-9]{8,}|xox[baprs]-[a-z0-9-]{8,}|bearer[[:space:]]+[a-z0-9._-]{8,}|-----begin[[:space:]][a-z ]*private key-----)`)
	namedCredential = regexp.MustCompile(`(?i)(api[_-]?key|token|password|client_secret)[[:space:]]*[:=][[:space:]]*["']?[^[:space:]"']{8,}`)
	uncPath         = regexp.MustCompile(`\\\\[a-zA-Z0-9._-]+\\[a-zA-Z0-9.$_-]+`)
	posixAbsolute   = regexp.MustCompile(`(^|[[:space:]"'=:,(])/[a-zA-Z0-9._-]+(/[a-zA-Z0-9._-]+)+`)
	privateRoot     = regexp.MustCompile(`(?i)(^|[[:space:]"'=:,(])/(root|users|home|tmp|var|etc)(?:$|[[:space:]"'),.;:/])`)
)

var canonicalRuleGroups = map[string][]RuleID{
	"mandatory_recall":                     {"recall.preference_constraint", "recall.project_context", "recall.continuity", "recall.history", "recall.consequential", "recall.previous_record", "recall.selective_not_trivial"},
	"source_selection":                     {"source.facts", "source.documents", "source.both", "source.self_contained"},
	"storage_inclusions":                   {"storage.fact", "storage.document", "storage.todoist", "storage.ordinary", "storage.ambiguous"},
	"storage_exclusions":                   {"exclude.tasks", "exclude.long_form", "exclude.secrets", "exclude.hidden_reasoning", "exclude.transient", "exclude.fabrication", "exclude.no_retain"},
	"capability_fallbacks":                 {"capability.independent", "fallback.memory", "fallback.documents", "fallback.todoist_no_fact", "fallback.empty_result", "fallback.failure_not_empty"},
	"lifecycle_context_precedence":         {"lifecycle.current", "lifecycle.noncurrent", "lifecycle.similarity_not_authority", "context.current_instruction_precedence", "lifecycle.verify_drift"},
	"result_disclosure_and_adapter_safety": {"result.no_fabricated_success", "result.duplicate", "disclosure.failure_details", "adapter.client_cannot_weaken", "adapter.hooks_no_auto_store"},
}

var canonicalGroupOrder = []string{"mandatory_recall", "source_selection", "storage_inclusions", "storage_exclusions", "capability_fallbacks", "lifecycle_context_precedence", "result_disclosure_and_adapter_safety"}

var canonicalRetryRules = []RetryRule{
	{ID: "retry.fact_recall", MaxAutomaticRetries: 0, AmbiguousOutcomeAction: "disclose_unconfirmed"},
	{ID: "retry.document_search", MaxAutomaticRetries: 1, AmbiguousOutcomeAction: "same_bounded_request"},
	{ID: "retry.read_only", MaxAutomaticRetries: 1, AmbiguousOutcomeAction: "retry_only_when_necessary"},
	{ID: "retry.idempotent_exact_update", MaxAutomaticRetries: 1, AmbiguousOutcomeAction: "preserve_exact_target_and_payload"},
	{ID: "retry.fact_ambiguous_write", MaxAutomaticRetries: 0, AmbiguousOutcomeAction: "verify_or_ask_before_retry"},
	{ID: "retry.task_ambiguous_create", MaxAutomaticRetries: 0, AmbiguousOutcomeAction: "verify_lookup_or_disclose_uncertainty"},
	{ID: "retry.mutation", MaxAutomaticRetries: 0, AmbiguousOutcomeAction: "fresh_decision_or_idempotency_guarantee"},
	{ID: "retry.non_transient_error", MaxAutomaticRetries: 0, AmbiguousOutcomeAction: "disclose_capability_failure"},
}

var canonicalTelemetryAllowlist = []string{"contract_version", "scenario_id", "capability", "operation", "outcome", "latency_bucket", "retry_count", "client_family"}
var canonicalTelemetryForbidden = []string{"prompts_responses_queries", "memory_document_task_content", "identifiers_and_paths", "credentials_users_endpoints_payloads", "vectors_and_hidden_reasoning"}

// Updated alongside bundle/v1/policy.json. It is independent of the manifest
// inventory so a rewritten manifest cannot bless weakened shared rules.
const canonicalPolicySHA256 = "f7c4e9721d36bb1eb8b68f04946da295b07fb5895df536d5113cffbb9491510b"

// EmbeddedSources returns private copies of the normative contract and public
// conformance suite shipped with the standalone integration binary.
func EmbeddedSources() ([]byte, []byte, error) {
	contract, err := publicAssets.ReadFile("bundle/v1/model-usage-contract.md")
	if err != nil {
		return nil, nil, fmt.Errorf("read embedded contract: %w", err)
	}
	suite, err := publicAssets.ReadFile("bundle/v1/scenarios.json")
	if err != nil {
		return nil, nil, fmt.Errorf("read embedded conformance suite: %w", err)
	}
	return append([]byte(nil), contract...), append([]byte(nil), suite...), nil
}

// LoadEmbedded validates and loads the completely self-contained public bundle.
func LoadEmbedded() (*Bundle, error) {
	contract, suite, err := EmbeddedSources()
	if err != nil {
		return nil, err
	}
	return Load(contract, suite)
}

// Load loads embedded public bundle sources and binds them to the exact
// normative contract and public conformance suite supplied by the caller.
func Load(contractSource, suiteSource []byte) (*Bundle, error) {
	manifest, err := publicAssets.ReadFile("bundle/v1/manifest.json")
	if err != nil {
		return nil, fmt.Errorf("read embedded manifest: %w", err)
	}
	policy, err := publicAssets.ReadFile("bundle/v1/policy.json")
	if err != nil {
		return nil, fmt.Errorf("read embedded policy: %w", err)
	}
	templates := map[string][]byte{}
	err = fs.WalkDir(publicAssets, "bundle/v1/templates", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, readErr := publicAssets.ReadFile(name)
		if readErr != nil {
			return readErr
		}
		templates[strings.TrimPrefix(name, "bundle/v1/")] = content
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read embedded templates: %w", err)
	}
	return loadSources(manifest, policy, templates, contractSource, suiteSource)
}

func loadSources(manifestSource, policySource []byte, templates map[string][]byte, contractSource, suiteSource []byte) (*Bundle, error) {
	var manifest Manifest
	if err := decodeStrict(manifestSource, &manifest); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	var policy Policy
	if err := decodeStrict(policySource, &policy); err != nil {
		return nil, fmt.Errorf("decode policy: %w", err)
	}
	suite, err := conformance.LoadSuite(bytes.NewReader(suiteSource))
	if err != nil {
		return nil, err
	}
	b := &Bundle{manifest: manifest, policy: policy, suite: *suite, manifestSource: append([]byte(nil), manifestSource...), policySource: append([]byte(nil), policySource...), templates: cloneBytesMap(templates)}
	b.telemetryTuples = buildTelemetryTuples(suite)
	if err := b.validate(contractSource, suiteSource); err != nil {
		return nil, err
	}
	return b, nil
}

func decodeStrict(source []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

func (b *Bundle) validate(contractSource, suiteSource []byte) error {
	m := &b.manifest
	if m.SchemaVersion != currentBundleSchema {
		return fmt.Errorf("manifest schema_version must be %d", currentBundleSchema)
	}
	if m.BundleVersion != BundleVersion || m.ContractVersion != ContractVersion || m.ConformanceSuiteVersion != SuiteVersion {
		return fmt.Errorf("manifest versions must be bundle %s, contract %s, and suite %s", BundleVersion, ContractVersion, SuiteVersion)
	}
	if digest(contractSource) != m.SourceIdentity.ContractSHA256 {
		return fmt.Errorf("contract source checksum mismatch")
	}
	if digest(suiteSource) != m.SourceIdentity.ConformanceSuiteSHA256 {
		return fmt.Errorf("conformance suite source checksum mismatch")
	}
	catalog, err := conformance.LoadContractCatalog(bytes.NewReader(contractSource))
	if err != nil {
		return fmt.Errorf("load contract catalog: %w", err)
	}
	suite, err := conformance.LoadSuite(bytes.NewReader(suiteSource))
	if err != nil {
		return fmt.Errorf("load conformance suite: %w", err)
	}
	if err := conformance.ValidateCoverage(suite, catalog); err != nil {
		return fmt.Errorf("validate contract coverage: %w", err)
	}
	if catalog.Version != m.ContractVersion || suite.ContractVersion != m.ContractVersion || suite.SuiteVersion != m.ConformanceSuiteVersion {
		return fmt.Errorf("manifest versions do not match contract and conformance suite")
	}
	if err := validateCapabilities(m); err != nil {
		return err
	}
	if err := validateClients(m, b.templates); err != nil {
		return err
	}
	if err := validateSourceAssets(m, b.policySource, b.templates); err != nil {
		return err
	}
	if err := validatePolicy(&b.policy, m, suite); err != nil {
		return err
	}
	if err := validateSourcePrivacy("manifest.json", b.manifestSource); err != nil {
		return err
	}
	if err := validateSourcePrivacy("policy.json", b.policySource); err != nil {
		return err
	}
	for name, source := range b.templates {
		if err := validateSourcePrivacy(name, source); err != nil {
			return err
		}
	}
	return nil
}

func validateCapabilities(m *Manifest) error {
	allowed := map[conformance.Capability]map[string]bool{
		conformance.CapabilityOrdinaryContext: {},
		conformance.CapabilityMemory:          {"recall_facts": true, "store_fact": true, "update_fact": true, "set_fact_lifecycle": true},
		conformance.CapabilityDocuments:       {"search_documents": true},
		conformance.CapabilityTodoist:         {"get_tasks": true, "create_task": true, "update_task": true, "complete_task": true, "delete_task": true},
	}
	if len(m.RequiredCapabilities) != 1 || m.RequiredCapabilities[0].ID != conformance.CapabilityOrdinaryContext || len(m.RequiredCapabilities[0].Tools) != 0 {
		return fmt.Errorf("ordinary_context must be the sole required capability and have no tools")
	}
	if len(m.OptionalCapabilities) != 3 {
		return fmt.Errorf("optional capabilities must contain memory, documents, and todoist exactly once")
	}
	seen := map[conformance.Capability]bool{}
	for _, mapping := range append(append([]CapabilityMapping{}, m.RequiredCapabilities...), m.OptionalCapabilities...) {
		tools, ok := allowed[mapping.ID]
		if !ok {
			return fmt.Errorf("unknown capability %q", mapping.ID)
		}
		if seen[mapping.ID] {
			return fmt.Errorf("duplicate capability %q", mapping.ID)
		}
		seen[mapping.ID] = true
		toolSeen := map[string]bool{}
		for _, tool := range mapping.Tools {
			if !tools[tool] {
				return fmt.Errorf("unknown tool %q for capability %q", tool, mapping.ID)
			}
			if toolSeen[tool] {
				return fmt.Errorf("duplicate tool %q", tool)
			}
			toolSeen[tool] = true
		}
		if len(toolSeen) != len(tools) {
			return fmt.Errorf("capability %q tool mapping is incomplete", mapping.ID)
		}
	}
	return nil
}

func validateClients(m *Manifest, templates map[string][]byte) error {
	want := []conformance.ClientFamily{conformance.ClientCodex, conformance.ClientClaude, conformance.ClientChatGPT, conformance.ClientGenericMCP}
	if len(m.Clients) != len(want) {
		return fmt.Errorf("manifest must contain exactly four supported clients")
	}
	allPaths := map[string]string{}
	seen := map[conformance.ClientFamily]bool{}
	for _, client := range m.Clients {
		_, supported := artifactFormatVersion(client.ID)
		if !supported {
			return fmt.Errorf("unknown client %q", client.ID)
		}
		if seen[client.ID] {
			return fmt.Errorf("duplicate client %q", client.ID)
		}
		seen[client.ID] = true
	}
	for i, client := range m.Clients {
		if client.ID != want[i] {
			return fmt.Errorf("client %d must be %q", i, want[i])
		}
		formatVersion, _ := artifactFormatVersion(client.ID)
		if client.ArtifactFormatVersion != formatVersion {
			return fmt.Errorf("client %q artifact format version must be %q", client.ID, formatVersion)
		}
		if len(client.Artifacts) == 0 || len(client.OverridePaths) == 0 {
			return fmt.Errorf("client %q must declare artifacts and separate override paths", client.ID)
		}
		for _, artifact := range client.Artifacts {
			if err := addSafePath(allPaths, artifact.Path, "owned"); err != nil {
				return err
			}
			if !safeRelativePath(artifact.Template) {
				return fmt.Errorf("unsafe template path %q", artifact.Template)
			}
			if _, ok := templates[artifact.Template]; !ok {
				return fmt.Errorf("template %q is not embedded", artifact.Template)
			}
		}
		for _, override := range client.OverridePaths {
			if err := addSafePath(allPaths, override, "override"); err != nil {
				return err
			}
		}
		if !reflect.DeepEqual(client, canonicalClientInventories[client.ID]) {
			return fmt.Errorf("client %q inventory is not the supported canonical inventory", client.ID)
		}
	}
	return nil
}

func addSafePath(paths map[string]string, candidate, kind string) error {
	if !safeRelativePath(candidate) {
		return fmt.Errorf("unsafe %s path %q", kind, candidate)
	}
	for existing, existingKind := range paths {
		foldedCandidate, foldedExisting := strings.ToLower(candidate), strings.ToLower(existing)
		if foldedCandidate == foldedExisting || strings.HasPrefix(foldedCandidate, foldedExisting+"/") || strings.HasPrefix(foldedExisting, foldedCandidate+"/") {
			return fmt.Errorf("%s path %q overlaps %s path %q", kind, candidate, existingKind, existing)
		}
	}
	paths[candidate] = kind
	return nil
}

func safeRelativePath(value string) bool {
	// This is a portable logical-path contract, not an installer sandbox.
	// Installers must additionally enforce root containment and no-follow
	// symlink traversal, and must never log artifact content or credentials.
	if value == "" || path.IsAbs(value) || windowsDriveAbs.MatchString(value) || strings.HasPrefix(value, `\\`) || strings.Contains(value, `\`) || strings.Contains(value, ":") || path.Clean(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	reserved := map[string]bool{"con": true, "prn": true, "aux": true, "nul": true, "com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true, "lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
			return false
		}
		base := strings.ToLower(strings.SplitN(component, ".", 2)[0])
		if reserved[base] {
			return false
		}
	}
	return true
}

func validateSourceAssets(m *Manifest, policy []byte, templates map[string][]byte) error {
	want := map[string][]byte{"policy.json": policy}
	for name, content := range templates {
		want[name] = content
	}
	if len(m.SourceAssets) != len(want) {
		return fmt.Errorf("source asset inventory is incomplete")
	}
	seen := map[string]bool{}
	for _, asset := range m.SourceAssets {
		if !safeRelativePath(asset.Path) {
			return fmt.Errorf("unsafe source asset path %q", asset.Path)
		}
		if seen[asset.Path] {
			return fmt.Errorf("duplicate source asset %q", asset.Path)
		}
		seen[asset.Path] = true
		content, ok := want[asset.Path]
		if !ok {
			return fmt.Errorf("unknown source asset %q", asset.Path)
		}
		if !hexDigest.MatchString(asset.SHA256) || digest(content) != asset.SHA256 {
			return fmt.Errorf("source asset checksum mismatch for %q", asset.Path)
		}
	}
	return nil
}

func validatePolicy(p *Policy, m *Manifest, suite *conformance.Suite) error {
	if p.SchemaVersion != currentPolicySchema || p.BundleVersion != BundleVersion || p.ContractVersion != ContractVersion || p.BundleVersion != m.BundleVersion || p.ContractVersion != m.ContractVersion {
		return fmt.Errorf("policy schema or exact versions do not match manifest")
	}
	if len(p.RuleGroups) != len(canonicalGroupOrder) {
		return fmt.Errorf("policy rule group inventory mismatch")
	}
	seenRules := map[RuleID]policyRuleMetadata{}
	for i, group := range p.RuleGroups {
		if group.ID != canonicalGroupOrder[i] {
			return fmt.Errorf("policy rule group %d must be %q", i, canonicalGroupOrder[i])
		}
		want := canonicalRuleGroups[group.ID]
		if len(group.Rules) != len(want) {
			return fmt.Errorf("policy rule group %q inventory mismatch", group.ID)
		}
		for j, rule := range group.Rules {
			if rule.ID != want[j] {
				return fmt.Errorf("policy rule %d in %q must be %q", j, group.ID, want[j])
			}
			if err := validatePolicyRule(rule.ID, rule.Text, rule.ScenarioCategories, rule.Capabilities, rule.Operations); err != nil {
				return err
			}
			if _, duplicate := seenRules[rule.ID]; duplicate {
				return fmt.Errorf("duplicate policy rule %q", rule.ID)
			}
			seenRules[rule.ID] = policyRuleMetadata{categories: rule.ScenarioCategories, capabilities: rule.Capabilities, operations: rule.Operations}
		}
	}
	if len(p.RetryRules) != len(canonicalRetryRules) {
		return fmt.Errorf("retry rule inventory mismatch")
	}
	for i, rule := range p.RetryRules {
		want := canonicalRetryRules[i]
		if rule.ID != want.ID || rule.MaxAutomaticRetries != want.MaxAutomaticRetries || rule.AmbiguousOutcomeAction != want.AmbiguousOutcomeAction {
			return fmt.Errorf("retry rule %d does not match canonical %q", i, want.ID)
		}
		if err := validatePolicyRule(rule.ID, rule.Text, rule.ScenarioCategories, rule.Capabilities, rule.Operations); err != nil {
			return err
		}
		if _, duplicate := seenRules[rule.ID]; duplicate {
			return fmt.Errorf("duplicate policy rule %q", rule.ID)
		}
		seenRules[rule.ID] = policyRuleMetadata{categories: rule.ScenarioCategories, capabilities: rule.Capabilities, operations: rule.Operations}
	}
	if p.Telemetry.RuleID != "telemetry.policy" || p.Telemetry.EnabledByDefault || !equalStrings(p.Telemetry.Allowlist, canonicalTelemetryAllowlist) || !equalStrings(p.Telemetry.ForbiddenContentCategories, canonicalTelemetryForbidden) || p.Telemetry.FailureMode != "disable_if_allowlist_cannot_be_guaranteed" {
		return fmt.Errorf("telemetry policy does not match canonical contract allowlist")
	}
	seenRules[p.Telemetry.RuleID] = policyRuleMetadata{categories: []string{"telemetry"}}
	if err := validateScenarioMappings(p, suite); err != nil {
		return err
	}
	if digest(barePolicySource(p)) != canonicalPolicySHA256 {
		return fmt.Errorf("policy content differs from canonical non-overridable policy")
	}
	return nil
}

type policyRuleMetadata struct {
	categories   []string
	capabilities []conformance.Capability
	operations   []conformance.Operation
}

func validatePolicyRule(id RuleID, text string, categories []string, capabilities []conformance.Capability, operations []conformance.Operation) error {
	if !policyReference.MatchString(string(id)) || strings.TrimSpace(text) == "" {
		return fmt.Errorf("invalid policy rule %q", id)
	}
	validCategories := map[string]bool{"recall": true, "store": true, "task": true, "offline": true, "lifecycle": true, "failure": true, "telemetry": true}
	for _, category := range categories {
		if !validCategories[category] {
			return fmt.Errorf("policy rule %q has invalid scenario category %q", id, category)
		}
	}
	validCapabilities := map[conformance.Capability]bool{conformance.CapabilityMemory: true, conformance.CapabilityDocuments: true, conformance.CapabilityTodoist: true, conformance.CapabilityOrdinaryContext: true}
	for _, capability := range capabilities {
		if !validCapabilities[capability] {
			return fmt.Errorf("policy rule %q has invalid capability %q", id, capability)
		}
	}
	validOperations := map[conformance.Operation]bool{conformance.OperationRecall: true, conformance.OperationSearch: true, conformance.OperationStore: true, conformance.OperationTaskList: true, conformance.OperationTaskCreate: true, conformance.OperationTaskUpdate: true, conformance.OperationTaskComplete: true, conformance.OperationTaskDelete: true, conformance.OperationLifecycle: true, conformance.OperationFallback: true}
	for _, operation := range operations {
		if !validOperations[operation] {
			return fmt.Errorf("policy rule %q has invalid operation %q", id, operation)
		}
	}
	return nil
}

func validateScenarioMappings(p *Policy, suite *conformance.Suite) error {
	metadata := map[RuleID]policyRuleMetadata{}
	for _, group := range p.RuleGroups {
		for _, rule := range group.Rules {
			metadata[rule.ID] = policyRuleMetadata{categories: rule.ScenarioCategories, capabilities: rule.Capabilities, operations: rule.Operations}
		}
	}
	for _, rule := range p.RetryRules {
		metadata[rule.ID] = policyRuleMetadata{categories: rule.ScenarioCategories, capabilities: rule.Capabilities, operations: rule.Operations}
	}
	metadata[p.Telemetry.RuleID] = policyRuleMetadata{categories: []string{"telemetry"}}
	scenarios := map[string]conformance.Scenario{}
	for _, scenario := range suite.Scenarios {
		scenarios[scenario.ID] = scenario
	}
	if len(p.ScenarioMappings) != len(scenarios) {
		return fmt.Errorf("policy scenario coverage count mismatch")
	}
	seen := map[string]bool{}
	for _, mapping := range p.ScenarioMappings {
		scenario, ok := scenarios[mapping.ScenarioID]
		if !ok {
			return fmt.Errorf("unknown policy scenario ID %q", mapping.ScenarioID)
		}
		if seen[mapping.ScenarioID] {
			return fmt.Errorf("duplicate policy scenario ID %q", mapping.ScenarioID)
		}
		seen[mapping.ScenarioID] = true
		if len(mapping.PolicyRefs) == 0 {
			return fmt.Errorf("scenario %q has no policy references", mapping.ScenarioID)
		}
		category := strings.ToLower(strings.SplitN(scenario.ID, "-", 2)[0])
		for _, ref := range mapping.PolicyRefs {
			meta, exists := metadata[RuleID(ref)]
			if !exists {
				return fmt.Errorf("scenario %q references unknown policy rule %q", mapping.ScenarioID, ref)
			}
			if !containsString(meta.categories, category) {
				return fmt.Errorf("scenario %q category is incoherent with policy rule %q", mapping.ScenarioID, ref)
			}
		}
		if err := validateTraceRecipe(mapping, scenario, metadata, p.Telemetry.RuleID); err != nil {
			return err
		}
	}
	if p.ScenarioBindingSHA256 != scenarioBindingSignature(p.ScenarioMappings, scenarios) {
		return fmt.Errorf("scenario identity, recipe, and rule binding signature mismatch")
	}
	return nil
}

func validateTraceRecipe(mapping ScenarioMapping, scenario conformance.Scenario, metadata map[RuleID]policyRuleMetadata, telemetryRule RuleID) error {
	recipe := mapping.TraceRecipe
	if len(recipe.Observed) == 0 || len(recipe.Events) == 0 {
		return fmt.Errorf("scenario %q trace recipe must contain observations and events", mapping.ScenarioID)
	}
	trace := conformance.Trace{
		SchemaVersion: conformance.CurrentSchemaVersion, ContractVersion: ContractVersion,
		ScenarioID: mapping.ScenarioID, ClientFamily: conformance.ClientGenericMCP,
		Observed: recipe.Observed, Events: recipe.Events,
	}
	encoded, _ := json.Marshal(trace)
	if _, err := conformance.DecodeTrace(encoded); err != nil {
		return fmt.Errorf("scenario %q trace recipe is invalid: %w", mapping.ScenarioID, err)
	}
	refs := make(map[RuleID]bool, len(mapping.PolicyRefs))
	for _, ref := range mapping.PolicyRefs {
		refs[RuleID(ref)] = true
	}
	observed := make(map[conformance.Observation]bool, len(recipe.Observed))
	for _, observation := range recipe.Observed {
		observed[observation] = true
	}
	pending := map[string]int{}
	for _, event := range recipe.Events {
		if required := recipeEventObservation(event.Event); required != "" && !observed[required] {
			return fmt.Errorf("scenario %q trace recipe event lacks its observation category", mapping.ScenarioID)
		}
		switch event.Event {
		case conformance.EventToolCall, conformance.EventToolResult:
			if !recipeToolAuthorized(refs, metadata, event.Capability, event.Operation) {
				return fmt.Errorf("scenario %q trace recipe operation is not authorized by its policy references", mapping.ScenarioID)
			}
			state, ok := scenario.Capabilities[event.Capability]
			if !ok || state == conformance.CapabilityDisabled || state == conformance.CapabilityUnavailable {
				return fmt.Errorf("scenario %q trace recipe tool event conflicts with capability state", mapping.ScenarioID)
			}
			key := string(event.Capability) + "\x00" + string(event.Operation)
			if event.Event == conformance.EventToolCall {
				pending[key]++
			} else {
				if pending[key] == 0 {
					return fmt.Errorf("scenario %q trace recipe tool result has no preceding call", mapping.ScenarioID)
				}
				pending[key]--
				if state == conformance.CapabilityTimeout && event.Outcome != conformance.OutcomeTimeout {
					return fmt.Errorf("scenario %q trace recipe result conflicts with timeout capability state", mapping.ScenarioID)
				}
			}
		case conformance.EventCapability:
			if !recipeCapabilityAuthorized(refs, metadata, event.Capability) {
				return fmt.Errorf("scenario %q trace recipe capability is not authorized by its policy references", mapping.ScenarioID)
			}
			state, ok := scenario.Capabilities[event.Capability]
			if !ok || event.Outcome != conformance.OutcomeUnavailable || state == conformance.CapabilityAvailable || state == conformance.CapabilityTimeout {
				return fmt.Errorf("scenario %q trace recipe capability event conflicts with capability state", mapping.ScenarioID)
			}
		case conformance.EventFallback:
			if !recipeCapabilityAuthorized(refs, metadata, conformance.CapabilityOrdinaryContext) {
				return fmt.Errorf("scenario %q trace recipe fallback is not authorized by its policy references", mapping.ScenarioID)
			}
		case conformance.EventClaim, conformance.EventDisclosure, conformance.EventArtifact:
			if !recipeCodeAuthorized(refs, event.Code, telemetryRule) {
				return fmt.Errorf("scenario %q trace recipe code is not authorized by its policy references", mapping.ScenarioID)
			}
		}
	}
	for _, count := range pending {
		if count != 0 {
			return fmt.Errorf("scenario %q trace recipe tool calls and results are not paired", mapping.ScenarioID)
		}
	}
	return nil
}

func recipeEventObservation(kind conformance.EventKind) conformance.Observation {
	switch kind {
	case conformance.EventCapability:
		return conformance.ObservationCapabilities
	case conformance.EventToolCall, conformance.EventToolResult:
		return conformance.ObservationToolEvents
	case conformance.EventClaim, conformance.EventDisclosure, conformance.EventFallback:
		return conformance.ObservationUserVisibleClaims
	case conformance.EventArtifact:
		return conformance.ObservationArtifacts
	default:
		return ""
	}
}

func recipeToolAuthorized(refs map[RuleID]bool, metadata map[RuleID]policyRuleMetadata, capability conformance.Capability, operation conformance.Operation) bool {
	for ref := range refs {
		meta := metadata[ref]
		if containsCapability(meta.capabilities, capability) && containsOperation(meta.operations, operation) {
			return true
		}
	}
	return false
}

func recipeCapabilityAuthorized(refs map[RuleID]bool, metadata map[RuleID]policyRuleMetadata, capability conformance.Capability) bool {
	for ref := range refs {
		if containsCapability(metadata[ref].capabilities, capability) {
			return true
		}
	}
	return false
}

func recipeCodeAuthorized(refs map[RuleID]bool, code conformance.EventCode, telemetryRule RuleID) bool {
	allowed := map[conformance.EventCode][]RuleID{
		conformance.CodeFactFound:               {"source.facts", "source.both"},
		conformance.CodeDocumentEvidence:        {"source.documents", "source.both"},
		conformance.CodeOrdinaryResponse:        {"source.self_contained", "storage.ordinary"},
		conformance.CodeNoRelevantFact:          {"fallback.empty_result"},
		conformance.CodeWriteConfirmed:          {"storage.fact"},
		conformance.CodeDocumentRouted:          {"storage.document"},
		conformance.CodeSecretRejected:          {"exclude.secrets"},
		conformance.CodeTaskCreated:             {"storage.todoist"},
		conformance.CodeClarificationRequested:  {"storage.ambiguous"},
		conformance.CodeWriteDuplicate:          {"result.duplicate"},
		conformance.CodeWriteUnconfirmed:        {"retry.fact_ambiguous_write", "retry.task_ambiguous_create", "retry.mutation"},
		conformance.CodeTaskNotCreated:          {"fallback.todoist_no_fact"},
		conformance.CodeMemoryNotChecked:        {"fallback.memory", "disclosure.failure_details", "fallback.failure_not_empty"},
		conformance.CodeDocumentsNotSearched:    {"fallback.documents"},
		conformance.CodeCurrentFactUsed:         {"lifecycle.current", "lifecycle.verify_drift"},
		conformance.CodeHistoricalFactUsed:      {"lifecycle.noncurrent"},
		conformance.CodeLifecycleUncertain:      {"lifecycle.similarity_not_authority"},
		conformance.CodeCurrentInstructionUsed:  {"context.current_instruction_precedence"},
		conformance.CodeExplicitLifecycleChange: {"context.current_instruction_precedence"},
		conformance.CodeUnverifiedFact:          {"lifecycle.verify_drift"},
		conformance.CodeTelemetryAllowed:        {telemetryRule},
		conformance.CodeTelemetryDisabled:       {telemetryRule},
	}
	for _, ref := range allowed[code] {
		if refs[ref] {
			return true
		}
	}
	return false
}

func containsCapability(values []conformance.Capability, want conformance.Capability) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsOperation(values []conformance.Operation, want conformance.Operation) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func scenarioIdentitySignature(scenario conformance.Scenario) string {
	payload := struct {
		IntentClass    string                                                 `json:"intent_class"`
		SyntheticInput string                                                 `json:"synthetic_input"`
		Capabilities   map[conformance.Capability]conformance.CapabilityState `json:"capabilities"`
	}{scenario.IntentClass, scenario.SyntheticInput, scenario.Capabilities}
	encoded, _ := json.Marshal(payload)
	return digest(encoded)
}

func scenarioBindingSignature(mappings []ScenarioMapping, scenarios map[string]conformance.Scenario) string {
	type binding struct {
		ScenarioID        string      `json:"scenario_id"`
		IdentitySignature string      `json:"identity_signature"`
		PolicyRefs        []string    `json:"policy_refs"`
		TraceRecipe       TraceRecipe `json:"trace_recipe"`
	}
	bindings := make([]binding, 0, len(mappings))
	for _, mapping := range mappings {
		bindings = append(bindings, binding{ScenarioID: mapping.ScenarioID, IdentitySignature: scenarioIdentitySignature(scenarios[mapping.ScenarioID]), PolicyRefs: mapping.PolicyRefs, TraceRecipe: mapping.TraceRecipe})
	}
	encoded, _ := json.Marshal(bindings)
	return digest(encoded)
}

func validateSourcePrivacy(name string, source []byte) error {
	// Best-effort, high-confidence defense in depth only. This is not a
	// secrets boundary: installers must never log source or artifact content.
	text := string(source)
	if urlValue.MatchString(text) || privateEndpoint.MatchString(text) || uriCredentials.MatchString(text) || windowsDriveAbs.MatchString(text) || uncPath.MatchString(text) || posixAbsolute.MatchString(text) || privateRoot.MatchString(text) || strings.Contains(text, "../") || strings.Contains(text, `..\`) || credentialValue.MatchString(text) || namedCredential.MatchString(text) {
		return fmt.Errorf("source asset %q contains endpoint, credential, absolute path, UNC path, or traversal", name)
	}
	return nil
}

func barePolicySource(p *Policy) []byte { data, _ := json.Marshal(p); return data }
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
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func digest(content []byte) string { sum := sha256.Sum256(content); return hex.EncodeToString(sum[:]) }

func cloneJSON(source, target any) {
	encoded, _ := json.Marshal(source)
	_ = json.Unmarshal(encoded, target)
}

func cloneBytesMap(source map[string][]byte) map[string][]byte {
	result := make(map[string][]byte, len(source))
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result[key] = append([]byte(nil), source[key]...)
	}
	return result
}
