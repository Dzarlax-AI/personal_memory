# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Self-hosted semantic memory + Todoist integration. Written in Go as a single static binary.

Stack:
- **Qdrant** — vector database for storing embeddings (shared `infra-qdrant` in production)
- **text-embeddings-inference (TEI)** — local embedding model server
- **Traefik v3** — reverse proxy with Let's Encrypt SSL
- **mcp-go + Chi** — Go HTTP server and MCP implementation

## Architecture

Two Docker services in this repo: `memory-embeddings` (TEI), `memory-mcp` (Go server). Qdrant is provided by the infra stack (`infra-qdrant`) and reached on the `infra` Docker network. TEI and Qdrant are internal — not exposed outside Docker networks.

```
Client → Traefik (mcp.<domain>) → memory-mcp:8000 (Go)
           ├─ /memory   → memory MCP (X-API-Key)
           │             (tools include RAG when ENABLE_RAG=true)
           ├─ /todoist  → todoist MCP (X-API-Key, ENABLE_TODOIST)
           ├─ /viz/     → viz dashboard (Authentik + proxy secret, ENABLE_VIZ)
           └─ /health   → liveness (no auth)
```

Single Go process serves all routes on one port via Chi router. `/memory` accepts API-key and optional OAuth auth; optional `/todoist` is API-key-only. `/viz` combines Traefik Authentik ForwardAuth with an application-verified proxy secret so browsers get OIDC without trusting identity headers directly.

Todoist, viz, and RAG are toggled by `ENABLE_TODOIST` / `ENABLE_VIZ` / `ENABLE_RAG` env vars. Disabled features do not require their secrets or register their routes. Todoist requires both `TODOIST_TOKEN` and `API_KEY` only when enabled. Viz requires Traefik to overwrite `X-Personal-Memory-Proxy-Secret` with `VIZ_PROXY_SECRET` after successful ForwardAuth. Backup runs as a goroutine.

### Qdrant collections

| Collection | Purpose |
|---|---|
| `memory` | facts written via `store_fact` (default memory layer) |
| `doc_chunks` | markdown chunks from `RAG_DOCUMENTS_DIR` (when `ENABLE_RAG=true`) |
| `doc_folders` | folder summaries for hierarchical search (when `ENABLE_RAG=true`) |

Collection name is now a `qdrant.Client` field, not a constant — one client per collection.

## Project Layout

```
cmd/
  server/main.go           — entrypoint, Chi router, graceful shutdown
  indexer/main.go          — standalone RAG indexer binary (cron-friendly)
  migrate-memory-lifecycle/ — dry-run/apply/rollback lifecycle migration
internal/
  config/                  — env vars → struct
  middleware/auth.go       — X-API-Key + Bearer auth
  qdrant/client.go         — Qdrant REST client (upsert, search, scroll, delete, snapshots, field index)
  embeddings/client.go     — TEI REST client (Embed + EmbedBatch, batch size 32)
  memory/
    server.go              — 12 memory MCP tools
    cache.go               — in-memory cache with TTL + invalidation
    lifecycle/             — normalized lifecycle model, validation, authority ranking
    lifecycle_adapter.go   — memory read-path filters, formatting, and lifecycle counts
  lifecyclemigration/      — immutable manifest, safe resume, and compare-before-rollback
  rag/
    chunker.go             — markdown-aware chunking (heading → paragraph → sentence)
    summarizer.go          — folder summaries (filenames + first H1/H2/H3)
    indexer.go             — walk + incremental upsert, stale cleanup, batched embeds
    server.go              — MCP tools: search_documents, reindex_documents
  todoist/
    client.go              — Todoist REST API v1 client
    server.go              — 7 MCP tools
  viz/
    handler.go             — Chi subrouter: /{tab}, /api/facts, /api/graph, /api/duplicates, /api/documents, /assets/*
    similarity.go          — cosine similarity
    static/                — embedded dashboard: shell.html + views/*.html + assets/{styles.css, js/*.js}
  backup/loop.go           — snapshot + prune goroutine
```

## MCP Tools

### Writing
- `store_fact(fact, tags?, primary_tag?, namespace?, permanent?, valid_until?, lifecycle_state?, ...)` — embed and save a fact; optional lifecycle inputs create a validated explicit target
- `update_fact(old_query, new_fact, lifecycle_state?, ...)` — find by similarity or exact ID, replace text, and preserve or explicitly replace lifecycle metadata
- `set_fact_lifecycle(point_id, lifecycle_state, ...)` — replace validated lifecycle metadata by exact ID without re-embedding
- `delete_fact(query, namespace?)` — find by similarity and delete
- `forget_old(days?, namespace?, dry_run?)` — delete old facts; skips `permanent=true`; defaults to dry run
- `import_facts(facts)` — bulk import from JSON string

### Reading
- `recall_facts(query, tags?, namespace?, limit?)` — semantic search with scores; filters expired; async-increments `recall_count`
- `list_facts(tags?, namespace?)` — list all facts with metadata
- `find_related(query, namespace?, limit?)` — lifecycle-ranked related facts with cosine scores; valid superseded facts remain eligible above the duplicate threshold
- `get_stats()` — counts, namespace/tag breakdown, most recalled
- `list_tags(namespace?)` — all tags with counts
- `export_facts(namespace?)` — export as JSON

### Todoist
- `get_projects`, `get_labels`, `get_tasks`, `create_task`, `update_task`, `complete_task`, `delete_task`

### RAG (when `ENABLE_RAG=true`, registered on the `/memory` MCP endpoint)
- `search_documents(query, limit?, mode?)` — hierarchical search by default: top folders first, then chunks inside those folders, with flat fallback. `mode="flat"` forces a single-collection vector search.
- `reindex_documents()` — launches incremental re-indexing in a background goroutine. Skips unchanged files (SHA256 hash). Mutex-guarded — only one reindex at a time. Stale-file cleanup is aborted if the walk was incomplete or would remove >50% of the index.

## Data Model (Qdrant payload)

```
text              string    — the fact
namespace         string    — logical group (default: "default")
tags              []string  — labels
permanent         bool      — never deleted by forget_old
valid_until       string    — ISO date; expired facts excluded from search
created_at        string    — ISO datetime
updated_at        string    — ISO datetime (set on update)
recall_count      int       — times returned by recall_facts
last_recalled_at  string    — ISO datetime
user              string    — from MEMORY_USER env var
lifecycle_state   string    — current, historical, superseded, or disputed; no lifecycle fields means legacy current
canonical         bool      — explicit current-only authority hint; not globally unique
provenance        object    — origin source and optional reference; not a trust score
verified_at       string    — optional RFC3339 verification timestamp
supersedes        []string  — normalized IDs replaced by this fact
superseded_by     []string  — normalized IDs replacing this fact; required for superseded state
```

The normative lifecycle contract, including visibility and rollout behavior, is in `docs/lifecycle.md`.

### Point IDs

- **New points**: deterministic UUID-v5-like hex derived from namespace + exact text
- **Legacy points**: numeric and text-only deterministic IDs; both remain readable and can be rewritten with `cmd/migrate-memory-ids`

The Qdrant client unmarshals `id` into `interface{}` and converts to string with `parsePointID`. Don't assume IDs are always strings — Qdrant returns whatever was stored.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `API_KEY` | — | Shared secret for `X-API-Key`. Required unless OAuth secures `/memory`; also required whenever Todoist is enabled. Empty auth fails startup by default. |
| `ALLOW_INSECURE_AUTH` | `false` | Explicit isolated-development escape hatch for running without API-key/OAuth auth (and viz without a proxy secret). Never enable in production. |
| `MEMORY_USER` | `claude` | Username stored in fact metadata |
| `MEMORY_DOMAIN` | required | Domain — MCP at `mcp.<domain>` (used by Traefik labels in deploy) |
| `QDRANT_URL` | `http://memory-qdrant:6333` | Qdrant endpoint. In production: `http://infra-qdrant:6333` |
| `EMBED_URL` | `http://memory-embeddings:80` | TEI endpoint |
| `EMBED_MODEL` | `intfloat/multilingual-e5-small` | Expected TEI model ID |
| `EMBED_MODEL_REVISION` | pinned commit | Immutable 40-character model commit |
| `ADOPT_EXISTING_EMBEDDING_IDENTITY` | `false` | One-start binding for verified legacy collections without identity metadata; never overrides mismatch |
| `ENABLE_TODOIST` | `false` | Enable Todoist MCP server |
| `ENABLE_VIZ` | `false` | Enable visualization dashboard |
| `TODOIST_TOKEN` | — | Todoist API token (only when `ENABLE_TODOIST=true`) |
| `VIZ_PROXY_SECRET` | — | Required only when `ENABLE_VIZ=true`; Traefik overwrites the trusted proxy header after ForwardAuth. |
| `CACHE_TTL` | `60` | Search cache TTL in seconds |
| `DEDUP_THRESHOLD` | `0.97` | Cosine similarity for dedup |
| `RELATED_FACT_LOW` | `0.60` | Minimum cosine similarity for related-fact candidates |
| `CONTRADICTION_LOW` | — | Deprecated fallback for `RELATED_FACT_LOW` for one deprecation window; ignored when the new variable is set |
| `KEEP_SNAPSHOTS` | `7` | Snapshots to retain |
| `BACKUP_INTERVAL_HOURS` | `24` | Backup frequency in hours |
| `VIZ_SIMILARITY_THRESHOLD` | `0.65` | Cosine similarity threshold for graph edges |
| `MCP_PORT` | `8000` | HTTP port |
| `ENABLE_RAG` | `false` | Enable document RAG tools (`search_documents`, `reindex_documents`) |
| `RAG_DOCUMENTS_DIR` | `/root/documents/personal` | Root directory to index. Hidden dirs (`.git`, `.sync`) are skipped. |
| `RAG_CHUNK_MAX_BYTES` | `1500` | Max chunk size in bytes (heading → paragraph → sentence → hard split) |
| `RAG_FOLDER_TOP_K` | `3` | Top N folders to consider in hierarchical search |
| `RAG_FOLDER_THRESHOLD` | `0.50` | Min folder similarity score; below this, fall back to flat chunk search |
| `RAG_COLLECTION_CHUNKS` | `doc_chunks` | Qdrant collection for chunks |
| `RAG_COLLECTION_FOLDERS` | `doc_folders` | Qdrant collection for folder summaries |
| `RAG_REINDEX_INTERVAL_MINUTES` | `0` | How often the in-server goroutine auto-re-indexes. 0 disables (run manually or via `cmd/indexer`). |

Never hardcode credentials. Use `.env` file (excluded from git).

## Key Implementation Details

### memory/server.go
- Collection creation and model compatibility are verified by `internal/embeddingidentity` before memory workers start
- `cache.Invalidate()` is called after any write operation (store, delete, update, import, forget_old)
- Recall events, including cache hits, are queued to a bounded worker that batches atomic Qdrant increments and drains during graceful shutdown; metrics remain best-effort under hard kills
- `forget_old` defaults to `dry_run=true` — safe by default
- New point IDs are deterministic from namespace + exact text; legacy numeric/text-only IDs are handled on read and by the standalone migration
- TEI and Qdrant accessed via Docker network (no auth needed)
- Similarity scores are cosine proximity, not contradiction or entailment probability. Duplicate, related, disputed, and superseded are distinct concepts; inspect structured related candidates semantically before acting on them.
- `store_fact` returns `status`, `stored`, optional `point_id`/`duplicate`, and `related_facts`; `find_related` returns `count` and `related_facts`. Candidates include text, score, namespace, tags, and normalized lifecycle metadata, with a text fallback for older clients.
- Duplicate prevention is unchanged except that a valid superseded fact does not block a new current fact. Related-fact feedback does not auto-supersede, classify disputes, or invoke an LLM.
- `recall_facts` and operational context admit only valid, non-expired current facts; payloads with no lifecycle fields are legacy current. Canonical current facts rank first without changing vector scores.
- `find_related`, `list_facts`, `export_facts`, `get_stats`, and Viz retain history-inspection visibility as defined in `docs/lifecycle.md`; malformed explicit lifecycle metadata remains inspectable but is never current truth.
- Lifecycle writes are explicit and validated. `store_fact`/`update_fact` preserve old-client behavior when lifecycle inputs are omitted; `set_fact_lifecycle` uses exact IDs and a lifecycle-only ordered Qdrant batch without re-embedding.
- Lifecycle migration is standalone, dry-run-first, manifest-backed, and never runs at startup. Apply/rollback require stopped writers; rollback uses compare-before-restore and never overwrites post-migration lifecycle changes.

### memory/lifecycle and lifecycle_adapter.go
- `lifecycle.Parse(payload, pointID)` is the single normalization and validation boundary. Only a payload with no lifecycle fields is treated as legacy current; malformed explicit metadata returns a non-sensitive invalid view and must not panic.
- Lifecycle transitions are explicit, reversible, and idempotent when target invariants pass. `permanent` controls retention only, while expired `valid_until` excludes even current facts from current-context flows.

### qdrant/client.go
- Collection name is a struct field — `NewClient(url, collection string)` — one client per collection
- Both `Search` and `Scroll` receive point IDs as `interface{}` and normalize via `parsePointID`
- Scroll uses `interface{}` for offset to handle both string and numeric next_page_offset
- `CreateFieldIndex(field, schema)` creates payload indexes (used by RAG for fast `file_path` / `folder_path` filtering)
- `ReplaceLifecyclePayload` updates only lifecycle keys through an ordered strong batch; it never rewrites vectors or unrelated payload metadata
- Supports snapshot create/list/delete for the backup loop
- Reads and updates Qdrant 1.16+ collection metadata used by the embedding identity guard

### embeddingidentity/identity.go
- Verifies configured model ID/revision against TEI `/info`, including dtype, pooling, and probe vector size
- Preflights every active collection before any metadata write; non-empty legacy collections require one-shot explicit adoption
- Stored mismatches always fail startup and cannot be overridden by the adoption flag

### rag/indexer.go
- Single `ScrollAll` at the start of `Run` snapshots every file's hash + expected chunk count; per-file hash checks are in-memory afterwards (no N+1 round-trips)
- Files are truly "unchanged" only when hash matches AND `actualCount == totalChunks` — a half-indexed file (partial upsert from a prior run) is detected and rebuilt
- Embeds are batched via `embeddings.Client.EmbedBatch` (TEI sub-batches of 32)
- Generation-based replacement writes and verifies a complete new file generation before deleting old chunks; empty files remove their old generations
- Stale cleanup aborts if the walk had any errors OR if it would remove more than half the known files (guards against transient Resilio/FS glitches wiping the index)
- Walk skips hidden dirs (`.git`, `.sync`, `.trash`, …) except for the root

### rag/server.go
- `Server.EnsureIndexes` / package-level `rag.EnsureIndexes` create payload indexes after the shared embedding identity guard has created and verified collections
- `reindex_documents` is mutex-guarded (`sync.Mutex.TryLock`) and runs on the server-lifetime context so graceful shutdown cancels in-flight reindexing
- `search_documents` accepts only `mode=hierarchical|flat`, caps integer `limit` at 100, and returns paths relative to `RAG_DOCUMENTS_DIR`

### todoist/server.go
- Thin wrapper over Todoist REST API v1 (`https://api.todoist.com/api/v1`)
- `TODOIST_TOKEN` is read from env at startup — never passed by the client
- The route and client are created only when `ENABLE_TODOIST=true`; disabled Todoist requires neither token nor Todoist-specific route
- Stateless — no caching, no local storage

### viz/handler.go
- Chi subrouter with JSON APIs (`/api/facts`, `/api/graph`, `/api/duplicates`, `/api/documents`), asset server (`/assets/*`), and a shell handler for `/` + `/{tab}` — all tab paths return the same HTML (SPA with History API routing on the client)
- `static/` subtree is embedded via `//go:embed all:static`; `shell.html` is composed with view fragments at handler construction time, cached as `composedHTML`
- `WithDocumentRAG(chunks, docsDir)` is an opt-in hook; when nil, `/api/documents` returns 404 and the Documents tab is hidden client-side
- Graph API computes pairwise cosine similarity in-process; caps at `max_edges` strongest edges
- The app verifies `VIZ_PROXY_SECRET`; Traefik must overwrite the trusted header only after Authentik ForwardAuth succeeds

### cmd/server/main.go
- Chi router with logger + recoverer middleware
- Public: `/health` (no auth)
- `chi.Group` applies API-key/OAuth auth to `/memory`; the optional `/todoist` group is API-key-only
- `/viz` uses app-level proxy-secret middleware in addition to Traefik/Authentik; `/health` remains public
- `signal.NotifyContext` for graceful shutdown; backup goroutine respects context
- Single `StreamableHTTPServer` per MCP server (memory, todoist)

## Build & Deploy

### Local build
```bash
make test
```

### Docker (multi-stage)
```bash
docker build -t personal-memory .
```
Builder/runtime base images are digest-pinned. TEI, Qdrant, and the embedding model revision are immutable in repository compose. Browser assets are exact-version downloads checked against `build/browser-assets.sha256` before compilation.

### CI/CD
`.github/workflows/docker.yml`: on push to `main`, runs `go test` then builds and pushes to `ghcr.io/dzarlax-ai/personal-memory:{latest,sha}`.

### Deploy
Production deploy configs live in `personal_ai_stack/deploy/memory/`. Deploy skill (`deploy-personal`) handles syncing configs and pulling the latest image on the VPS.
This repository does not update that external deploy config automatically. Production needs a separate reviewed diff for `VIZ_PROXY_SECRET`, the Traefik header overwrite, immutable dependency refs, and a concrete application `sha-*` tag/digest; do not deploy moving `latest`.

## Verification

After setup:
- `curl https://mcp.<domain>/health` → `ok`
- `http://localhost:6333/dashboard` on the VPS shows the `memory` collection
- Legacy data from the Python implementation is read transparently (numeric IDs handled)
