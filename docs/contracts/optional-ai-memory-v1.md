# Optional AI Memory Contract v1

Status: implementation contract for approved plan revision 1 (2026-10-07). Runtime behavior remains opt-in and experimental pending independent quality evidence. Baseline revision: 484b6427ce8c5e6f07b3c5a0fa0e158126c74110 plus private baseline hashes in eval-results/optional-ai-memory-v1/baseline.json.

## R2 implementation amendment (approved 2026-10-08)

The user approved execution of R2-A–F. This amendment adds client-declared project registration and explicit recall context. Runtime model calls, real client installation, production catalog publication and deploy are outside this engineering approval.

- Registry API: `Evidence {Kind, Text string}`, `RegistrationInput {ProjectKey, Namespace, Name, Tag, Summary string; Evidence []Evidence}`, `RegistrationResult {Status, ProjectID, Tag, CatalogHash string}`, `EnsureProject(dir, owner string, input RegistrationInput) (RegistrationResult,error)` and `ResolveProject(dir, owner, projectID string) (Entry,string,error)`. JSON snake_case; evidence kinds client_readme/client_agents/user_declared. Owner is server-configured, never caller-selected. Namespace projects only. Current server authenticates a deployment-wide memory owner (`MEMORY_USER`); no per-OAuth-user multitenancy is implied by this amendment.
- Optional Entry metadata: project_id/project_key/owner/client_evidence/description_source/declared_summary, omitted for legacy snapshots so their hashes remain stable. `Eligible(Entry)` admits approved and declared descriptions; a declared description is not verified ownership. Registration does not pretend operator approval or promote semantic fact authority.
- Limits: serialized registration ≤8KiB, summary ≤2KiB, maximum four evidence excerpts totaling ≤4KiB, bounded identities/name/tag, existing max32 entries / 64KiB snapshot. Reject obvious private paths and credential material; clients must minimize/sanitize inputs. Structural validation is not proof that arbitrary text is secret-free or semantically correct.
- Stable identity: deterministic scoped owner + namespace + project_key; no semantic/alias merging. Worktree/path move reuse the client key. Tag collision or identity conflict returns ambiguous. Changed description returns proposed_update and leaves the active entry untouched. Creation and updates use private immutable snapshots, process lock and compare-before-publication. Retry only local publication conflicts before a mutation; ambiguous filesystem outcomes must not be called success.
- Registration mode `off|client_declared`, default off, works without any AI profile/budget/secret. Tool exposure advertises capability when enabled. Clients call ensure_project before the first relevant memory operation with explicit project context; repeated calls are idempotent. Readers do not mutate the registry. Old clients and missing/unavailable registration retain baseline memory. A registration failure never authorizes a fabricated recording origin.
- Recall accepts optional project_context_id. Resolve it under server owner; invalid/foreign identity is rejected. It is context for optional relevance only, never a namespace/tag filter, lifecycle/authority hint or wider retrieval pool. Send only an eligible safe description, not local identity/evidence paths; apply provider egress to the context too. Context/catalog generation partitions derived cache identity. Search_documents is unchanged except client-side preceding registration.
- Maintenance consumes eligible declared cards and scoped client evidence as untrusted excerpts. Preserve the immutable original declared_summary and private alias→card/evidence digest dependencies and exact fact dependencies. Model-derived descriptions must not become evidence for subsequent changes; registration compares the original declaration after enrichment. Model output cannot replace identity/owner/key/name/tag or grant approved state. All grouping remains reviewed manual stopped-writer application.
- Description publication policy review (default) or evidence_bounded (explicit opt-in). Automatic publication only of descriptive fields on existing identities, with supplied evidence, intact dependencies, no missing_evidence/conflicts and unchanged full parent hash. Structural evidence checks do not prove semantic truth. Model confidence does not grant authority. No new identities, deletions, renames, merges or fact grouping from descriptive publication. Store the proposal before publication; rejected/ambiguous publication leaves the previous active generation recoverable.
- Client bundle requires a version/hash update and conformance scenarios for registration capability and fallback. Installing this updated bundle into a real client is a separate action. Synthetic conformance is not evidence of live proprietary-client behavior.

Proposed executor allocation: Luna/high for registry and bundle, Sol/high for server/config/context integration, Luna/high for bounded maintenance updates after registry API readiness; separate Sol/high acceptance review. Coordinator freezes contracts, owns shared documentation and verifies combined behavior. Names denote confirmed available models gpt-6-luna and gpt-6.1-sol; the running coordinator ID is not verified.

## R1 package boundaries (superseded where amended by R2)

- `internal/contextcatalog`: `Entry` (Namespace, Tag, Name, Summary, Aliases, OwnedComponents, Boundaries, Uses, SharedWith, PositiveExamples, NegativeExamples, EvidenceRefs, ReviewStatus), `Snapshot` (SchemaVersion int, Version string, ParentHash string, CreatedAt string, Entries []Entry). JSON fields use snake_case. Approved entries only in a published snapshot. `Validate(Snapshot) error`, `Hash(Snapshot) (string,error)`, `LoadActive(dir string) (Snapshot,string,error)`, `Publish(dir string, snapshot Snapshot, expectedActiveHash string) (string,error)`. Max 32 entries, 64KiB encoded snapshot, max 4KiB per description, known five namespaces, lowercase kebab-case tags. Atomic immutable snapshots and active pointer. `Publish` requires base hash equality, creates parent dirs 0700/files0600, must reject symlinks. Empty expected hash means initial publication, not overwrite. Reader returns validated private copy.
- `internal/aijudgment`: `Origin {SourceProject, SourceKind, RecordedAt string}`. `ClassifyInput {FactText, Namespace string; Origin *Origin; Catalog contextcatalog.Snapshot}`; `ClassifyResult {Status, PrimaryTag string; RelatedTags []string; MissingContextCodes []string}`. `Candidate {Alias, Text, Namespace string; Tags []string; AuthorityBucket string}`; `RankInput {Query, LifecycleMode string; Catalog *contextcatalog.Snapshot; Candidates []Candidate}`; `RankResult {Status string; Scores map[string]float64; NoneRelevant bool}`. JSON snake_case. Status decided/abstained/unavailable/invalid. `Usage {InputTokens, OutputTokens int64; Known bool}` returned separately from each call.
- `aijudgment.Provider` exposes `Classify(context.Context,ClassifyInput) (ClassifyResult,Usage,error)` and `Rank(context.Context,RankInput) (RankResult,Usage,error)`. `New(provider string, Config) (Provider,error)`: Config {Model,Endpoint,APIKey string; Transport http.RoundTripper; MaxRequestBytes,MaxResponseBytes int; Timeout time.Duration}. No retry, no redirect; all callers validate exact candidate/project membership, complete responses and finite numbers. Pure request builders exported for payload preview. Contexts/probabilities are local aliases; no original IDs.
- Maintenance provider uses a generative protocol, not Decisions or Jev. Maintenance proposals only, never tools or direct mutations. Protocol/model/base URL configured by operator.
- Shared config/core policy/root integration is owned by coordinator. No executor edits existing server/config/qdrant/shared docs unless assigned.

## Behavior invariants

1. All three features off by default; none of the inactive profile keys are read. Manual catalog works without AI.
2. Explicit caller primary and legacy one-tag primary promotion win. Only project grouping of a new projects fact with absent primary may be inferred. Existing update/import does not infer.
3. Origin declaration is separate from subject; source_kind user_declared/client_declared. No reconstruction on old facts. Creation timestamp server-generated. Preserve on update; additive import/export.
4. Read candidates are existing eligible semantic pool, AI cap20; no widening. Sort only within equal existing lifecycle authority bucket: canonical current, other current, disputed, historical, superseded (verified existing lifecycle.rankTier). Preserve semantic scores and stable tie order. Invalid/missing/partial score, none-relevant or changed metadata falls back to saved baseline B as a whole.
5. No metadata authority from model confidence. Classify abstention persists no fabricated project tag. Return outcomes separate from stored/duplicate.
6. Egress allowed only for configured namespace+existing project tags, or explicitly permitted unassigned in that namespace. Query/catalog also private. One disallowed candidate rejects whole rank request. No provider failover.
7. Budget spans queue+serialize+HTTP; read/write1s, no retry. Request≤64KiB, response≤64KiB. Semaphore bounded. Offline tests use synthetic input and fake transports only.
8. Worker interval explicitly configured, no initial inference on startup; concurrency1, bounded sweeps, semantic fingerprints exclude recall counters. Published catalog remains manual initially.
9. Grouping apply exact-ID manifest, stopped writers, semantic fingerprint compare; touch only tags/primary/audit reference, no vector/text/namespace/lifecycle updates. Journal intent before dispatch; unknown status stays ambiguous. No automatic mutation retry. Rollback compare-after before restore.
10. No real fact/model calls, install, commit, push, PR or deployment from implementation delegates. Reports include owned diff, commands, outcomes, limitations. Tests and isolated checks are authorized by plan A0–G.

## Integration source evidence

Existing `internal/memory/server.go` storeFact normalizes metadata, embeds before mutation lock and handles duplicate/upsert. recallFacts acquires cache leader, produces lifecycle candidates before limit, counts actual returned records once. `internal/memory/lifecycle.SortCandidates` defines authority comparator; runtime preserves it. Existing maintenance service has quarantine/restore/purge only. `qdrant.Client.SetPayload` exists; a narrow grouping wrapper will validate keys and use strong wait acknowledgement. No payload CAS is available: stopped-writer apply is required. Existing evaluator transports are research-only and must remain unchanged.

## Implementation decisions recorded during acceptance

- Maintenance `output_mode` is `schema` by default; explicit `json` supports compatible models without structured-schema support. Both paths require the complete proposal shape and identical semantic validation. No automatic downgrade or retry occurs.
- Runtime ON reads bypass derived caching. Shadow reads sample cache misses. After a dispatched judgment, all candidate dependencies are re-read; a changed fact yields the refreshed eligible baseline within the original semantic pool. Unverifiable freshness returns a generic error. This safety I/O uses the caller context after the optional one-second inference budget.
- Shared daily reservations are 65,536 input tokens, or 131,072 with maintenance enabled. Corrupt existing ledgers disable inference; implausible usage exhausts that day's budget without integer overflow.
- Grouping manifests include a valid catalog SHA-256 and exact semantic fingerprints. Per-journal-directory locks serialize local operations; these are not distributed Qdrant CAS. Explicit stopped writers remain mandatory. Narrow payload mutation uses `wait=true&ordering=strong`.
- Maintenance proposals retain private alias-to-original-version dependencies for every selected fact. Catalog evidence references must resolve to supplied aliases. Provider input never contains local point IDs, semantic fingerprints, or local evidence references.
- Offline paired scoring accepts normalized, request-hash-bound outcomes without any provider dispatch. Missing and invalid outcomes remain in the denominator. Author-labelled synthetic fixtures establish harness behavior, not model accuracy.

## Source-only pilot contract repair

The first two authorized maintenance calls produced complete HTTP responses but failed strict proposal acceptance. Production request building now supplies the exact validated catalog timestamp and clarifies same-card catalog-evidence scope. The output schema limits `missing_evidence` to the existing five validator codes, `review_status` to `draft|unresolved`, and both schema versions to `1`, in schema and JSON modes. Validation remains strict; responses are never silently normalized. No new provider dispatch or publication is implied by this local repair.

## Ownership boundary clarification (2026-10-08)

- `owned_components` describes implementation or configuration actually owned by the repository. Hosting a product does not establish ownership of its implementation. Infrastructure descriptions must qualify service entries as deployment configuration, network/proxy policy, or backup configuration where the source establishes that scope. Bare deployed product names are insufficient ownership descriptions.
- `boundaries` distinguishes product-specific behavior from shared infrastructure operations. A named outside-catalog product remains outside the catalog; deployment in a known infrastructure repository does not authorize assigning all its facts to that repository. A cross-service operation may concern infrastructure when the assertion and source evidence establish that scope. An ambiguous primary subject remains unresolved.
- Service inventories alone do not establish `uses` or `shared_with` relationships, or implementation ownership. Dependencies require direct supplied evidence.
- This clarification changes maintenance instructions only. The existing catalog fields, scoped evidence aliases, strict validators, identity preservation, publication gates and disabled-by-default behavior remain in force. Semantic compliance requires source review; the structural validator cannot prove ownership from prose.
- Retain the previous raw model proposal and comparison unchanged. A revised proposal is a separate generation and must be evaluated against frozen references; it is not a manual correction of old model output.

## R3 subject and origin boundary (2026-10-08)

Store/update accept optional `subject_scope` (`project`, `non_project`, `unknown`)
and `subject_context` (at most 2048 UTF-8 bytes). Context without scope defaults to
unknown. Subject metadata is a client declaration, distinct from recording origin
and namespace. An explicit namespace is never changed by classification. Updating
fact text without replacing subject metadata clears stale subject context;
unchanged text preserves it. Import validates subject metadata; recall/export
retain it.

Personal/tech/job-search and explicit non-project writes bypass project inference.
A scope/namespace mismatch preserves baseline storage and reports
`namespace_scope_mismatch`. The extended classification contract distinguishes
`non_project`, `multiple_projects`, `other_project`, `insufficient_context`, and a
known project. Abstention never invents a primary tag. Legacy provider requests
without subject fields retain their historical wire shape.

Runtime synchronous and shadow classification omit recording origin. Origin
remains stored provenance; evaluators may supply it for diagnostic comparisons and
historical replay. Existing conservative origin permission checks remain. No
model-accuracy, production-enable, client-install or deployment approval follows
from these engineering contracts.
