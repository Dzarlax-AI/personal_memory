# Fact Lifecycle Contract

This document defines how Personal Memory classifies stored facts as current context or inspectable history. The contract is additive: existing facts remain readable and are treated as current until lifecycle metadata is explicitly added.

## States

Every fact has one normalized lifecycle state:

| State | Meaning | Included in default current-context reads |
|---|---|---|
| `current` | Suitable for present-day context, subject to validation and expiry | Yes |
| `historical` | Accurate for a past period, but not current guidance | No |
| `superseded` | Replaced by one or more identified facts | No |
| `disputed` | Contested or unresolved and unsafe as default truth | No |

An explicit state is classification metadata, not a deletion or retention instruction.

## Qdrant payload fields

Lifecycle metadata lives beside the existing fact payload fields:

| Field | Type | Contract |
|---|---|---|
| `lifecycle_state` | string | One of `current`, `historical`, `superseded`, or `disputed`. |
| `canonical` | boolean | An explicit preference hint. It is valid only on a `current` fact. |
| `provenance` | object | Origin metadata with required non-empty string `source` and optional string `reference`. |
| `verified_at` | string | Timestamp in RFC3339 format. |
| `supersedes` | array | Unique point IDs that this fact replaces. IDs are normalized as strings. |
| `superseded_by` | array | Unique point IDs that replace this fact. IDs are normalized as strings. Required when state is `superseded`. |

Relationship IDs must be non-empty string or non-negative integer point IDs. A fact cannot reference its own point ID. Duplicate IDs normalize to one occurrence. A `current` fact cannot have `superseded_by` entries.

Valid current fact metadata:

```json
{
  "lifecycle_state": "current",
  "canonical": true,
  "provenance": {
    "source": "user",
    "reference": "decision-7"
  },
  "verified_at": "2026-07-21T08:30:00Z",
  "supersedes": ["older-point-id"]
}
```

Valid superseded fact metadata:

```json
{
  "lifecycle_state": "superseded",
  "canonical": false,
  "superseded_by": ["current-point-id"]
}
```

### Legacy facts

A payload with none of the lifecycle fields is normalized as `current` with `legacy=true`:

```json
{
  "text": "Existing fact without lifecycle metadata"
}
```

This is the only legacy-current rule. Once any lifecycle field is present, the payload is no longer classified as legacy, even when `lifecycle_state` is omitted. An explicit unknown state or any malformed explicit lifecycle field has `valid=false` and a metadata-only `invalid_reason`. Invalid string states remain visible verbatim in inspection views; a non-string state is represented as an empty state rather than being mislabeled as `current`. Invalid metadata must never cause a panic or leak fact text into the reason.

The normalized view returned by read surfaces contains `state`, `legacy`, `canonical`, optional `provenance`, optional `verified_at`, `supersedes`, `superseded_by`, `valid`, and optional `invalid_reason`.

## Authority metadata

`canonical=true` is an explicit ranking hint, not a global uniqueness guarantee. It is accepted only for a valid `current` fact. Personal Memory does not currently enforce one canonical fact per topic, namespace, tag, or relationship set.

Provenance records origin, not trust. A source or reference does not make a fact correct, verified, canonical, or current. `verified_at` records when a fact was verified but does not independently change its state or authority.

## State changes and invariants

Lifecycle transitions are explicit, reversible, and idempotent. Reapplying the same valid target state is safe. Any known state may be corrected to another known state when the complete target metadata satisfies these invariants:

- the target state is one of the four defined states;
- `canonical=true` appears only with `current`;
- `superseded` has at least one `superseded_by` point ID;
- `current` has no `superseded_by` point IDs;
- provenance, verification time, and relationship IDs have valid types and formats;
- relationship arrays contain no empty, duplicate, or self-referencing normalized IDs.

Changing a lifecycle state does not automatically create, edit, or validate the related points. It also does not infer reciprocal relationships.

## Retention and expiry

`permanent=true` is retention-only: it prevents `forget_old` from deleting the fact. It does not make the fact current, canonical, valid, verified, or visible in default context.

`valid_until=YYYY-MM-DD` is independent of lifecycle classification. Once expired, even a valid canonical `current` fact is excluded from current-context flows. Expired facts remain available to inventory surfaces such as list, export, stats, and Viz.

## Read visibility

| Read surface | Current | Historical | Superseded | Disputed | Invalid lifecycle | Expired |
|---|---:|---:|---:|---:|---:|---:|
| `recall_facts` | Yes | No | No | No | No | No |
| Operational context tool and HTTP endpoint | Yes | No | No | No | No | No |
| `find_related` | Yes | Yes | Yes | Yes | Yes, labeled invalid | No |
| `list_facts` | Yes | Yes | Yes | Yes | Yes, labeled invalid | Yes |
| `export_facts` | Yes | Yes | Yes | Yes | Yes, raw payload | Yes |
| `get_stats` | Yes | Yes | Yes | Yes | Counted separately | Counted separately |
| Viz fact list, detail, graph, and duplicates | Yes | Yes | Yes | Yes | Yes, normalized invalid view | Yes |

For `recall_facts` and operational context, “Yes” also requires valid lifecycle metadata. Legacy facts qualify as current. Canonical current facts rank ahead of ordinary current facts without altering vector similarity scores. `find_related` is an inspection-oriented semantic surface: it keeps all lifecycle states, ranks lifecycle authority tiers, and exposes normalized lifecycle metadata in its output.

Viz does not filter nodes by lifecycle state. Its privacy-safe summaries include the normalized lifecycle block; selected detail responses additionally retain the raw payload unchanged.

## Rollout, future writes, and rollback

The default rollout is read-only and performs no startup migration. Existing payloads are neither rewritten nor backfilled. Missing lifecycle metadata remains compatible through legacy normalization, so deployment does not require a collection-wide mutation.

Lifecycle mutation tools, bulk backfill, relationship maintenance, and migration behavior are intentionally deferred to issue #22. Current write-tool schemas do not promise lifecycle inputs.

Safe rollback is to deploy a version that ignores the additive lifecycle fields. Existing fact text and metadata remain in Qdrant, and no rollout migration needs to be reversed. Before rolling back after future issue #22 mutation support, operators must separately assess facts already classified as non-current because an older server will not enforce lifecycle visibility.
