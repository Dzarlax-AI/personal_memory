---
title: What's new
description: Unreleased context-correctness improvements and upgrade notes.
---

## Unreleased: context correctness

This release hardens the boundary between stored context and what a client is
allowed to treat as current, complete, and safe to use.

### More explicit fact outcomes

- A deterministic fact ID is no longer silently overwritten by `store_fact` or
  `import_facts`.
- Ambiguous writes and saturated recall windows are reported explicitly instead
  of being presented as ordinary absence or success.
- Expiry uses one strict UTC calendar-date rule across runtime and evaluation.

### Safer document retrieval

- Document search returns only sealed, fully validated RAG generations; it does
  not mix old and partial replacements.
- A stale generation's score is never attached to newer document text.
- Stored candidates outside `RAG_DOCUMENTS_DIR` and malformed Qdrant point IDs
  are rejected before they reach a client.
- The public document-search modes are `hierarchical` and `flat`.

### Upgrade note for RAG operators

Existing unsealed RAG chunks are intentionally withheld as
`legacy_unverified`. After upgrading an RAG-enabled service, run a normal
reindex from the configured documents directory before relying on
`search_documents`. This is an explicit operator action: application startup
does not automatically reindex, and no fact-memory migration is involved.

See [MCP tools](../reference/tools/) for the response contract and the
[upgrade guide](../operations/upgrade-rollback/) for the normal deployment
boundary.

## Unreleased: optional AI memory layer

Three independent capabilities are available as opt-in experiments: periodic
maintenance, write-time project classification, and read-time fact selection.
All remain disabled when `MEMORY_AI_CONFIG_FILE` is absent. Write/read judgment
can use Decisions or Jev; maintenance uses an operator-selected compatible model.
Enabling one capability does not enable the others. Shadow mode sends allowed
payloads to the configured provider and needs the same egress policy as on mode.

### Automatic project context

An updated MCP client can call `ensure_project` using bounded repository evidence
before its first project-related memory operation. Registration is independent
of inference credentials. It is explicit, idempotent and opt-in; hooks never
register projects or store facts automatically. Ambiguous identities and changed
summaries remain proposals rather than silently replacing an accepted identity.

### Facts without a project

Personal life facts and cross-project technical preferences need no project
catalog or model. Clients choose an explicit namespace from the assertion.
Optional `subject_scope` and `subject_context` describe what a fact concerns.
Recording origin is stored separately and is omitted from runtime write
classification, including shadow mode. A repository origin never supplies proof
of project membership.

### Upgrade and acceptance boundary

Existing clients can continue ordinary writes without the new fields. There is
no automatic migration, fact regrouping or inference activation. Deploying the
server does not install updated client instructions: integration bundle 0.2.0
must be installed or updated separately at the operator's explicit client root.
For ChatGPT, the official UI/admin step remains necessary.

Maintenance defaults to review proposals. Automatic fact regrouping is not a
background maintenance action. Write/read on modes require their own quality
acceptance before operational use; the small exploratory routing results do not
establish production accuracy. See the [optional AI operator guide](../operations/optional-ai/)
and [integration bundle guide](../integration-bundle/guide/) for configuration,
egress, budgets and installation behavior.
