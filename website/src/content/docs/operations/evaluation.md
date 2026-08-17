---
title: Retrieval evaluation
---

# Memory evaluation

`cmd/eval-memory` evaluates retrieval ranking and lifecycle presentation as
separate concerns. A semantically relevant obsolete fact can rank highly and
still need to be suppressed for current context. Canonical preference is also
reported separately from MRR and nDCG so authority is not mistaken for
semantic relevance.

The current public contract is `evaldata/public/v3/`. Public v1 and v2 remain
checked in as historical compatibility artifacts; CI does not use them as the
current baseline.

## Schema v3 experiment identity

A schema-v3 dataset and report identify an experiment with all of the
following:

- `schema_version` and `dataset_version`;
- the complete embedding identity: provider, model ID, immutable revision,
  dtype, pooling, vector size, and input profile;
- the named retrieval configuration: logical collection names, folder
  settings, evaluated `top_k`, retrieval strategy, dense candidate limit, and
  RRF constant;
- the run mode: `fixture`, `tei-fixture`, or `live`;
- per-query cohort membership and deterministic cohort aggregates.

The input profile is part of vector identity. `legacy-raw-v1` sends the stored
text without role prefixes. `multilingual-e5-v1` applies the model-specific
query/passage roles and is valid only for
`intfloat/multilingual-e5-small`. Changing the profile requires re-embedding
the corpus and queries; a profile flag must never relabel existing vectors.
The evaluator and materializer support both profiles. The production server
and standalone indexer currently reject non-legacy profiles during
configuration validation because their memory and RAG embedding calls remain
raw-text paths.

Queries can belong to more than one cohort. Public v3 reports these stable
cohorts:

| Cohort | Purpose |
| --- | --- |
| `exact-name` | Exact names and filenames that lexical retrieval should protect. |
| `identifier-path` | UUIDs, cluster names, and path-like identifiers. |
| `general-semantic` | Ordinary semantic retrieval coverage. |
| `lifecycle` | Retrieval cases with lifecycle presentation expectations. |
| `multilingual` | Cross-language and non-English retrieval. |

`exact-name` and `identifier-path` are protected comparison cohorts.
`general-semantic`, `lifecycle`, and `multilingual` remain visible for
diagnosis and aggregate scoring.

## Lifecycle intents

Fact queries declare one of four intents:

| Intent | Use it when | Lifecycle presentation |
| --- | --- | --- |
| `current` | Building current model context | Include valid, unexpired current facts; suppress historical, superseded, disputed, invalid, and expired facts. Demote an ordinary current fact when a valid canonical current candidate is present. |
| `history` | Inspecting how facts changed | Include valid, unexpired current, historical, and superseded facts with their state visible. Keep disputed candidates uncertain. |
| `as_of` | Inspecting state at a fixed date | Apply history-style presentation and evaluate `valid_until` at the required `as_of` date. |
| `uncertainty` | Reviewing unresolved alternatives | Keep each valid, unexpired disputed candidate visible without choosing a winner. |

There is no `valid_from` field. `as_of` can prove that a fact had expired by
the query date, but cannot prove that it already existed then. `verified_at`
records verification time and is not treated as a validity start.

Document queries support current intent only and do not use lifecycle
expectations.

Each fact candidate reports its normalized state, canonical flag, expiry,
presentation decision, stable reason codes, and explicit-metadata validity.
The decisions are `include`, `suppress`, `demote`, and `uncertain`.

Transition scenarios report `transition_valid`, `source_invalid`,
`target_invalid`, or `transition_invalid`. Public v3 retains the complete
16-case source-to-target matrix across `current`, `historical`, `superseded`,
and `disputed`, plus invalid-metadata cases.

## Conservative gates

Dataset gates independently enforce minimum Hit@K, MRR, nDCG@K, retrieval
invariants, lifecycle decisions, canonical preference, and transition
expectations.

Schema-v3 comparisons add a conservative candidate gate:

- the candidate must pass its dataset gates;
- aggregate ranking metrics must not regress;
- aggregate invariant violations and lifecycle violations must remain zero and
  must not regress;
- neither protected cohort may regress on MRR, Hit@K, nDCG@K, or invariant
  violations;
- at least one ranking metric must improve in at least one protected cohort.

This gate deliberately rejects a candidate that improves only an aggregate or
an exploratory cohort. `--enforce-gates` returns a failing exit for that
result; it must not be disabled to manufacture a winner.

## Run modes

| Source | Corpus and query vectors | Qdrant behavior | Intended use |
| --- | --- | --- | --- |
| `fixture` | Uses vectors already in the dataset. | Creates uniquely named temporary `eval_*` collections, seeds them, evaluates them, and deletes only those collections. | Deterministic replay without TEI. |
| `tei-fixture` | Re-embeds the synthetic corpus and queries with the declared profile. | Uses the same isolated temporary-collection lifecycle as fixture mode. | Model/profile experiments and diagnostic timing. |
| `live` | Uses stored collection vectors and embeds only queries that omit vectors. | Read-only access to the logical collections named in the dataset. It does not create, upsert, delete, or increment recall counters. | Private supplementary evaluation against an existing service. |

Use a dedicated local Qdrant for both fixture modes. Never point them at
production or a shared memory service. A hard kill can leave a uniquely named
`eval_*` collection; inspect the collection list and remove only the exact
leaked evaluation collection.

Store private datasets and generated reports under `evaldata/private/` and
`eval-results/`. Both are ignored by Git. Production facts, paths, point IDs,
credentials, and private vectors must never enter public fixtures, baselines,
CI artifacts, or commits.

## Deterministic public v3 CI replay

Start an isolated Qdrant and run:

```bash
make eval-public QDRANT_TEST_URL=http://127.0.0.1:6333
```

The target runs two `source=fixture` rankings:

1. `public-v3-legacy-raw-vector-only`, byte-compared with
   `evaldata/public/v3/baseline.json`;
2. `public-v3-legacy-raw-hybrid-rrf60-candidate`, with a dense candidate limit
   of 40 and RRF constant 60, byte-compared with
   `evaldata/public/v3/hybrid-rrf60-candidate.json`.

Generated JSON and Markdown are written under `eval-results/`. Fixture reports
omit timestamps, temporary collection names, and timing, so canonical JSON is
byte reproducible. Schema-v3 rendering rounds diagnostic result scores to five
decimal places so insignificant Qdrant float32 differences across CPU
architectures do not change evidence bytes; ranking order and all metrics
remain unrounded. CI starts only Qdrant; it does not start TEI, download a model,
or require a GPU.

The pinned
`evaldata/public/v3/hybrid-rrf60-failing-comparison.json` is the separately
verified cross-configuration comparison. It records the expected conservative
gate failure and is not used as a passing ranking target.

Equivalent direct commands are:

```bash
go run ./cmd/eval-memory run \
  --source fixture \
  --dataset evaldata/public/v3/dataset.json \
  --qdrant-url http://127.0.0.1:6333 \
  --json eval-results/public-v3-baseline.json \
  --markdown eval-results/public-v3-baseline.md
cmp evaldata/public/v3/baseline.json \
  eval-results/public-v3-baseline.json

go run ./cmd/eval-memory run \
  --source fixture \
  --dataset evaldata/public/v3/dataset.json \
  --qdrant-url http://127.0.0.1:6333 \
  --configuration-name public-v3-legacy-raw-hybrid-rrf60-candidate \
  --retrieval-strategy hybrid-rrf \
  --dense-candidate-limit 40 \
  --rrf-constant 60 \
  --json eval-results/public-v3-hybrid-rrf60.json \
  --markdown eval-results/public-v3-hybrid-rrf60.md
cmp evaldata/public/v3/hybrid-rrf60-candidate.json \
  eval-results/public-v3-hybrid-rrf60.json
```

## Deterministic public v4 document-routing replay

Schema v4 adds an independent `document_routing_strategy`, bounded routing
parameters, and privacy-safe per-query routing traces. It leaves every v3 byte
unchanged. Routing decisions are recorded only in `reason_codes`; the separate
`reranker_reason` field records the bounded reranker outcome. Both fields and
result source names are validated against fixed allowlists.

The compact synthetic v4 corpus reuses pinned v3 vectors for eight queries:
seven document cases covering misleading or missing folder summaries, an exact
product name, and identifiers/paths, plus one multilingual control.

```bash
make eval-public-v4 QDRANT_TEST_URL=http://127.0.0.1:6333
```

The target byte-replays hierarchical-only, flat-only, blended RRF, and an
explicitly unavailable reranker fail-open case, then byte-recomputes strict
comparisons. Expected quality-gate rejection exits with status `3`; status `1`
is reserved for usage, input, output, and other errors. Only isolated Qdrant
and precomputed fixture vectors are used.

Schema-v4 diagnostic result scores are rounded to four decimal places. This
absorbs observed float32 boundary differences between Linux and macOS Qdrant
without changing ranking order or metrics; historical schema-v3 artifacts
retain their five-decimal canonicalization unchanged.

The unavailable case is a resilience check, not a model-quality benchmark: it
records `reranker_fallback` and preserves the deterministic blended order.
Against hierarchical-only, blended RRF improved aggregate MRR from `0.59375`
to `0.65` without regressing either protected cohort, but it did not strictly
improve one as required. Flat-only reached aggregate MRR `0.828125`, but
regressed protected identifier/path MRR from `0.375` to `0.3125`. Therefore
the benchmark has no winner: the runtime default remains hierarchical-only and
reranking remains disabled. Real reranker quality and latency evidence requires
a separately provisioned, identity-matched endpoint and remains follow-up work.

## Materialize vectors with TEI

`materialize` creates a reusable schema-v3 fixture by replacing every fact,
chunk, folder, and query vector with output from a verified TEI instance:

```bash
go run ./cmd/eval-memory materialize \
  --dataset evaldata/public/v3/dataset.json \
  --embed-url http://127.0.0.1:8080 \
  --embed-model intfloat/multilingual-e5-small \
  --input-profile multilingual-e5-v1 \
  --output evaldata/private/public-v3-e5-materialized.json
```

The command verifies TEI model ID, immutable revision, dtype, and pooling
before embedding. It writes atomically with mode `0600`, refuses to overwrite
the input path, validates the result, and does not echo provider response
bodies. Keep the output private even when its source text is synthetic: it is
large, environment-specific benchmark material and is not a public baseline
until it completes the rebaseline review.

Run a materialized dataset with `source=fixture`; `tei-fixture` would embed it
again. For one-off model/profile experiments, use `tei-fixture` directly:

```bash
go run ./cmd/eval-memory run \
  --source tei-fixture \
  --dataset evaldata/public/v3/dataset.json \
  --qdrant-url http://127.0.0.1:6333 \
  --embed-url http://127.0.0.1:8080 \
  --input-profile multilingual-e5-v1 \
  --configuration-name v3-c-multilingual-e5-vector-only \
  --json eval-results/v3-c.json \
  --markdown eval-results/v3-c.md
```

## Four-configuration experiment

Use one pinned TEI instance, one isolated Qdrant, the same dataset revision,
and these four configurations:

| ID | Input profile | Strategy | Candidate settings |
| --- | --- | --- | --- |
| A | `legacy-raw-v1` | `vector-only` | Dataset defaults. |
| B | `legacy-raw-v1` | `hybrid-rrf` | `dense-candidate-limit=40`, `rrf-constant=60`. |
| C | `multilingual-e5-v1` | `vector-only` | Re-embed corpus and queries. |
| D | `multilingual-e5-v1` | `hybrid-rrf` | Re-embed corpus and queries; limit 40, RRF 60. |

Run A through D with `source=tei-fixture`, distinct stable
`--configuration-name` values, and matching output paths. B changes only the
retrieval flags. C changes `--input-profile` and therefore re-embeds. D applies
both sets of overrides. Compare B, C, and D against A with
`--enforce-gates`. Do not compare different dataset versions, model
identities, or run modes.

For latency diagnosis, run each relevant configuration repeatedly on the same
idle host and report the per-run distributions. Timing is informational:
environment load, Qdrant placement, TEI placement, and corpus size all affect
it. There is no automatic latency threshold and ranking gates never infer one.

## Bounded public v3 decision

Public v3.1.0 contains 48 facts, including 41 current or legacy retrieval
candidates, plus 41 chunks and 41 folder summaries. This makes each retrieval
pool exceed the candidate limit of 40 while keeping the fixture bounded.

The four-configuration benchmark found no qualifying candidate:

- legacy raw plus hybrid RRF produced a small aggregate improvement, but
  exact-name and identifier-path protected cohorts had zero ranking
  improvement, so the conservative comparison gate failed;
- both `multilingual-e5-v1` configurations regressed aggregate ranking against
  the legacy raw baseline;
- in the same-host five-run diagnostic, hybrid search p95 was roughly twice
  vector-only search p95. This is evidence for that emulated host and fixture,
  not a universal production latency claim.

The decision is therefore no rollout: production remains
`legacy-raw-v1` with vector-only retrieval. Hybrid RRF remains evaluator
experiment machinery, not an enabled production retrieval feature. There is
no embedding migration, deploy change, or production reindex associated with
this benchmark. Server and standalone-indexer startup also fail closed if a
non-legacy input profile is configured, preventing experimental identity
metadata from being attached to raw runtime vectors.

## Private live evaluation

Live mode is for private supplementary cases:

```bash
go run ./cmd/eval-memory run \
  --source live \
  --dataset evaldata/private/my-v3-live-set.json \
  --qdrant-url http://127.0.0.1:6333 \
  --embed-url http://127.0.0.1:8080 \
  --documents-root /path/to/private/documents \
  --json eval-results/private.json \
  --markdown eval-results/private.md
```

The CLI checks TEI identity before embedding missing query vectors. Keep live
reports private because result IDs and retrieval ordering can reveal details
about the indexed corpus.

## Version and rebaseline public evidence

Treat the dataset and all pinned evidence as one reviewed unit:

1. Bump `dataset_version` for any semantic corpus, query, relevance, cohort,
   gate, identity, or retrieval-configuration change.
2. Keep public text, paths, point IDs, and vectors synthetic.
3. Record the full embedding and configuration identity. Never label
   constructed vectors as TEI output.
4. Review every expected ID and grade, forbidden ID, cohort assignment,
   lifecycle expectation, transition, and gate.
5. If vectors change, materialize them privately with the pinned TEI, confirm
   the output is mode `0600`, and review before copying any approved synthetic
   artifact into the public version directory.
6. Run each fixture configuration twice against an isolated Qdrant and
   byte-compare JSON and Markdown between runs.
7. Recompute cross-configuration comparisons with `--enforce-gates`; preserve
   a failing comparison when the evidence says there is no winner.
8. Verify Qdrant has no leaked `eval_*` collections.
9. Replace pinned reports only after review, update their contract hashes and
   tests, then run `make eval-public`, `make eval-public-v4`, `go test ./...`,
   and `go vet ./...`.

Schema v1 and v2 remain accepted, and schema v3 remains immutable historical
retrieval evidence. Schema v4 is the current document-routing evidence.
