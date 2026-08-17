# Personal Memory

Personal Memory is a self-hosted semantic memory layer for MCP-compatible AI clients. It stores user-controlled facts, retrieves relevant context across sessions, and optionally provides document search, Todoist tools, and a visualization dashboard.

## Capabilities

- Semantic fact storage, recall, lifecycle management, export, and inspection.
- Optional Markdown/text RAG with incremental indexing.
- Optional Todoist MCP endpoint; the token remains server-side.
- Optional authenticated visualization dashboard.
- Qdrant snapshots and embedding-identity verification.

## Architecture

```text
MCP client -> Traefik -> Personal Memory (Go) -> TEI -> Qdrant
                                      ├-> Todoist (optional)
                                      └-> RAG/Viz (optional)
```

`/memory` is the Streamable HTTP MCP endpoint. `/todoist` exists only when its feature is enabled. TEI and Qdrant are internal services. Before serving requests, the application verifies the configured embedding identity against every active collection.

## Recommended: ask Codex or Claude Code to set it up

For a new local installation, paste this into Codex or Claude Code:

```text
Install a new local Personal Memory service and connect this client by following
https://github.com/Dzarlax-AI/personal_memory/releases/latest/download/agent-setup.md
Show me the exact release and planned changes before writing anything. Stop at
every reconnect required by the playbook, and do not claim completion until a
fresh session loads the installed policy, discovers all required tools, and
get_stats succeeds.
```

The released guide is bound to one stable version and uses only its matching
Compose file, integration binary, and checksums. It writes a privacy-safe
checkpoint, stops after client-native MCP registration, resumes for real tool
discovery and bundle installation, then requires a final fresh session to load
and verify the policy. The automated local flow is Memory-only. If you need
Documents, stop before setup and use a
RAG-enabled service plus the [manual client guide](https://dzarlax-ai.github.io/personal_memory/getting-started/connect-clients/).

For a reproducible older release, replace `releases/latest/download` in the
prompt with `releases/download/vX.Y.Z`. Read the complete
[agent-assisted setup explanation](https://dzarlax-ai.github.io/personal_memory/getting-started/installation/),
or use the manual path below.

## Manual fallback: local service

The local release needs only Docker Engine or Docker Desktop with Docker Compose v2. It uses published images, stores data in named volumes, and exposes only `http://127.0.0.1:8000/memory`. It needs no repository clone, build, Traefik, domain, `.env`, or API key.

Choose the asset for your CPU (`amd64` for Intel/AMD, `arm64` for Apple Silicon or ARM64 Linux):

```bash
mkdir personal-memory
cd personal-memory
curl -fsSLO https://github.com/Dzarlax-AI/personal_memory/releases/latest/download/compose.local-arm64.yaml
curl -fsSLO https://github.com/Dzarlax-AI/personal_memory/releases/latest/download/SHA256SUMS
if command -v shasum >/dev/null 2>&1; then
  grep 'compose.local-arm64.yaml' SHA256SUMS | shasum -a 256 -c -
else
  grep 'compose.local-arm64.yaml' SHA256SUMS | sha256sum -c -
fi
mv compose.local-arm64.yaml compose.yaml
docker compose pull
docker compose up -d memory-qdrant memory-embeddings
```

For Intel/AMD, replace both occurrences of `arm64` with `amd64`. The first embedding-model download can be slow. Observe it without restarting it:

```bash
docker compose ps --format json memory-embeddings
docker compose logs --tail 50 memory-embeddings
```

When `memory-embeddings` is healthy, start the application with a short bounded wait:

```bash
docker compose up -d --wait --wait-timeout 120 memory-mcp
curl -fsS http://127.0.0.1:8000/health
# ok
```

Do not change the published port to a non-loopback address: this intentionally unauthenticated mode is safe only on `127.0.0.1`. See the full [local installation instructions](https://dzarlax-ai.github.io/personal_memory/getting-started/installation/) for Windows download commands, resume behavior, stop, diagnostics, and rollback.

## Production baseline

The checked-in Compose stack is production-oriented: it expects Docker, an external `traefik` network, HTTPS-capable Traefik, DNS for `mcp.<domain>`, and writable Qdrant data/snapshot directories.

```bash
git clone https://github.com/Dzarlax-AI/personal_memory.git
cd personal_memory
sudo mkdir -p /root/memory/qdrant_storage /root/memory/qdrant_snapshots
docker network inspect traefik >/dev/null
cp .env.example .env
openssl rand -hex 32
```

Set `MEMORY_DOMAIN`, put the generated secret in `API_KEY`, and keep `ALLOW_INSECURE_AUTH=false`. The checked-in Compose file uses `:latest`, so select a reviewed immutable image before first start:

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

Connect clients to `https://mcp.example.com/memory` with `X-API-Key` or a Bearer API key. Todoist uses a distinct `/todoist` endpoint and needs explicit enablement.

See [Upgrade and rollback](https://dzarlax-ai.github.io/personal_memory/operations/upgrade-rollback/) for local and production rollback procedures.

## Development

```bash
make dev-deps   # fetches embedded browser assets
make test       # assets, vet, tests, and binaries
make docs-install
make docs-check
make docs-build
```

Documentation targets are separate from `make test`; ordinary Go checks do not download Node dependencies.

## Documentation

The public documentation is available at [dzarlax-ai.github.io/personal_memory](https://dzarlax-ai.github.io/personal_memory/). Contributors can edit its canonical source in [`website/src/content/docs`](website/src/content/docs).

- [Installation](https://dzarlax-ai.github.io/personal_memory/getting-started/installation/)
- [Connect clients](https://dzarlax-ai.github.io/personal_memory/getting-started/connect-clients/)
- [Upgrade and rollback](https://dzarlax-ai.github.io/personal_memory/operations/upgrade-rollback/)
- [Backups and releases](https://dzarlax-ai.github.io/personal_memory/operations/backups-release/)
- [Troubleshooting](https://dzarlax-ai.github.io/personal_memory/operations/troubleshooting/)
- [Fact lifecycle](https://dzarlax-ai.github.io/personal_memory/lifecycle/fact-lifecycle-contract/)
- [Lifecycle migration](https://dzarlax-ai.github.io/personal_memory/lifecycle/migration/)
- [Maintenance](https://dzarlax-ai.github.io/personal_memory/maintenance/)
- [MCP tools](https://dzarlax-ai.github.io/personal_memory/reference/tools/)
- [Configuration](https://dzarlax-ai.github.io/personal_memory/reference/configuration/)
- [Compatibility](https://dzarlax-ai.github.io/personal_memory/reference/compatibility/)
- [Architecture and security](https://dzarlax-ai.github.io/personal_memory/architecture-security/)
- [Limitations](https://dzarlax-ai.github.io/personal_memory/limitations/)
- [Client integration bundle](https://dzarlax-ai.github.io/personal_memory/integration-bundle/guide/)
- [Retrieval evaluation](https://dzarlax-ai.github.io/personal_memory/operations/evaluation/)
- [Conformance suite](https://dzarlax-ai.github.io/personal_memory/operations/conformance/)
