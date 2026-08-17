---
title: Upgrade and rollback
---

## Local release assets

Keep the current file as a rollback candidate, download the architecture-specific asset for the desired version, and verify its checksum before replacing `compose.yaml`:

```bash
cp compose.yaml compose.previous.yaml
curl -fsSLo compose.next.yaml https://github.com/Dzarlax-AI/personal_memory/releases/download/vX.Y.Z/compose.local-arm64.yaml
curl -fsSLo SHA256SUMS.next https://github.com/Dzarlax-AI/personal_memory/releases/download/vX.Y.Z/SHA256SUMS
grep 'compose.local-arm64.yaml' SHA256SUMS.next | sed 's/compose.local-arm64.yaml/compose.next.yaml/' | shasum -a 256 -c -
mv compose.next.yaml compose.yaml
docker compose pull
docker compose up -d memory-qdrant memory-embeddings
```

Use `amd64` instead of `arm64` on Intel/AMD. Observe TEI as described in [Installation](../../getting-started/installation/), then start the application:

```bash
docker compose up -d --wait --wait-timeout 120 memory-mcp
curl -fsS http://127.0.0.1:8000/health
```

For rollback, restore `compose.previous.yaml`, pull its pinned images, and repeat the same staged start. Named volumes remain in place:

```bash
mv compose.previous.yaml compose.yaml
docker compose pull
docker compose up -d memory-qdrant memory-embeddings
docker compose up -d --wait --wait-timeout 120 memory-mcp
```

Stop if a release declares a data migration or embedding identity changes. Such a release needs a separate migration and rollback contract; deleting volumes is not rollback.

## Production deployment

Preflight reviewed configuration and compatibility, then take and identify a Qdrant snapshot. The checked-in Compose file uses `:latest`; before pulling or starting anything, create a reviewed local override that selects an immutable application image. Keep the previous immutable reference for rollback.

```bash
# Replace the example tag with the reviewed published sha-* tag.
cat > compose.release.yml <<'EOF'
services:
  memory-mcp:
    image: ghcr.io/dzarlax-ai/personal-memory:sha-0123456789abcdef
EOF
docker compose -f docker-compose.yml -f compose.release.yml config
docker compose -f docker-compose.yml -f compose.release.yml pull
docker compose -f docker-compose.yml -f compose.release.yml up -d
docker compose -f docker-compose.yml -f compose.release.yml ps
curl -fsS https://mcp.example.com/health
```

Health alone is insufficient: connect an authenticated client to `/memory` and exercise its expected path. If it fails, inspect logs and change the override to the previous immutable image; run `pull` and `up -d` with both files again, then recheck health plus the client path.

Stop if embedding identity rejects a model or collection mismatch, snapshots are unavailable, or the release includes a lifecycle/data operation. Those require a separate recovery decision.
