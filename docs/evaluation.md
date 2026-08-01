# Memory evaluation

`cmd/eval-memory` answers two different questions:

1. Did retrieval return the semantically relevant facts or document chunks?
2. Did lifecycle policy present each fact safely for the requested intent?

The evaluator keeps those answers separate. A relevant obsolete fact can have a
high vector score and still be correctly suppressed for current context.
Likewise, canonical preference is reported independently from MRR and nDCG so
an authority decision is not disguised as semantic relevance.

## Choose an intent

Schema-v2 fact queries declare one of four intents:

| Intent | Use it when | Lifecycle presentation |
| --- | --- | --- |
| `current` | Building current model context | Include valid, unexpired current facts; suppress historical, superseded, disputed, invalid, and expired facts. An ordinary current fact is demoted when a valid canonical current candidate is present. |
| `history` | Inspecting how facts changed | Include valid, unexpired current, historical, and superseded facts with their state visible. Disputed candidates remain uncertain. |
| `as_of` | Inspecting state at a fixed date | Apply history-style presentation and evaluate `valid_until` at the query's required `as_of` date. |
| `uncertainty` | Reviewing unresolved alternatives | Keep each valid, unexpired disputed candidate visible as uncertain; do not choose a winner. |

There is currently no `valid_from` field. `as_of` can therefore prove that a
fact had expired by the query date, but it cannot prove that the fact already
existed on that date. `verified_at` records verification time; it is not a
validity start and the evaluator never treats it as one.

Document queries support current intent only and do not use lifecycle
expectations.

## Read lifecycle results

Each fact candidate reports its normalized state, whether it is canonical,
whether it is expired at the reference date, its presentation decision, stable
reason codes, and whether its explicit lifecycle metadata is valid.

The decisions are:

- `include`: present normally;
- `suppress`: exclude from the requested view;
- `demote`: retain, but below a stronger current authority;
- `uncertain`: retain without selecting a winner.

Presentation reason codes are deliberately closed and non-sensitive:
`current_truth`, `canonical_preference`, `current_context`, `historical`,
`historical_context`, `superseded`, `superseded_context`, `disputed`,
`invalid_lifecycle`, and `expired`.

Transition scenarios report `transition_valid`, `source_invalid`,
`target_invalid`, or `transition_invalid`. Public v2 cases cover every valid
target state, idempotent transitions, and invalid lifecycle invariants.

## Metrics and the blocking gate

Reports keep four metric groups distinct:

- relevance: Hit@K, MRR, and nDCG@K;
- lifecycle: declared candidate checks and violations;
- canonical preference: ordinary-current demotion checks and violations;
- transitions: expected validity and reason-code checks for each scenario.

`forbid_invariant_violations` blocks forbidden result IDs and other retrieval
invariant failures. `forbid_lifecycle_violations` blocks mismatched lifecycle
decisions, states, reason codes, canonical preference behavior, and transition
expectations. Minimum Hit@K, MRR, and nDCG@K gates continue to apply
independently. `make eval-public` compares the candidate with the checked-in
baseline and uses `--enforce-gates`, so any blocking gate makes the command
fail.

## Run the public evaluation

The checked-in v2 dataset is fully synthetic: payload text, paths, point IDs,
vectors, and embedding identity are constructed for evaluation. The vectors
are not TEI output, so the public lifecycle gate needs Qdrant but no TEI
service.

Start a dedicated local Qdrant, then run:

```bash
make eval-public
```

Equivalent direct invocation:

```bash
go run ./cmd/eval-memory run \
  --source fixture \
  --dataset evaldata/public/v2/dataset.json \
  --qdrant-url http://127.0.0.1:6333 \
  --json eval-results/public.json \
  --markdown eval-results/public.md

go run ./cmd/eval-memory compare \
  --baseline evaldata/public/v2/baseline.json \
  --candidate eval-results/public.json \
  --json eval-results/comparison.json \
  --enforce-gates
```

Fixture mode validates the complete dataset before contacting Qdrant. It
creates three unique collections whose names begin with `eval_`, seeds the
synthetic points, evaluates every query, and deletes only collections created
by that run. It never mutates the logical `memory`, `doc_chunks`, or
`doc_folders` collections.

Use a local test Qdrant for fixture mode. Never point it at production or a
shared memory service. A hard kill can leave a uniquely named `eval_*`
collection behind; inspect the collection list and remove only the exact leaked
test collection.

## Run private supplementary cases

Private v2 datasets can supplement the public synthetic scenarios with
user-specific phrasing and expected IDs. Store them under
`evaldata/private/`; that directory and `eval-results/` are ignored by Git.
Production facts, document paths, point IDs, and credentials must never enter
public fixtures, baselines, CI artifacts, or commits.

Live mode is read-only and searches the logical collection names declared by
the dataset. It does not create collections, upsert points, delete data, or
increment recall counters:

```bash
go run ./cmd/eval-memory run \
  --source live \
  --dataset evaldata/private/my-v2-live-set.json \
  --qdrant-url http://127.0.0.1:6333 \
  --json eval-results/private.json \
  --markdown eval-results/private.md
```

Private live queries may omit vectors when `--embed-url` is supplied. The CLI
checks TEI `/info` and rejects model ID, revision, dtype, or pooling mismatches
before embedding.

## Schema and report compatibility

Dataset schema v1 remains accepted and emits report schema v1 exactly as
before. The immutable `evaldata/public/v1/dataset.json` and
`evaldata/public/v1/baseline.json` files are retained as historical,
byte-compatibility artifacts.

Dataset schema v2 adds intents, fixed-date `as_of`, lifecycle expectations,
transition scenarios, and the lifecycle gate. It emits report schema v2 with a
required lifecycle section. Comparisons require compatible dataset/report
identity; a v1 baseline is not a substitute for the v2 lifecycle baseline.

Canonical reports omit timestamps, physical temporary collection names, and
latency so independent fixture runs are byte reproducible.

## Update the public dataset

Treat a public dataset and its baseline as one reviewed unit:

1. Bump `dataset_version` for semantic corpus or relevance changes.
2. Keep all public text, paths, IDs, and vectors synthetic.
3. Record the complete embedding identity; never label constructed vectors as
   model output.
4. Review every expected ID, grade, forbidden ID, lifecycle decision, state,
   reason code, transition result, and gate change.
5. Run fixture mode twice against a dedicated local Qdrant into separate JSON
   and Markdown files.
6. Byte-compare both JSON files and both Markdown files.
7. Verify the local Qdrant has no leaked `eval_*` collections.
8. Replace the checked-in baseline with the freshly generated report.
9. Run `make eval-public`, the Go test suite, and `go vet ./...`.
