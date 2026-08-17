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

## Quick start

The checked-in Compose stack is a production-oriented baseline: it expects Docker, an external `traefik` network, HTTPS-capable Traefik, DNS for `mcp.<domain>`, and writable Qdrant data/snapshot directories.

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

See [Upgrade and rollback](website/src/content/docs/operations/upgrade-rollback.md) for client-path verification and reverting to a previous immutable image.

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

The canonical public source is in [`website/src/content/docs`](website/src/content/docs). The site will publish at `https://dzarlax-ai.github.io/personal_memory/` after GitHub Pages is enabled.

- [Installation](website/src/content/docs/getting-started/installation.md)
- [Connect clients](website/src/content/docs/getting-started/connect-clients.md)
- [Upgrade and rollback](website/src/content/docs/operations/upgrade-rollback.md)
- [Backups and releases](website/src/content/docs/operations/backups-release.md)
- [Troubleshooting](website/src/content/docs/operations/troubleshooting.md)
- [Fact lifecycle](website/src/content/docs/lifecycle/fact-lifecycle-contract.md)
- [Lifecycle migration](website/src/content/docs/lifecycle/migration.md)
- [Maintenance](website/src/content/docs/maintenance/index.md)
- [MCP tools](website/src/content/docs/reference/tools.md)
- [Configuration](website/src/content/docs/reference/configuration.md)
- [Compatibility](website/src/content/docs/reference/compatibility.md)
- [Architecture and security](website/src/content/docs/architecture-security.md)
- [Limitations](website/src/content/docs/limitations.md)
- [Client integration bundle](website/src/content/docs/integration-bundle/guide.md)
- [Retrieval evaluation](website/src/content/docs/operations/evaluation.md)
- [Conformance suite](website/src/content/docs/operations/conformance.md)
