---
title: Lifecycle migration
---

Lifecycle migration never runs at startup. Preview is read-only; do not pass write confirmations or a manifest:

The published container installs this executable at `/personal-memory-migrate-lifecycle`; locally built binaries may use a different path.

```bash
/personal-memory-migrate-lifecycle
```

Create and verify a Qdrant snapshot, then stop every memory writer. Apply requires the stopped-writer confirmation and an exclusive immutable manifest path:

```bash
/personal-memory-migrate-lifecycle -apply -confirm-writes-stopped \
  -rollback-manifest /secure/path/memory-lifecycle-rollback.jsonl
```

Verify the reported `scanned`, `planned`, `applied`, `already_applied`, `conflicts`, and `invalid` counts and inspect current reads before resuming writers. The manifest is lifecycle-only and mode `0600`, but remains operational backup material.

If apply is interrupted, keep writers stopped and resume with the exact same immutable manifest command. Do not create a new manifest:

```bash
/personal-memory-migrate-lifecycle -apply -confirm-writes-stopped \
  -rollback-manifest /secure/path/memory-lifecycle-rollback.jsonl
```

For rollback, stop writers again and use the exact manifest:

```bash
/personal-memory-migrate-lifecycle -rollback /secure/path/memory-lifecycle-rollback.jsonl \
  -confirm-writes-stopped
```

Rollback restores only unchanged migration targets and reports conflicts non-zero; it never overwrites post-migration lifecycle changes. Treat conflicts or invalid records as a stop condition, preserve the snapshot and manifest, and investigate before restarting writers.
