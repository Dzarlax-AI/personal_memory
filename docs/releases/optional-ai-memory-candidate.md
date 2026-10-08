# Optional AI memory release candidate

Prepared locally on 2026-10-08. This is an unreleased candidate, not a version tag
or a production activation. Integration bundle version is 0.2.0; the application
release version has not been selected.

## Delivered behavior

- Maintenance, write classification and read selection are independently
  configurable and disabled by default.
- Decisions and Jev are optional judgment providers. Maintenance uses an
  operator-selected compatible generative model.
- An MCP client can register project context from bounded source evidence using
  `ensure_project`. Registration needs no inference credentials; changed or
  ambiguous identities remain proposals.
- `subject_scope` and `subject_context` describe the assertion. Recording origin
  remains stored provenance and is omitted from runtime write classification,
  including shadow.
- Personal/tech writes bypass project inference. Missing project context never
  fabricates a project or blocks ordinary recording.
- No automatic namespace migration or background fact regrouping is introduced.
  Maintenance defaults to review; grouping application is a separate operator
  action with stopped writers.

## Candidate files

The runtime scope is `cmd/server`, `cmd/context-catalog`, `cmd/ai-maintenance`,
`cmd/eval-ai-memory`, `internal/aipolicy`, `internal/contextcatalog`,
`internal/aijudgment`, `internal/aimaintenance`, `internal/factgrouping`, the memory
write/read/registration/origin/subject paths, configuration wiring,
`internal/qdrant/grouping.go`, and origin presentation in Viz. Client delivery is
`integrationbundle` and its `cmd/memory-integration` checks. The Makefile,
`.env.example`, optional-AI/reference/integration documentation, What's new page,
and isolated acceptance probe are included.

The current branch contains unrelated edits and older research. Do not stage the
whole worktree. Shared runtime files have accumulated edits and require a review
of their complete diff before integration. Private evaluation corpora, responses,
credentials, `.env`, `.DS_Store`, generated docs and test storage are excluded.
The selected staged tree was exported independently of unrelated dirty work.
With checksum-verified browser dependencies, all Go tests, vet, operator binary
builds and 128 bundle-artifact scenarios passed. The public synthetic evaluator
fixture is included; private evaluation corpora and responses remain excluded.

## Fresh acceptance evidence

- `go test ./...` passed.
- `go vet ./...` passed.
- Server binary built successfully.
- `make integration-bundle-public` passed all 32 scenarios for each of Codex,
  Claude, ChatGPT and generic MCP (128 scenario evaluations). These are artifact
  contract checks, not observations of proprietary clients.
- Quick install checks passed for Codex/Claude with memory-only and document
  presets, using empty temporary client roots.
- Authenticated MCP HTTP client → built server → isolated real Qdrant passed:
  health, initialization, tool discovery, life write before catalog creation,
  registration created/existing, registry-only project write, and recall preserving
  origin/subject/namespace without inferred primary grouping.
- Runtime synchronous/shadow tests verified omission of origin from provider input
  and preservation in storage.
- Astro diagnostics: zero errors/warnings/hints. Fresh docs build and local link
  check results are recorded with the candidate verification record.

The protocol probe uses deterministic synthetic embeddings, not TEI model quality.
It uses synthetic facts, no provider credentials, and zero external model calls.
The empty Qdrant container is removed after the probe. This does not establish a
production deployment, real Codex/Claude client behavior, or model quality.

Reproduce the storage/protocol probe from the repository root with:

```sh
GOCACHE=/tmp/personal-memory-probe-cache scripts/probe-optional-ai-memory.sh
```

Docker must already contain the pinned Qdrant image named in the script. The
probe creates no persistent volume and installs no client instructions. It writes
its local verification result under ignored `eval-results/`.

## Quality disposition

The final origin ablation used 18 authored subjects, two repeated runs, two
conditions and two providers. All 144 responses were valid and accepted by
byte-exact offline adapter replay. Both repeats agreed: Decisions 15/18 without
origin and 15/18 with it; Jev 17/18 and 15/18. Both incorrectly assigned an
underdetermined Go-bot assertion to its recording project in the origin condition.
Removing origin is the accepted engineering boundary. It is not production
accuracy approval. Read selection and maintenance have separate quality gates.

## Publication and rollout boundary

Local preparation is complete after the recorded checks. Publication requires an
explicit commit/push/PR decision under this repository's policy. Deployment is a
separate reviewed change in `personal_ai_stack/deploy/memory` with an immutable
application reference. Start with all AI features off; do not introduce a
production config, catalog or stored-fact migration with this candidate.

Client bundle installation is separate and uses an explicit client root. Existing
clients keep baseline memory behavior; ChatGPT needs its official UI/admin step.
A later real-client acceptance must name the client, endpoint and isolated test
namespace before writing any records. Do not label the current Go-client probe
as proprietary-client acceptance.

## Rollback

Keep the prior application reference and client bundle backup. Disable optional
features independently; removing `MEMORY_AI_CONFIG_FILE` returns to default-off
operation, including registration. Retain source origin and subject metadata as
stored data. Use the normal compare-before-restore bundle rollback at the exact
installed root. No bulk fact rewrite follows rollback.
