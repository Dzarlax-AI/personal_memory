---
title: Backups and releases
---

The server creates and prunes Qdrant snapshots using `BACKUP_INTERVAL_HOURS` and `KEEP_SNAPSHOTS`. Snapshot retention is not a substitute for a verified restore procedure.

Before a release, record the immutable application image, verify a usable snapshot, review `.env` and external Traefik configuration, and confirm the expected feature flags. Deploy only the reviewed image. After deployment, check `/health`, logs, and an authenticated real client path. Keep the preceding immutable image reference available for rollback.

Restoring data is an operator action against Qdrant, not an automatic server operation. Stop writers before restoring a memory collection, verify the intended snapshot identity, restore using Qdrant's documented procedure, then validate reads before resuming writers.
