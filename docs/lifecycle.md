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

Deployment remains read-only and performs no startup migration. Existing payloads are neither rewritten nor backfilled automatically. Missing lifecycle metadata remains compatible through legacy normalization, so deployment does not require a collection-wide mutation.

`store_fact` and `update_fact` accept optional lifecycle inputs. Omitting every lifecycle input preserves the legacy-compatible store behavior and the existing update metadata respectively. Supplying any lifecycle input constructs a complete explicit target: the state defaults to `current`, canonical defaults to false, relationships default to empty, and omitted provenance or verification metadata is absent.

`set_fact_lifecycle` is the metadata-only transition path. It requires an exact numeric or UUID point ID, never performs semantic target selection, and never calls the embedding service. The request describes a complete target lifecycle view. Qdrant updates are restricted to lifecycle keys, so text, vectors, recall counters, and unrelated payload fields are not rewritten. Changing a state does not infer or maintain reciprocal relationships.

`import_facts` preserves valid lifecycle metadata from exported facts. Entries with malformed explicit lifecycle metadata are skipped without logging their fact text.

### Explicit legacy migration

`personal-memory-migrate-lifecycle` classifies only payloads with none of the six lifecycle keys. Its sole deterministic target is explicit `current`, `canonical=false`, and empty relationship arrays. Expiry, retention, text, tags, and similarity do not influence classification.

The command is dry-run-only unless `-apply` is supplied. Apply requires every memory writer to be stopped and an exclusive rollback manifest path. The manifest is created with mode `0600` before the first mutation and contains point IDs plus lifecycle-only before/after metadata; it never contains fact text or vectors. A partial apply resumes from the same immutable manifest.

Before production apply:

1. Create and verify a Qdrant snapshot of the memory collection.
2. Stop every server, importer, migration, and other memory writer.
3. Run the lifecycle migration without `-apply` and review its counts and point IDs.
4. Run apply with `-confirm-writes-stopped` and a new `-rollback-manifest` path.
5. Re-run apply with the same manifest to verify zero remaining changes before restarting writers.

Rollback also requires stopped writers. It restores a point only when its current lifecycle subset still exactly matches the migration-applied target. A deliberate post-migration change is reported as a conflict and is never overwritten. If manifest rollback cannot be completed, restore the pre-migration Qdrant snapshot according to the infrastructure runbook.
