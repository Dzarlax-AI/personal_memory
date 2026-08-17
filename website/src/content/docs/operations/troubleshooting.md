---
title: Troubleshooting
---

**Authentication:** Confirm `API_KEY` is set unless OAuth protects `/memory`; Todoist needs both `API_KEY` and `TODOIST_TOKEN` when enabled. Keep `ALLOW_INSECURE_AUTH=false`; set it to `true` only for isolated development, never staging or production.

**Embedding identity:** A startup mismatch between configured TEI identity and collection metadata is intentional. Do not override it; restore the compatible image/configuration or follow the explicit adoption path for verified legacy collections.

**Routes:** `/health` is public. `/memory` requires API-key or configured OAuth auth. `/todoist` is absent until enabled. Production `/viz` requires `VIZ_PROXY_SECRET`, and Traefik must overwrite the trusted proxy-secret header only after successful ForwardAuth.

**Indexing:** RAG requires `ENABLE_RAG=true` and a readable `RAG_DOCUMENTS_DIR`. Confirm the bind mount and use `cmd/indexer` or `reindex_documents`; incomplete walks and dangerous stale cleanup are deliberately refused.

**Logs:** Start with `docker compose ps` and `docker compose logs memory-mcp`; then test the exact external URL and an authenticated client request rather than relying only on container health.
