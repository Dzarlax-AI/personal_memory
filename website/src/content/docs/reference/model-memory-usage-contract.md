---
title: Model-memory usage contract
---

# Model Memory Usage Contract

Contract version: **1.0.0**

Status: **Normative**

This document defines client-independent behavior for models and agents using Personal Memory facts, document search, Todoist, and ordinary conversation context. It is the common source for client-specific prompts, skills, hooks, and conformance tests.

Client-specific instructions may make these rules more concrete, but they MUST NOT weaken a `MUST` or `MUST NOT` rule in this contract.

## Normative language

The key words **MUST**, **MUST NOT**, **REQUIRED**, **SHOULD**, **SHOULD NOT**, and **MAY** are normative:

- **MUST** and **MUST NOT** define conformance requirements.
- **SHOULD** and **SHOULD NOT** define the default behavior. A client may depart from them only for a concrete reason that preserves the intent of the contract.
- **MAY** defines optional behavior.

## Goals

A conforming client:

- uses memory intensively when durable user or project context can materially improve the work;
- avoids indiscriminate tool calls for trivial or self-contained requests;
- chooses facts, documents, Todoist, or ordinary context according to the information type;
- stores only durable, compact context as facts;
- handles lifecycle state, related candidates, staleness, and uncertainty explicitly;
- degrades honestly when a capability is disabled or unavailable;
- does not expose user content through usage telemetry.

## Non-goals

This contract does not:

- require recall before every message;
- define a client-specific installation bundle;
- require Todoist, document RAG, or telemetry;
- introduce an LLM into the Personal Memory server write or retrieval path;
- permit storage of chain-of-thought, credentials, or private reasoning;
- replace the [Fact Lifecycle Contract](../lifecycle/fact-lifecycle-contract/).

## Terms

| Term | Meaning |
|---|---|
| **Fact memory** | Compact, durable context accessed through tools such as `recall_facts` and `store_fact`. |
| **Document RAG** | Long-form source material searched through `search_documents`. |
| **Todoist** | Optional task storage accessed through Todoist MCP tools. |
| **Ordinary context** | The current conversation, attached content, and other context already available without Personal Memory tools. |
| **Capability** | A tool or server the client has discovered and can attempt to call. Discovery does not prove that a later call will succeed. |
| **Drift-prone fact** | A fact likely to change over time, such as current employment, schedules, prices, laws, dependency versions, or deployment state. |
| **Write outcome** | A confirmed store result, confirmed duplicate, confirmed rejection, or ambiguous result after a timeout or transport failure. |

## Capability model

Fact memory, document RAG, and Todoist are independent capabilities. A client MUST determine availability from the tools exposed in the current session or from an authoritative client capability signal.

A client MUST NOT:

- assume that document RAG exists merely because fact memory exists;
- assume that Todoist exists merely because a memory server exists;
- treat a missing tool as an empty successful result;
- claim that recall, storage, search, or task creation succeeded without a successful tool result;
- store a task as a fact when Todoist is missing or unavailable.

Capability discovery is session-scoped. A capability that existed in an earlier session MAY be absent in the current one.

## Intensive but selective use

“Intensive use” means recalling whenever durable context can materially affect correctness, personalization, continuity, or a consequential decision. It does not mean calling a tool before every response.

### Mandatory recall triggers

Before producing a substantive answer or taking an action, a client MUST attempt relevant fact recall when the request:

- depends on a user preference, durable constraint, identity detail, or established working style;
- refers to a personal or work project whose architecture, naming, configuration, or prior decisions may matter;
- uses continuity language such as “as usual,” “like before,” “continue,” “the next issue,” or “you know my preference”;
- asks for historical reasoning, prior decisions, commitments, or user-specific context not fully present in ordinary context;
- would cause a consequential or difficult-to-reverse action where a stored constraint could change the safe course;
- asks what the user previously decided, prefers, knows, or recorded.

The client SHOULD form a narrow semantic query from the current task and use the narrowest relevant namespace when that namespace is known.

### Recall source decision table

| Situation | Required source | Rationale |
|---|---|---|
| Durable preference, constraint, identity, project fact, or prior decision | `recall_facts` | Facts are the compact durable memory layer. |
| Reasoning, article, meeting note, runbook, specification, or other long-form source | `search_documents` | Documents preserve source context and detail. |
| A durable decision is needed together with its rationale or supporting material | Both | Facts provide the current decision; documents provide evidence and history. |
| The answer is fully contained in the current prompt or attachment and does not depend on durable context | Neither | Additional retrieval would be indiscriminate. |
| Trivial transformation, formatting, arithmetic, or generic explanation with no personal dependency | Neither | Memory cannot materially improve the answer. |
| The user explicitly asks to search one store only | That store only, unless safety requires disclosing a missing dependency | Respect explicit scope. |

When both stores are needed, the client MAY query them in parallel. It MUST interpret the results by type: a document passage does not silently replace a current fact, and a fact does not prove the contents of a long-form source.

### Empty recall

A successful call returning no relevant facts is different from unavailable recall:

- on an empty successful result, the client MAY continue using ordinary context and SHOULD avoid implying that a preference or decision was found;
- on an unavailable or failed call, the client MUST follow the disclosure and failure rules below.

## Context precedence

The user’s explicit statement in the current conversation takes precedence for the current task over conflicting recalled context. This does not silently rewrite durable memory.

When current conversation context materially conflicts with a recalled fact, the client SHOULD:

1. follow the current explicit instruction for the current task, unless unsafe;
2. identify the recalled item as potentially stale, disputed, or superseded;
3. ask whether the durable fact should be updated when persistence is relevant;
4. use explicit lifecycle operations rather than inferring state from similarity.

System and developer instructions remain subject to the client’s own instruction hierarchy and are outside the authority model of fact memory.

## Storage routing

The client MUST classify candidate information before attempting storage.

| Destination | Store when | Correctly routed example | Counterexample (route elsewhere) |
|---|---|---|---|
| Fact memory | The information is compact, durable, user-specific or project-specific, and likely to improve future work. | “I prefer reversible PostgreSQL migrations.” | “Remind me at 16:00 to review the migration.” |
| Document library | The information is long-form source material whose structure, wording, or supporting detail matters. | An architecture decision record explaining rejected alternatives. | A one-sentence durable preference better represented as a fact. |
| Todoist | The user asks to create, update, complete, or delete an actionable task or reminder and Todoist is available. If it is disabled, the task remains uncreated. | “Create a task to review the migration tomorrow.” | “The analytics service uses ClickHouse”; this durable project statement belongs in fact memory. |
| Ordinary context only | The information is transient, already supplied for the current task, or not expected to help in future sessions. | A temporary draft title being compared in this conversation. | A durable tool preference the user explicitly asks the client to remember. |
| Never store | The content is a secret, credential, private chain-of-thought, hidden reasoning, or sensitive payload not explicitly suitable for durable memory. | An API token, password, private key, session cookie, or hidden reasoning trace. | “Production credentials are managed in 1Password,” without any credential value; this durable non-secret statement may qualify as a fact. |

### Durable fact criteria

A client MAY call `store_fact` only when all of the following are true:

1. The statement is expected to remain useful beyond the current conversation.
2. It is compact enough to stand alone without long-form source context.
3. It is appropriate for durable storage and contains no secret or hidden reasoning.
4. It is not primarily a task, reminder, or calendar action.
5. The client can choose an existing valid namespace or can obtain the needed placement from the user.
6. The client can state the fact without inventing details or certainty.

Good fact categories include:

- explicit preferences;
- durable constraints;
- accepted decisions;
- identity and role context;
- non-obvious project architecture or configuration;
- known gotchas that remain useful across sessions.

The client SHOULD NOT store facts merely because they appeared in conversation. It SHOULD store a newly stated durable preference or decision when the user asks it to remember, or when established client policy authorizes proactive storage.

If durability or destination is ambiguous, the client SHOULD ask one concise question rather than selecting a store arbitrarily.

### Explicit exclusions

A client MUST NOT store the following as facts:

- tasks, reminders, deadlines, or action-item state;
- long-form documents or large verbatim passages;
- credentials, tokens, passwords, private keys, cookies, or recovery codes;
- chain-of-thought, hidden reasoning, or internal scratch work;
- transient conversation state, tentative wording, or disposable drafts;
- fabricated names, configurations, measurements, or completion claims;
- data the user has asked not to retain.

A client MUST NOT use fact memory as a fallback queue for another unavailable system.

### Store result interpretation

The client MUST inspect the structured result of `store_fact` when available:

- `status="stored"` with `stored=true` confirms a new durable write;
- `status="duplicate"` with `stored=false` confirms that no new point was written and identifies an existing candidate;
- an error, timeout, canceled request, malformed response, or lost connection does not confirm either success or failure.

The client MUST NOT say “remembered” after a duplicate result without clarifying that the information was already present. It MUST NOT say “remembered” after an ambiguous outcome.

Related candidates are semantic neighbors, not automatically contradictions. The client MUST inspect their text and lifecycle metadata before proposing any update.

## Lifecycle and authority

The [Fact Lifecycle Contract](../lifecycle/fact-lifecycle-contract/) is normative for states, authority metadata, relationships, expiry, visibility, migration, and rollback.

A conforming client:

- MUST use valid, non-expired `current` facts as default operational truth;
- MUST NOT present `historical`, `superseded`, `disputed`, expired, or invalid lifecycle records as current truth;
- MUST treat `canonical=true` as a ranking hint, not proof of correctness or global uniqueness;
- MUST treat provenance as origin metadata, not a trust score;
- MUST NOT infer contradiction, dispute, or supersession from cosine similarity;
- MUST use explicit lifecycle mutations when changing authority or history state;
- MUST NOT invent reciprocal lifecycle relationships that the server did not create.

When a related candidate appears to conflict with a new durable statement, the client SHOULD ask for clarification or propose an explicit lifecycle update. It MUST NOT silently overwrite or auto-supersede the existing fact.

## Staleness and verification

Lifecycle validity does not guarantee real-world freshness. Before relying on a drift-prone fact for a consequential answer or action, the client MUST do one of the following:

1. verify it against an available authoritative source; or
2. explicitly tell the user that the memory is old or unverified and identify the relevant timestamp when available.

The client SHOULD use `verified_at`, provenance, `updated_at`, and the nature of the fact to assess freshness. Absence of `verified_at` means “not recorded as verified,” not “false.”

Stable personal preferences and historical facts do not require repeated external verification unless current context or another source creates a real reason for doubt.

## Todoist routing

Tasks and reminders belong in Todoist, not fact memory.

Todoist is optional and may be disabled. A client MUST treat it as unavailable unless the relevant tools are discovered in the current session.

When the user requests a task mutation:

1. If the relevant Todoist tool is available, the client MUST use it and inspect the result before claiming success.
2. If Todoist is disabled, undiscovered, or unavailable, the client MUST report that limitation.
3. The client MUST NOT store the task as a fact, document, or ordinary-memory substitute.
4. The client MAY offer the task text back to the user for manual use, but MUST label it as not created.

Reading Todoist is also optional. If task retrieval fails, the client MUST NOT fabricate an empty task list.

## Disabled and partially available behavior

| Availability | Required behavior | Forbidden claim |
|---|---|---|
| Fact memory unavailable | Continue only when ordinary context is sufficient; disclose the limitation when recall or storage was required. | “I checked your memory” or “I remembered this.” |
| Document RAG unavailable | Use facts or ordinary context only when they can answer the request; disclose that documents were not searched when document evidence was required. | “I searched your documents.” |
| Todoist unavailable | Report that the task was not created or changed; optionally provide manual task text. | “Task created,” or storing it as a fact. |
| Fact memory available, RAG unavailable | Use facts for durable context; do not imply that source documents were checked. | A claim based on an unsearched document. |
| RAG available, fact memory unavailable | Search source material when appropriate; do not imply that durable preferences or current decisions were recalled. | “Your saved preference is …” based only on a document passage. |
| Tool call succeeds with no results | State that no relevant result was found when disclosure is useful; continue with qualified ordinary context. | Treating no result as capability failure or inventing a result. |
| Tool call fails or times out | Follow bounded retry rules and disclose unresolved failure. | Treating failure as an empty successful result. |

A client MAY continue without disclosure for a self-contained request where the missing capability was neither required nor attempted.

## Failure, timeout, and retry policy

Retries MUST be bounded. A client MUST avoid retry storms and MUST preserve enough response time to report failure honestly.

| Operation | Automatic retry | Requirements |
|---|---:|---|
| Fact recall | No automatic retry | A handled `recall_facts` call increments recall counters even when its response is lost; report the unconfirmed recall instead of inflating usage statistics. |
| Document search | At most one | Only after a transient transport error or timeout; use the same bounded request. |
| Read-only list or stats | At most one | Only when the result is necessary for the user’s request. |
| Idempotent update with an exact identifier and confirmed server idempotency | At most one | Retry must preserve the exact target and payload. |
| `store_fact` after an ambiguous outcome | No blind retry | First verify whether the fact now exists or ask the user before risking a duplicate. |
| Task creation after an ambiguous outcome | No blind retry | First verify through task lookup when possible; otherwise report uncertainty. |
| Delete, complete, import, bulk maintenance, or semantic mutation | No automatic retry | Require a fresh explicit decision or an operation-specific idempotency guarantee. |

Authentication, authorization, schema, validation, and capability-not-found errors are not transient. The client MUST NOT retry them automatically.

After retries are exhausted, the client MUST state:

- which capability was unavailable or failed;
- which requested operation was not confirmed;
- whether it continued using ordinary context;
- whether any write outcome remains ambiguous.

The client SHOULD avoid exposing stack traces, credentials, private endpoints, or raw sensitive tool payloads in the disclosure.

## User-visible disclosure

Disclosure MUST be concise and relevant to the decision the user is making.

A client MUST disclose when:

- required memory, document, or Todoist access was unavailable;
- a requested write was not confirmed;
- it relies on a drift-prone fact that was not freshly verified;
- recalled sources materially conflict;
- it continues from ordinary context after required retrieval failed.

A client SHOULD identify old or unverified memory with language such as “saved on,” “last verified at,” or “not freshly verified,” using timestamps only when the tool returned them.

A client MUST NOT:

- imply freshness merely because a fact is `current`;
- expose unrelated recalled facts;
- claim a tool was used when it was not called;
- turn uncertainty into a fabricated success statement.

## Privacy-safe usage telemetry

Telemetry is optional. This section defines an allowlist for implementations that emit it; it does not require runtime telemetry.

An event MAY contain only:

| Field | Allowed values |
|---|---|
| `contract_version` | Published contract version, such as `1.0.0`. |
| `scenario_id` | A stable identifier from this contract or a future registered extension. |
| `capability` | `memory`, `documents`, `todoist`, or `ordinary_context`. |
| `operation` | A coarse operation class such as `recall`, `search`, `store`, `task_create`, or `fallback`. |
| `outcome` | `success`, `empty`, `duplicate`, `unavailable`, `timeout`, `rejected`, `ambiguous`, or `error`. |
| `latency_bucket` | A coarse local bucket, not a raw distributed trace containing content. |
| `retry_count` | A bounded non-negative count. |
| `client_family` | `codex`, `claude`, `chatgpt`, or `generic_mcp`, when known. |

Telemetry MUST NOT contain:

- prompts, responses, queries, fact text, document text, task text, or conversation excerpts;
- tags, namespaces, filenames, document paths, point IDs, task IDs, or provenance references;
- credentials, authentication headers, user identifiers, private endpoints, or tool payloads;
- embeddings, similarity vectors, chain-of-thought, or hidden reasoning.

Implementations SHOULD prefer local aggregation and SHOULD document retention separately. If the implementation cannot guarantee the allowlist, it MUST disable telemetry rather than redact best-effort after capture.

## Client mapping

The contract is behavioral. Client integrations map local concepts to it without changing the common rules.

| Client | Capability discovery | Durable instruction location | Conformance requirement |
|---|---|---|---|
| Codex | Current tool/app inventory and tool errors | Project or global Codex instructions generated from this contract | Must preserve all common `MUST` and `MUST NOT` rules. |
| Claude | Current MCP server tool inventory and tool errors | Claude project instructions or skills generated from this contract | Must preserve all common `MUST` and `MUST NOT` rules. |
| ChatGPT | Current connected app/tool inventory and tool errors | GPT or workspace instructions generated from this contract | Must preserve all common `MUST` and `MUST NOT` rules. |
| Generic MCP client | MCP tool discovery and call results | Client-specific system policy generated from this contract | Must preserve all common `MUST` and `MUST NOT` rules. |

An integration MAY use client-native terminology. It MUST NOT:

- weaken disabled-capability disclosure;
- route tasks into fact memory;
- allow secrets or hidden reasoning to be stored;
- convert bounded retry into unlimited retry;
- skip required freshness warnings;
- make recall mandatory for every trivial message.

## Conformance scenarios

Scenario identifiers are stable public identifiers. Published identifiers MUST NOT be renumbered or reused. A retired scenario remains recorded in the changelog.

Each scenario below defines externally observable behavior. Future conformance tests MAY add synthetic fixture details while preserving the stated expected and forbidden outcomes.

### Recall and source selection

| ID | Given | Expected behavior | Forbidden behavior |
|---|---|---|---|
| `RECALL-001` | A project-specific request may depend on saved architecture decisions. | Attempt a narrow `recall_facts` query before substantive work. | Proceeding as though no prior project context can exist. |
| `RECALL-002` | The user says “use my usual migration approach.” | Recall relevant preferences or disclose that memory is unavailable. | Inventing the usual approach. |
| `RECALL-003` | The user asks for reasoning contained in a long-form design document. | Use `search_documents`. | Treating a compact fact as proof of the document’s reasoning. |
| `RECALL-004` | The user asks for the current decision and its historical rationale. | Use facts and documents, interpreting each by type. | Searching only one store and claiming both kinds of evidence. |
| `RECALL-005` | The prompt fully contains a generic arithmetic or formatting task. | Use neither memory store. | Calling recall indiscriminately. |
| `RECALL-006` | Recall succeeds with no relevant result. | Continue with qualified ordinary context. | Claiming a preference was found or treating the call as unavailable. |

### Durable storage

| ID | Given | Expected behavior | Forbidden behavior |
|---|---|---|---|
| `STORE-001` | The user explicitly states a durable technical preference and asks to remember it. | Store a compact fact in an appropriate existing namespace. | Keeping it only as transient context while claiming it was remembered. |
| `STORE-002` | The user shares a tentative draft phrase for the current response. | Keep it in ordinary context only. | Persisting disposable draft text as a fact. |
| `STORE-003` | The user supplies a long architecture note. | Keep or index it as a document when document storage is in scope. | Copying the whole note into `store_fact`. |
| `STORE-004` | The content contains an API token. | Refuse durable storage of the token and avoid echoing it unnecessarily. | Sending the token to any memory store or telemetry. |
| `STORE-005` | The user requests a reminder. | Route it to available Todoist. | Storing the reminder as a fact. |
| `STORE-006` | Durability or namespace placement is genuinely ambiguous. | Ask one concise clarification question. | Inventing placement or silently storing transient content. |
| `STORE-007` | `store_fact` reports a duplicate. | Explain that the fact was already present and no new point was stored. | Claiming a new write occurred. |
| `STORE-008` | `store_fact` times out after the request was sent. | Treat the outcome as ambiguous and verify before any retry. | Blindly retrying or claiming success. |

### Tasks and optional capabilities

| ID | Given | Expected behavior | Forbidden behavior |
|---|---|---|---|
| `TASK-001` | Todoist is available and task creation succeeds. | Report the confirmed task creation. | Reporting success before inspecting the result. |
| `TASK-002` | Todoist is not exposed in the current session. | Report that the task was not created; optionally provide manual task text. | Storing the task as a fact or claiming creation. |
| `TASK-003` | Todoist task creation has an ambiguous timeout. | Verify through task lookup when possible, otherwise report uncertainty. | Blindly creating a second task. |
| `OFFLINE-001` | Fact memory is unavailable for a preference-dependent request. | Disclose that saved preferences could not be checked and qualify any fallback. | Claiming memory was checked. |
| `OFFLINE-002` | Document RAG is unavailable for a source-dependent request. | Disclose that documents were not searched. | Inventing document evidence. |
| `OFFLINE-003` | Memory works but Todoist is disabled. | Continue memory operations normally and report task limitations separately. | Treating all capabilities as unavailable or routing tasks to facts. |
| `OFFLINE-004` | A self-contained request needs no optional capability. | Answer without unnecessary availability disclosure. | Adding irrelevant failure warnings or tool calls. |

### Lifecycle, staleness, and failures

| ID | Given | Expected behavior | Forbidden behavior |
|---|---|---|---|
| `LIFECYCLE-001` | Recall returns a valid current fact. | Use it subject to freshness and current user instructions. | Treating `current` as proof of real-world freshness. |
| `LIFECYCLE-002` | Inspection returns a superseded or disputed fact. | Label it as history or uncertainty and exclude it from default truth. | Presenting it as current guidance. |
| `LIFECYCLE-003` | A related candidate has a high similarity score. | Inspect semantics and lifecycle metadata. | Calling it a contradiction solely from the score. |
| `LIFECYCLE-004` | Current user input conflicts with recalled durable context. | Follow the current explicit instruction for this task and offer an explicit memory correction when relevant. | Silently overwriting memory or ignoring the current instruction. |
| `LIFECYCLE-005` | A consequential action depends on an old drift-prone fact. | Verify it or disclose its age/unverified status. | Presenting it as freshly verified. |
| `FAILURE-001` | `recall_facts` times out after the request may have reached the server. | Do not retry automatically; disclose that recall was not confirmed. | Retrying and potentially recording the same user recall twice. |
| `FAILURE-002` | Authentication or validation fails. | Do not retry automatically; report the capability failure safely. | Treating the error as transient or as an empty result. |
| `FAILURE-003` | A destructive or semantic mutation fails ambiguously. | Stop and report that the write was not confirmed. | Automatic repeated mutation. |

### Telemetry

| ID | Given | Expected behavior | Forbidden behavior |
|---|---|---|---|
| `TELEMETRY-001` | An implementation records a successful recall event. | Record only allowlisted coarse metadata. | Recording query text, returned facts, tags, namespace, or point IDs. |
| `TELEMETRY-002` | A tool call fails with a sensitive payload. | Record a coarse error outcome without payload content. | Logging the raw request, response, credentials, or private endpoint. |
| `TELEMETRY-003` | The allowlist cannot be guaranteed. | Disable telemetry. | Capture first and attempt best-effort redaction later. |

## Contract evolution

The contract follows semantic versioning:

- **Patch:** clarification that does not change conforming observable behavior.
- **Minor:** backward-compatible new scenarios or optional behavior.
- **Major:** changed or removed requirements, changed safety behavior, or incompatible scenario semantics.

Every contract change MUST update the version and changelog. Published scenario identifiers MUST NOT be reused. Client bundles SHOULD declare the contract version they implement.

## Changelog

### 1.0.0 — 2026-08-01

- Established the common selection rules for facts, documents, Todoist, and ordinary context.
- Defined durable storage criteria and explicit exclusions.
- Defined lifecycle, staleness, disclosure, disabled-capability, timeout, and bounded-retry behavior.
- Defined privacy-safe telemetry fields and prohibited content.
- Added client mappings and stable conformance scenario identifiers.
