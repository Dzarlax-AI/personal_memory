# Personal Memory

Personal Memory is a self-hosted semantic memory layer for MCP-compatible AI clients. It stores user-controlled facts, retrieves relevant context across sessions, and optionally provides document search, Todoist tools, and a visualization dashboard.

## Capabilities

- Semantic fact storage, recall, lifecycle management, export, and inspection.
- Optional Markdown/text RAG with incremental indexing.
- Optional Todoist MCP endpoint; the token remains server-side.
- Optional authenticated visualization dashboard.
- Local or explicitly selected cloud embedding backends, with separate fact and document bindings.
- Qdrant snapshots and embedding-identity verification.

## Architecture

```text
MCP client -> Traefik -> Personal Memory (Go) -> Qdrant
                                      ├-> Embedding backend
                                      │   ├-> Local: TEI or llama.cpp
                                      │   └-> Cloud: OpenAI or Google Gemini
                                      ├-> Todoist (optional)
                                      └-> RAG/Viz (optional)
```

`/memory` is the native Streamable HTTP MCP endpoint. It keeps API-key and optional generic OAuth authentication independent. An optional Cloudflare Access profile serves MCP clients at `/memory/oauth`, with a separate stateless transport. Viz can also require a browser-specific Access assertion alongside its proxy secret. The [Access migration guide](https://dzarlax-ai.github.io/personal_memory/operations/cloudflare-access/) explains how to keep the existing Authentik issuer available until every real client, including Gemini if used, has reconnected and been accepted. `/todoist` exists only when its feature is enabled. Qdrant and local model servers stay internal. Cloud embedding requests go to the selected provider only when its profile is active. Before serving requests, the application verifies the selected embedding identity against every active collection.

Access MCP and Viz each require their own configured audience to be present in
the signed assertion. Additional signed audiences are allowed; a token containing
only the MCP audience cannot authorize Viz. Viz also requires its proxy secret.

## Local and cloud embeddings

The server and standalone indexer support these explicit backend profiles:

| Location | Provider | Supported models | Input profile |
| --- | --- | --- | --- |
| Local | TEI | `intfloat/multilingual-e5-small` | `legacy-raw-v1` or `multilingual-e5-v1` |
| Local | llama.cpp | `embeddinggemma-2` | `embeddinggemma-search-v1` |
| Cloud | OpenAI | `text-embedding-3-small`, `text-embedding-3-large` | `openai-text-v1` |
| Cloud | Google | `gemini-embedding-2`, `gemini-embedding-001` | `gemini-embedding-2-search-v1`, `gemini-embedding-001-task-v1`, respectively |

Without `EMBED_PROFILES_FILE`, the existing local TEI setup uses the legacy
`EMBED_*` variables and collection names. The manual local-release setup below
continues to use that path. Published release assets are version-bound; choose an
application revision that includes backend profiles before using the advanced
configuration.

For backend selection, start with the checked-in
[profile examples and migration guide](docs/examples/embedding-backends/README.md)
and [example JSON](docs/examples/embedding-backends/profiles.json). Mount the JSON
read-only and set `EMBED_PROFILES_FILE` to its **container path** in both the
server and standalone indexer. `bindings.memory` selects a profile and fact
collection; `bindings.rag` selects a profile, chunk/folder collections, and an
explicit `flat` or `hierarchical` search mode. Facts and documents can use
different profiles. The example deliberately keeps E5 active.

Only active cloud profiles read the secret named by `api_key_env`; keep its value
outside the JSON and source control. Inactive profiles do not probe providers or
read keys. Cloud use sends the selected fact, query, or document input to that
provider and needs explicit permission for that data. There is no automatic
cross-provider fallback.

For a new vector space, prepare new matching collections before switching
bindings. Nonlegacy memory bindings require calibrated `dedup`, `related`,
`mutation`, and `viz` thresholds plus a recorded `basis`; nonlegacy hierarchical
RAG also requires `folder_threshold`. Existing vectors cannot be relabeled as a
new provider, model, dimension, normalization, or input profile. Use the
[manifest-bound migration and rollback procedure](docs/examples/embedding-backends/README.md#migration-on-the-data-host)
for an existing installation. Preserve the old collections; after new writes,
rollback must transfer the current data rather than select stale collections.

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
