# Reading a shadow write recommendation

This workflow uses the same authenticated `/memory` MCP connection as ordinary
memory tools. It requires write AI in `shadow` mode. When the tool is not present,
keep the normal memory workflow. This does not enable AI, alter its provider or
grant permission to apply suggested tags.

## 1. Supply the assertion's actual context

If the assertion concerns a project, use locally read component evidence or the
user's declaration. Register the project with `ensure_project` when available,
using the existing registration workflow. Recording origin and assertion subject
can differ. Never attach repository context to unrelated life facts.

An illustrative synthetic `tools/call` request:

```json
{
  "name": "store_fact",
  "arguments": {
    "fact": "The archive component renders session logs as Markdown and redacts secrets.",
    "namespace": "projects",
    "subject_scope": "unknown",
    "subject_context": "A session archive CLI that parses conversation logs and renders archival Markdown.",
    "allow_ai_processing": true
  }
}
```

Use explicit client tags when known. They remain authoritative. A supplied primary
tag, or a single tag promoted to primary, bypasses optional project classification.
`allow_ai_processing` is per-request admission; it is not persisted as permission
for future requests.

## 2. Keep the returned reference

Only successful eligible shadow writes whose pending journal was saved return
`ai.operation_ref`. The fallback text includes `ai_operation_ref`. A duplicate,
bypassed write or unavailable journal has no new reference. The initial
`ai.status: shadow` is not a completed judgment.

```json
{
  "name": "get_write_recommendation",
  "arguments": {
    "operation_ref": "<copy the exact reference returned by store_fact>"
  }
}
```

Read the reference once. If `pending`, allow one delayed follow-up. If still
pending, stop polling and retain it for a later explicit check. Each read performs
zero provider calls. It neither retries inference nor changes facts or recall
counters.

## 3. Present the outcome

| Status | Client behavior |
|---|---|
| `pending` | The saved job has no terminal result yet; one delayed follow-up is allowed. |
| `decided` | Show `suggested_primary_tag` separately from `recommended_related_tags`. Do not apply either automatically. |
| `abstained` | Show `subject_decision`; preserve distinctions between `non_project`, `multiple_projects`, `other_project` and `insufficient_context`. |
| `failed` | Show the finite `reason_code`; do not retry inference automatically. |
| `outdated` | Fact or catalog changed; discard the old suggestion. |
| `not_found` | The reference is unknown or belongs to another deployment owner. |
| `unavailable` | The result cannot currently be read safely; keep the stored fact and baseline grouping. |

All results have `applied: false`. Non-decided results have no suggested primary
and an empty related list. The response includes no fact text, component context,
point ID, local path, private owner, model rationale or raw journal. Lookup handles
are not credentials: MCP authentication remains required.

## Freshness and recovery

Input, grouping, origin, subject, creation identity, lifecycle/expiry/maintenance
changes invalidate suggestions. Recall-only updates do not. Changed catalogs
invalidate completed proposals. Pending jobs from another runtime report
`failed/interrupted`; a saved deadline bounds abandoned pending work. Reading never
resumes a lost job. Old version-2 operator journals cannot be queried through this
tool.

The API checks the current observed fact and catalog. A later write can still
invalidate a displayed recommendation. This workflow performs no automatic
mutation and does not establish separate identities for clients sharing the same
deployment credentials.
