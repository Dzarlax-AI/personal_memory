---
title: Optional AI memory features
description: Independent, experimental classification, recall ranking and catalog maintenance.
---

Personal Memory works without hosted inference, AI credentials or an AI catalog. Three features are independently optional and disabled by default:

| Feature | Selection |
| --- | --- |
| Periodic catalog maintenance | An explicitly selected generative endpoint and model |
| New-fact project classification | Off, Decisions or Jev |
| Recall candidate ranking | Off, Decisions or Jev |

**Experimental:** Transport and invariant tests do not establish classification or retrieval accuracy. Keep these features off for ordinary operation until a provider has passed the corresponding independent evaluation. Decisions and Jev do not generate catalog descriptions. A maintenance model has no direct mutation tools.

## Operator configuration

Set `MEMORY_AI_CONFIG_FILE` to a private mode-0600 JSON file. An absent variable disables all features. This path is server configuration; MCP clients cannot select endpoints or credentials. There is no online startup capability probe. Only active profiles require keys. The existing memory MCP API key is unrelated to inference keys.

A minimal disabled configuration:

```json
{
  "schema_version": 1,
  "catalog_dir": "/var/lib/personal-memory/context-catalog",
  "state_dir": "/var/lib/personal-memory/ai-state",
  "maintenance": {"enabled": false},
  "write": {"mode": "off", "provider": "none"},
  "read": {"mode": "off", "provider": "none"},
  "profiles": {},
  "egress": {"allowed_namespaces": [], "allowed_project_tags": [], "unassigned_namespaces": []}
}
```

Mount catalog and state directories on a persistent private volume. Use canonical paths, without symlink components. State and journals are private; do not publish them or commit them. The reader validates snapshot content and digest. Missing/corrupt catalog or exhausted storage/budget causes a baseline fallback or skipped maintenance run.

Judgment profiles use these exact current protocol/model/endpoint triples:

| Protocol | Model | Endpoint |
| --- | --- | --- |
| `decisions` | `gpt-6-luna` | `https://api.openai.com/v1/decisions` |
| `jev` | `jev-1.13.0` | `https://api.typesafe.ai/v1/systemone` |

Each hosted profile includes `key_file` pointing to a regular private mounted file. Profiles have optional `timeout_ms`; interactive read/write budgets remain bounded to one second. Write-shadow jobs use `write.shadow_timeout_ms`: omitted or zero preserves the 1000 ms default; explicit values must be 1–30000 ms. The write provider defaults to this shadow timeout; an explicit profile `timeout_ms` can impose a shorter deadline. Set both fields when increasing an existing profile. Read-shadow jobs remain bounded to one second. The runtime does not switch to another provider on failure. A model/version change requires contract and quality review.

A maintenance profile uses `protocol: openai-responses` or `openai-compatible-chat`, an explicit full endpoint URL, and the operator's `model`. Set `local: true` explicitly to permit HTTP for a local model server. Local unauthenticated profiles may omit `key_file`. Compatibility is checked by strict response validation; arbitrary models are not guaranteed to satisfy the proposal schema. A local gateway may itself forward data externally: review its configuration.

To activate a judgment feature, set its `mode` to `shadow` or `on`, `provider` to `decisions` or `jev`, and `profile` to an existing profile name. For maintenance set `enabled: true`, `profile`, `interval_seconds`, and `max_facts_per_run` (default 50, maximum 100). The default `catalog_publish: review` saves descriptions for review. An explicit `evidence_bounded` policy permits the limited descriptive publication described below. Keep `grouping_apply: manual`; fact retagging requires a separate approved manifest. There is no inference immediately on startup; the first maintenance tick follows the configured interval.

## Egress and budgets

With the default explicit policy, an empty egress allowlist permits no inference. `allowed_namespaces` and `allowed_project_tags` identify storage scope that can be shared. Every included project tag must be allowed; untagged facts require an explicit namespace in `unassigned_namespaces`. The source project is not used to bypass egress or select the subject. Catalog descriptions/examples and read queries are part of the disclosed payload. A single forbidden read candidate causes the entire ranking request to fall back.

Project catalog egress uses `egress.project_policy`, which defaults to `explicit` when omitted. In that mode, only tags named in `allowed_project_tags` are permitted, preserving the existing allowlist behavior. `registered` is a separate operator choice: it permits descriptions for eligible, persisted project registrations owned by this memory server, but only within explicitly allowed namespaces. Automatic registered grants apply to write classification and recall ranking only. Maintenance keeps its existing static explicit egress permissions in this revision. Registered mode grants access to current and future eligible project descriptions, so choose it with that scope in mind.

`egress.denied_project_tags` overrides both explicit and registered grants. In explicit mode, one forbidden catalog card rejects the whole catalog request, preserving existing behavior. In registered mode, denied or otherwise ungranted cards are filtered out of the provider catalog. Registered mode does not grant an unknown ordinary tag; it remains denied unless explicitly listed in `allowed_project_tags`. Draft or unresolved registrations, invalid or missing identity, foreign-owner entries, and identity-free legacy cards receive no automatic grant. Existing identity-free cards can still be allowed individually with an explicit tag grant. Registration is a local catalog operation and never sends a provider request. A successful registration can be used immediately without editing configuration or restarting the service.

In registered mode, the provider receives only eligible catalog cards granted by policy. Raw README text, submitted evidence excerpts, repository identity, private project key, and local evidence references are stripped before transmission. Project descriptions and documented examples can still be sent. For facts, optional origin metadata, and read candidates, scope is checked independently: a forbidden project tag in any of those inputs blocks the entire affected request rather than being silently removed. Life facts do not need their own project registration. Write classification remains restricted to project facts. Recall ranking in registered mode currently requires at least one permitted project card in the catalog, in addition to namespace permission; an empty or fully excluded catalog falls back to ordinary retrieval even for authorized life facts.

The example explicitly grants untagged `projects` facts through `unassigned_namespaces: ["projects"]`; combined with client opt-in this lets a new fact receive a project proposal. Leaving that list empty blocks untagged fact inference.

The approved egress-only example is `docs/examples/optional-ai/registered-projects-egress.json`. It contains only the exact `egress` configuration fragment and is not a standalone configuration file.

`shadow` also transmits data. It serves the ordinary baseline answer before bounded background evaluation. It does not change tags or increment recall counters a second time. Shadow jobs may be dropped when the queue is full. Hosted model retention follows the selected provider and account settings; feature flags do not create zero data retention guarantees.

No read/write provider retries or failover occur. Limits default to 1,000 calls and 2,000,000 input tokens per UTC day, with conservative 65,536-token reservations for unknown usage (131,072 when maintenance is enabled). Configure `limits.daily_calls`, `limits.daily_input_tokens`, and `limits.reservation_tokens`; Maintenance requires at least 131,072 reserved tokens. Implausible usage exhausts the day's allowance; corrupt ledgers disable inference. Usage is operational accounting, not a billing receipt. Raw prompts/responses and fact texts are not emitted into generic telemetry.

## Automatic project registration (R2)

The optional client-declared registry avoids manually preparing a catalog for every project. It is independent of the three AI switches and defaults to `off`:

```json
{
  "registration": { "mode": "client_declared" },
  "catalog_dir": "/var/lib/personal-memory/context-catalog"
}
```

This is a fragment of the optional-memory configuration, not a complete inference configuration. A fresh installation keeps maintenance, write inference and read inference off. To use optional AI, the operator separately configures a provider and feature, and chooses whether project descriptions may leave under the egress policy above. Write inference can additionally require per-request `allow_ai_processing=true` with `write.admission: "client_opt_in"`; this permission remains independent of the operator's provider and egress settings. Registration alone requires no model profile, API key, inference budget or maintenance worker. When enabled, the server advertises `ensure_project`. Updated client integrations call it before their first memory operation from an explicitly identified project. `recall_facts` and `search_documents` do not create projects implicitly.

The client sends `project_key`, `namespace: "projects"`, `name`, proposed `tag`, `summary`, and an `evidence` array of excerpts (`kind` and `text`); use an empty array when no excerpt is available. Use a persistent local UUID or a digest of a normalized repository identity with credentials removed. Reuse the same key across sessions, worktrees and local path changes. A Git repository is not required when the client can persist a stable key and the user declares the project. Do not submit absolute private paths, credentials, whole files or whole repositories. The server does not scan client machines or fetch arbitrary source URLs. Submitted evidence and private identity are not forwarded by write classification or recall ranking. Separately enabled maintenance can consume bounded client evidence under its static egress permissions, as described below.

Registration is bounded to 8 KiB, summary 2 KiB, four evidence excerpts totaling 4 KiB, and the existing 32-entry/64-KiB snapshot limits. `created` or `existing` returns `project_id`, the accepted `tag`, and `catalog_hash`. Description changes produce `proposed_update`; identity/tag conflicts produce `ambiguous`. A failed or unavailable registration leaves baseline memory usable. Client integrations skip the extra call when the capability is absent and cache successful registration for the session.

New cards are `declared`, not `approved`. They describe client-supplied context without claiming verified ownership. Stable identities are scoped to the configured memory owner; the current server has one deployment-wide `MEMORY_USER`, not separate catalogs per OAuth login. Do not expose a shared-owner instance as if it provided tenant isolation. Registered egress grants apply only to eligible declared or approved entries with a valid persisted identity and the current owner; they do not apply to drafts, unresolved entries, foreign owners, or legacy entries without identity. An explicit tag grant remains available for an existing identity-free card.

After registration, a client may supply the accepted tag as `source_project` with `source_kind: "client_declared"` when recording a fact. This records where the assertion came from, not its subject: an assistant can record a Health fact. An unsuccessful registration must not fabricate origin. Existing stored origins remain unchanged.

`recall_facts` accepts optional `project_context_id`. It is resolved under the configured owner and provides context only to optional AI relevance. It does not filter by project, widen the candidate pool, bypass lifecycle/quarantine checks or promote fact authority. An unknown or foreign identity is rejected. A fact or read candidate with a forbidden project tag blocks the full affected inference request. Document retrieval remains unchanged; the client can still register before `search_documents`.

The maintenance model can consume allowed declared cards and bounded client evidence. The original client summary is retained separately from model-derived descriptions and can be cited through a request-local alias. Model-derived descriptions are not source evidence for later updates. The default `catalog_publish: "review"` retains proposals for inspection. Explicit `evidence_bounded` permits only descriptive changes to existing identities with complete supplied evidence and intact dependencies; identity, owner, key, name, tag and fact grouping are not automatically changed. Evidence-link checks are structural, not proof that an assertion is true. Evaluate description quality before enabling this policy. Provider permissions for classification do not also authorize sending README excerpts to a maintenance provider.

The CLI below remains available for import, diagnosis and recovery. Real client installation, live maintenance inference, production publication and deployment are separately authorized actions; local engineering checks do not establish their acceptance.

### Manual import and recovery

The CLI works with no credentials:

```sh
go run ./cmd/context-catalog validate --file /private/path/catalog.json
go run ./cmd/context-catalog import --file /private/path/catalog.json --output /private/path/reviewed.json
go run ./cmd/context-catalog publish --dir /private/path/catalog --file /private/path/reviewed.json --expected-hash ''
go run ./cmd/context-catalog export --dir /private/path/catalog --output /private/path/export.json
```

For subsequent publications, set `parent_hash` and `--expected-hash` to the current active digest. Publication rejects a changed parent. Snapshots must have `schema_version: 1`, `catalog_version`, RFC3339 `created_at`, and entries with approved `review_status`. Entries identify namespace/tag, description, ownership boundaries, examples and local evidence references. At most 32 entries and 64 KiB are supported per snapshot. Namespaces are the existing five namespaces; entries are not restricted to four experimental projects. Only the `projects` scope currently participates in automatic project classification.

Local evidence references are retained for review and stripped from provider payloads. Catalog entries are context, not an independent truth source. Client registration can create a declared project tag. Renaming, merging identities and replacing disputed descriptions require operator review. Historical assertions retain their temporal scope.

## Declared origin and write behavior

`store_fact` and `update_fact` accept optional `source_project` and `source_kind` (`user_declared` or `client_declared`). Both are required together. The server records an origin timestamp. This metadata identifies where a record was declared, not what it is about. It is preserved on updates unless explicitly replaced and round-trips through import/export. Existing records receive no fabricated origin.

Explicit caller grouping always wins, including legacy one-tag primary promotion. AI classification only considers new `projects` facts without a primary grouping. It adds allowed project grouping without removing existing tags. Update/import do not initiate classification. Unknown, multiple-owner, invalid and unavailable responses retain the ordinary metadata. Stored/duplicate status continues to describe persistence, separately from inference outcomes. The model never changes namespace, text, lifecycle, expiry, permanent flags or vectors.

## Recall behavior

The model sees the existing eligible semantic candidate pool, at most 20 candidates. It cannot recover an omitted fact. All namespace/tag/lifecycle/expiry/quarantine filters remain enforced. Relevance only changes ordering within an equal lifecycle authority tier; semantic scores and ranks stay intact. The existing lifecycle order is canonical current, other current, disputed, historical, superseded. Invalid/incomplete results or none-relevant responses return the baseline. Actual returned facts receive recall increments once.

AI-ranked responses require candidate revalidation. The runtime may bypass caching for this optional mode to avoid publishing an order based on stale metadata. Ordinary disabled reads keep their existing cache behavior. Shadow comparisons sample eligible cache misses, rather than issuing another call for every cache hit.

## Maintenance review and explicit grouping changes

The maintenance worker scans bounded pages and saves proposal artifacts in private state. Under the default review policy it does not publish a catalog. The explicit evidence-bounded policy can publish descriptive updates after dependency checks; fact retagging remains manual. Read-counter changes do not trigger reclassification. Review the proposed subject/related tags, original-source evidence, uncertainty and catalog parent. A generated proposal is not its own correctness reference.

Grouping application uses a separately approved, mode-0600 `factgrouping.Manifest` with exact point IDs, semantic fingerprints and before/after grouping. Stop **all writers**, including other clients and operator tools, before using:

```sh
go run ./cmd/ai-maintenance apply --manifest /private/path/approved.json --journal /private/path/apply-journal.json --qdrant-url http://isolated-qdrant:6333 --confirm-server-stopped
go run ./cmd/ai-maintenance rollback --manifest /private/path/approved.json --journal /private/path/rollback-journal.json --qdrant-url http://isolated-qdrant:6333 --confirm-server-stopped
```

These examples are operator commands, not authorization to run them on production. The CLI changes only tags, primary tag and a local grouping audit reference. A manifest must be approved explicitly. It compares metadata before dispatch and records intent first. A timeout after dispatch remains ambiguous; resume inspects state and does not blindly retry. Rollback only restores matching after-state and does not overwrite later edits. Partial operations remain visible in the journal. Catalog publication and Qdrant changes are separate operations.

## Disable and recover

Set write/read `mode: off` or maintenance `enabled: false` and restart the selected memory service in an approved deployment window. Do not delete catalog snapshots or journals. Disabling inference preserves already stored grouping; correcting it requires a reviewed manifest. To restore a former catalog, publish a reviewed equivalent generation with the current parent hash. This changes catalog context, not the tags of existing facts.

## Evaluation and release boundaries

Build exact synthetic previews with `cmd/eval-ai-memory`. The public fixtures are structural, author-labelled examples, not proof of accuracy. Existing real campaigns remain immutable. Reference-audit-v3 human review and a new frozen holdout are still required. Compare Decisions, Jev and deterministic baselines on identical facts/candidates and catalog versions; evaluate write, read and maintenance separately. Model quality, API connectivity, valid JSON, invariant tests and target-host latency are distinct evidence.

No deployment, real-fact egress or production grouping changes are implied by building the optional layer. Deployment configuration in `personal_ai_stack` must be reviewed separately.

### Maintenance output compatibility

Maintenance profiles accept `output_mode: "schema"` (default strict JSON Schema) or `output_mode: "json"` for compatible models without schema support. JSON mode includes the proposal schema in the instructions and applies the same server validation. Protocol compatibility must be checked on synthetic data before real facts are allowed. The server does not install or start a local model runtime.

The worker stops creating proposals when 100 pending proposal files exist. Review and archive them explicitly before continuing; it does not delete unreviewed proposals. Failed sweeps use bounded backoff with jitter and persist a content-free outcome.

### Ordinary life facts and subject context

Project classification is eligible only for `namespace=projects` with no explicit
primary tag. Other namespaces keep ordinary writes without project inference.
Optional `subject_scope=non_project` also bypasses project inference, including
shadow work. A personal fact recorded from a repository can retain declared origin
without becoming a fact about that repository.

Clients may supply a bounded `subject_context` description of the actual component
the assertion concerns. It is stored as a client declaration and sent only under
the existing write egress policy, budget and final serialized 64 KiB limit. Treat
the description as untrusted data. Do not attach a repository's description to
unrelated life facts, and never include credentials or private paths.

The extended project rubric distinguishes known project, outside-catalog project,
positively non-project, multiple subjects without a primary and insufficient
context. Extended choices are used only when new subject fields are supplied;
legacy classification requests keep their original shape. Existing experiment
results therefore do not establish the quality of the extended rubric. Validate
it on synthetic inputs before transmitting new private life facts.

Subject metadata does not authorize maintenance to move a fact between namespaces
or infer its origin. All optional AI features remain disabled by default.

### Maintenance uncertainty codes

`missing_evidence` contains exact codes: `ambiguous_subject`, `missing_ownership`, `insufficient_evidence`, `catalog_gap`, or `shared_ownership_unclear`. Free-form explanations and code prefixes are rejected. The request schema and instructions must describe those same values. A proposal with missing evidence remains for review and is not automatically published under `evidence_bounded`.

### Recording origin and subject inference

The runtime write classifier omits recording origin from both synchronous and
shadow provider requests. Origin remains stored provenance and round-trips through
normal reads and import/export. Only the assertion, optional subject scope/context,
and approved catalog describe its project subject. Existing origin egress checks
remain conservative. Evaluators retain an explicit origin input for historical
wire replay and diagnostic comparisons; this does not enable runtime forwarding.

Personal life facts and cross-project technical preferences use explicit
`personal` or `tech` namespaces and bypass project inference. Project inference
remains optional and disabled by default. Removing origin input does not establish
model accuracy or authorize production activation.

### Maintenance response model identifiers

Maintenance profiles can set an optional `response_models` array (maximum 16
explicit identifiers) when a provider returns a snapshot identifier for a requested
model alias. The requested `model` and these exact identifiers are accepted;
other identifiers remain invalid. No prefix or automatic alias matching occurs.
This option is restricted to maintenance protocols and does not change the pinned
Decisions/Jev judgment adapters.

### Per-request write admission

Set `write.admission` to `client_opt_in` to require an explicit boolean
`allow_ai_processing=true` on each eligible `store_fact` request. Missing or false
preserves baseline storage without allocating AI work or budget; other types are
rejected before storage. The permission covers this assertion and submitted
subject context for write classification only. It is not persisted or reused by
read, maintenance, update or import. Existing explicit grouping and non-project
bypasses still apply.

The flag cannot override feature switches, budgets or the operator egress policy.
With the default explicit project policy, every eligible owner-scoped catalog card
must be permitted or the request falls back. Registered policy filters ungranted
cards from the provider catalog, while a forbidden fact or origin still blocks the
entire affected request. Recording origin is neither consent nor subject evidence.

Omission of `write.admission`, or `operator_policy`, retains the earlier operator
policy behavior. `client_opt_in` must be explicit for restricted shared-server
shadow. Admission is unsupported on read configuration. The preparation example
`docs/examples/optional-ai/decisions-scoped-shadow-preparation.json` starts with
empty project-tag and unassigned-namespace allowlists: approve and enumerate the full catalog, then explicitly permit unassigned projects before
activation. It is not a deployment instruction or a live enablement.

Disable write inference to roll back behavior. Remove the new admission property
before using an older binary whose strict loader does not recognize it.

### Write-shadow terminal outcomes

Private `write-audits` journals now record `failed` outcomes with a fixed `reason_code` and elapsed `duration_ms`, alongside existing `decided` and `abstained` proposals. Codes include `timeout`, `canceled`, `provider_auth`, `provider_rate_limit`, `provider_http`, `provider_transport`, `invalid_response`, `invalid_request`, `catalog_unavailable`, `egress_denied`, `budget_unavailable`, `queue_full` and `provider_unavailable`. No raw error, provider body, assertion or credential is stored in these fields. The existing private operator journal retains its point reference and grouping metadata; it is not telemetry. Bypassed or duplicate writes create no inference job or journal. Queue saturation records a failed job without calling the provider. Graceful cancellation records queued failures; a hard kill can still lose jobs. Journal persistence failure produces a fixed warning and cannot be recorded in the unavailable store.

A failed shadow job never changes stored grouping. Unknown usage reservations remain conservative after a timeout; `pending` does not mean the request is still executing. No automatic retry or provider fallback is added. Older binaries reject `shadow_timeout_ms`; rollback restores the previous image and configuration together.
