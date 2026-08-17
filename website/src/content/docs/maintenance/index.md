---
title: Maintenance
---

`forget_old` is a deprecated content-free preview; direct deletion is refused. Maintenance is manifest-bound. Keep writers stopped and manifests/journals in a private directory; tools create these files mode `0600`.

```bash
go build -o ./maintenance ./cmd/maintenance
./maintenance analyze --qdrant-url http://127.0.0.1:6333 --collection memory \
  --output /secure/path/manifest.json
./maintenance quarantine --qdrant-url http://127.0.0.1:6333 --collection memory \
  --manifest /secure/path/manifest.json --journal /secure/path/quarantine.json \
  --confirm-server-stopped --eligible
./maintenance restore --qdrant-url http://127.0.0.1:6333 --collection memory \
  --manifest /secure/path/manifest.json --journal /secure/path/restore.json \
  --confirm-server-stopped --point-id 12345
```

Quarantine accepts either repeatable `--point-id` values or `--eligible`; restore accepts exact IDs only. Both validate the saved manifest and payload fingerprint. A failed, ambiguous, conflict, not-found, or protected/ineligible result is unresolved: inspect its journal before restarting writers. A concurrent write that cannot be verified is ambiguous, never success.

Purge is manual, CLI-only, and requires an explicitly approved maintenance window. It accepts only explicit IDs from the saved manifest; `--eligible` is unavailable. After the selected positive quarantine age fully elapses:

```bash
./maintenance purge --qdrant-url http://127.0.0.1:6333 --collection memory \
  --manifest /secure/path/manifest.json --journal /secure/path/purge.json \
  --snapshot-archive /secure/path/purge-recovery.snapshot \
  --confirm-server-stopped --confirm-purge --minimum-quarantine-days 30 \
  --point-id 12345
```

The archive must be outside `/qdrant/snapshots`. Purge creates and proves a fresh snapshot identity, downloads and checksums the private archive, and re-proves the live snapshot before every deletion. Preserve manifest, journal, and archive together. For a partial result, reuse that journal, snapshot identity, and archive; do not create a post-delete replacement snapshot. Restore a verified archive only through the approved Qdrant recovery procedure while writers remain stopped.
