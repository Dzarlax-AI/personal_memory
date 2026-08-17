---
title: Installation
description: Start Personal Memory locally from immutable release assets, or use the production baseline.
---

## Local installation

The local path requires Docker Engine or Docker Desktop with Docker Compose v2. It needs no Git checkout, source build, Traefik, DNS, TLS, `.env`, root-owned directory, or API key.

Check your architecture:

```bash
uname -m
```

Use `arm64` for Apple Silicon or ARM64 Linux (`arm64`/`aarch64` output). Use `amd64` for Intel or AMD (`x86_64` output).

### macOS and Linux

The following example uses ARM64. Replace both occurrences of `arm64` with `amd64` on an Intel/AMD host:

```bash
mkdir personal-memory
cd personal-memory
curl -fsSLO https://github.com/Dzarlax-AI/personal_memory/releases/latest/download/compose.local-arm64.yaml
curl -fsSLO https://github.com/Dzarlax-AI/personal_memory/releases/latest/download/SHA256SUMS
grep 'compose.local-arm64.yaml' SHA256SUMS | shasum -a 256 -c -
mv compose.local-arm64.yaml compose.yaml
```

For reproducible installation or rollback, replace `latest/download` in both URLs with `download/vX.Y.Z`, using the exact desired release.

### Windows PowerShell

This example uses AMD64, which is the ordinary Windows Docker Desktop architecture:

```powershell
New-Item -ItemType Directory -Path personal-memory
Set-Location personal-memory
Invoke-WebRequest -Uri "https://github.com/Dzarlax-AI/personal_memory/releases/latest/download/compose.local-amd64.yaml" -OutFile "compose.yaml"
Invoke-WebRequest -Uri "https://github.com/Dzarlax-AI/personal_memory/releases/latest/download/SHA256SUMS" -OutFile "SHA256SUMS"
$expected = ((Select-String -Path SHA256SUMS -Pattern 'compose.local-amd64.yaml').Line -split '\s+')[0]
$actual = (Get-FileHash compose.yaml -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actual -ne $expected) { throw "compose.yaml checksum mismatch" }
```

### Start and observe the first model download

Pull images, then start Qdrant and the embedding service once:

```bash
docker compose pull
docker compose up -d memory-qdrant memory-embeddings
```

The first start downloads the pinned embedding model and can take materially longer than later starts. The model cache is a named volume. Observe the existing container; do not repeat the start command as a retry:

```bash
docker compose ps --format json memory-embeddings
docker compose logs --tail 50 memory-embeddings
```

A running container that has not become healthy is still waiting. If it exits non-zero, inspect its bounded recent logs and stop; do not restart it blindly. After 10 minutes, continue reporting that the download is in progress. After 30 minutes, stop waiting and use the same two observation commands later. The existing named cache makes the installation resumable.

When `memory-embeddings` reports healthy, start the application:

```bash
docker compose up -d --wait --wait-timeout 120 memory-mcp
curl -fsS http://127.0.0.1:8000/health
# ok
```

The MCP endpoint is `http://127.0.0.1:8000/memory` and needs no authorization header. Qdrant and TEI have no host ports. Do not change `127.0.0.1:8000:8000` to `0.0.0.0` or another non-loopback address without configuring authentication.

### Stop or remove

This preserves facts, snapshots, and the downloaded model:

```bash
docker compose down
```

`docker compose down -v` permanently removes the named volumes, including stored facts and local snapshots. It is not an ordinary uninstall or recovery command.

See [Connect clients](../connect-clients/), [Troubleshooting](../../operations/troubleshooting/), and [Upgrade and rollback](../../operations/upgrade-rollback/).

## Production-oriented baseline

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
# ok
```

For production environment details, see [Configuration](../../reference/configuration/).
