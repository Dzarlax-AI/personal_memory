---
title: MCP tools
---

Memory writes: `store_fact`, `update_fact`, `set_fact_lifecycle`, `delete_fact`, `forget_old`, and `import_facts`.

Memory reads: `recall_facts`, `list_facts`, `find_related`, `get_stats`, `list_tags`, and `export_facts`.

When enabled, RAG adds `search_documents` and `reindex_documents`; Todoist adds `get_projects`, `get_labels`, `get_tasks`, `create_task`, `update_task`, `complete_task`, and `delete_task`.

When RAG is disabled, its two tools are not registered on `/memory`. When Todoist is disabled, the `/todoist` route is absent. When Viz is disabled, the `/viz` route is absent.

Default recall returns valid, non-expired current facts. Lifecycle history requires explicit inspection modes. See the [lifecycle contract](../../lifecycle/fact-lifecycle-contract/).

## Optional project registration

When `registration.mode` is `client_declared`, `/memory` also advertises `ensure_project`. It accepts `project_key`, `namespace: "projects"`, `name`, `tag`, `summary`, and an `evidence` array (possibly empty). It registers declared project context without model inference. Only `created` and `existing` supply an active identity for clients; changed declarations create a proposal.

`recall_facts` accepts optional `project_context_id` for the configured owner's project. It supplies context to optional AI ranking without changing filters, lifecycle authority or retrieval candidates. Reads never register projects implicitly. See [optional AI configuration and limits](../../operations/optional-ai/).

## Optional assertion subject on writes

`store_fact` and `update_fact` accept optional `subject_scope` (`project`,
`non_project`, or `unknown`) and `subject_context` (at most 2048 UTF-8 bytes).
Supplying context without scope means `unknown`. These are untrusted client
declarations about the assertion, separate from `source_project` recording origin.
They never change the explicit namespace or replace explicit tags.

A life fact belongs in `personal` and needs no project registration, catalog,
inference provider or project tag. General technical preferences belong in `tech`.
Explicit `non_project` bypasses project inference. A conflicting namespace/scope
returns `subject_decision: namespace_scope_mismatch` while preserving the ordinary
write and its explicit metadata; the client must correct placement explicitly.

Optional project inference reports its decision in `ai.subject_decision`:
`known_project`, `other_project`, `non_project`, `multiple_projects`, or
`insufficient_context`. These are outcomes, never stored as synthetic project tags.
Only a validated known-project decision can apply inferred grouping.

The validated client declaration is stored under payload `subject`, exposed in
structured recall and retained by export/import. Updating text without replacement
subject fields clears the old declaration to avoid stale component context.
Unchanged text preserves it. Updates and imports do not run project inference.
Legacy calls without either optional field retain the original judgment wire format.

## Document search contract

`search_documents` accepts `mode="hierarchical"` (the default) or `mode="flat"`.
It returns only fully validated, sealed document generations. A response with
`incomplete: true` includes `rejected_candidates` counts: `legacy_unverified`
means an older index layout has not been reindexed; `out_of_root` means a stored
path is outside `RAG_DOCUMENTS_DIR`; and `stale_generation` means an older
generation matched semantically but was withheld rather than attaching its score
to newer content.

### RAG upgrade requirement

After upgrading an RAG-enabled service that already has document chunks, run a
normal reindex from the configured source directory before relying on document
search. Older unsealed chunks are deliberately not returned, rather than being
treated as published. Reindexing is separate from application startup and is
not automatic; it does not migrate fact-memory data.

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
