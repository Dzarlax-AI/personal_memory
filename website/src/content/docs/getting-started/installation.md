---
title: Installation
description: Prepare and start the production-oriented Compose baseline.
---

The checked-in Compose configuration expects Docker Engine, Docker Compose, an external `traefik` network, Traefik v3 with HTTPS, DNS for `mcp.<domain>`, and writable Qdrant storage and snapshot directories. It does not start Traefik.

```bash
git clone https://github.com/Dzarlax-AI/personal_memory.git
cd personal_memory
sudo mkdir -p /root/memory/qdrant_storage /root/memory/qdrant_snapshots
docker network inspect traefik >/dev/null
cp .env.example .env
openssl rand -hex 32
```

Set the generated value as `API_KEY`, choose `MEMORY_DOMAIN`, and keep insecure authentication disabled:

```dotenv
MEMORY_DOMAIN=example.com
API_KEY=replace_with_generated_secret
ALLOW_INSECURE_AUTH=false
ENABLE_RAG=false
ENABLE_TODOIST=false
ENABLE_VIZ=false
```

The checked-in Compose file uses `:latest`, so select a reviewed immutable application image before the first pull or start:

```bash
cat > compose.release.yml <<'EOF'
services:
  memory-mcp:
    image: ghcr.io/dzarlax-ai/personal-memory:sha-<commit>
EOF
docker compose -f docker-compose.yml -f compose.release.yml config
docker compose -f docker-compose.yml -f compose.release.yml pull
docker compose -f docker-compose.yml -f compose.release.yml up -d
docker compose -f docker-compose.yml -f compose.release.yml ps
curl -fsS https://mcp.example.com/health
# ok
```

The first TEI startup downloads the pinned embedding model. For environment details, see [Configuration](../reference/configuration/) and [Upgrade and rollback](../operations/upgrade-rollback/).
