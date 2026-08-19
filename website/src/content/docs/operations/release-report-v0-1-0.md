---
title: v0.1.0 production validation
description: Privacy-safe post-release production soak and release-gate evidence for v0.1.0.
---

Personal Memory v0.1.0 passed its production validation after publication. This
report is post-release evidence, not evidence that the gate preceded the
release.

## Decision

**Gate result: pass. Rollback decision: remain on v0.1.0.**

The validated candidate was release `v0.1.0`, commit
`d7d61501c4955aeee1a04eeebb738916de893d97`, deployed through the immutable
application tag `sha-d7d6150`. The preceding immutable tag `sha-fa275a4`
remained available from the registry as the rollback candidate.

No migration, maintenance mutation, reindex, restart, configuration edit,
rollback, restore, purge, or secret rotation was performed during validation.

## Production soak

The formal window started at 2026-08-17 15:05 UTC. The final checkpoint was
captured at 2026-08-19 23:20 UTC, more than 56 hours later.

At the final checkpoint:

- the application had run continuously from before T0 with zero restarts;
- the application image and reviewed Compose input were unchanged;
- aggregate application logs since T0 contained zero error, fatal, or warning
  matches;
- the public health route returned 200, unauthenticated Memory access was
  rejected, the disabled embedded Todoist route was absent from the direct
  service, and Viz still required its application authentication boundary;
- all three expected Qdrant collections reported green status with healthy
  optimizers;
- the snapshot retention set was present and included a successful backup
  created after T0;
- bounded resource observations showed no operational pressure requiring a
  configuration change; and
- the connected production client exposed all 15 enabled Memory and Documents
  tools, while read-only statistics and document-search calls succeeded.

There was no public intermediate checkpoint comment between T0 and the final
checkpoint. Continuity was established retrospectively from the unchanged
container start time, zero restart count, aggregate logs, unchanged image and
configuration identities, and post-T0 snapshot activity. This limitation is
recorded rather than presenting the window as continuous external synthetic
monitoring.

## Reproduced release gates

The following deterministic or isolated checks passed at T0:

- the full Go repository test target;
- all 32 public model-policy conformance scenarios;
- all 32 integration-bundle scenarios for each of Codex, Claude, ChatGPT, and
  generic MCP clients;
- both public v3 retrieval reports over the versioned 21-query dataset, with
  byte-for-byte agreement with the pinned evidence;
- all four public v4 document-routing reports over their versioned 8-query
  dataset, including the expected controlled no-winner gate rejections;
- local release and agent-setup validation scripts;
- documentation diagnostics and static build;
- an isolated synthetic lifecycle migration dry-run; and
- isolated maintenance analysis plus confirmation that mutation commands
  refused execution without their required operator gates.

The release's retrieval experiments did not establish a candidate that met all
protected-cohort gates. Production therefore remained on the existing vector
retrieval and hierarchical document-routing configurations.

## Recovery readiness

A fresh Memory collection snapshot was created and positively re-listed at T0.
Scheduled snapshot activity continued after T0. Exact snapshot identities and
operational locations are retained privately and are not part of this report.

The preceding application image `sha-fa275a4` was no longer present in the VPS
Docker cache at the final checkpoint, but its registry manifest remained
available. The reviewed rollback procedure therefore remained actionable
through an explicit pull and restart decision.

No real rollback or snapshot restore was executed. Those actions require a
separate stopped-writer or deployment approval and would create unnecessary
production risk in a passing validation. The command path, immutable image,
snapshot prerequisite, and mandatory post-rollback checks were reviewed
without mutating production.

## Known limitations

- v0.1.0 was published before this gate completed.
- The soak used T0 and final evidence plus retained service signals; it did not
  have a separately published intermediate checkpoint.
- The validation proves the deployed production configuration and the public
  deterministic fixtures. It does not copy private facts, documents, queries,
  identifiers, paths, or vectors into public evidence.
- Clean-machine installation validation and native Windows client integration
  remain tracked separately.

These limitations do not require rollback of the stable candidate. Future
releases should complete the release gate before publication and automate
privacy-safe intermediate checkpoint capture.
