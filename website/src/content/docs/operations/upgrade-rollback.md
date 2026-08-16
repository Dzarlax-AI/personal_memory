---
title: Upgrade and rollback
---

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
