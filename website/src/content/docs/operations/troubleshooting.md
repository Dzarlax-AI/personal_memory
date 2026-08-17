---
title: Troubleshooting
---

**Authentication:** Confirm `API_KEY` is set unless OAuth protects `/memory`; Todoist needs both `API_KEY` and `TODOIST_TOKEN` when enabled. Keep `ALLOW_INSECURE_AUTH=false`; set it to `true` only for isolated development, never staging or production.

**Embedding identity:** A startup mismatch between configured TEI identity and collection metadata is intentional. Do not override it; restore the compatible image/configuration or follow the explicit adoption path for verified legacy collections.

**Routes:** `/health` is public. `/memory` requires API-key or configured OAuth auth. `/todoist` is absent until enabled. Production `/viz` requires `VIZ_PROXY_SECRET`, and Traefik must overwrite the trusted proxy-secret header only after successful ForwardAuth.

**Indexing:** RAG requires `ENABLE_RAG=true` and a readable `RAG_DOCUMENTS_DIR`. Confirm the bind mount and use `cmd/indexer` or `reindex_documents`; incomplete walks and dangerous stale cleanup are deliberately refused.

**Logs:** Start with `docker compose ps` and `docker compose logs memory-mcp`; then test the exact external URL and an authenticated client request rather than relying only on container health.

## Local installation

**Embedding model still downloading:** Run `docker compose ps --format json memory-embeddings` and `docker compose logs --tail 50 memory-embeddings`. A running but not-yet-healthy container is waiting, not failed. Do not repeat `up` in a retry loop. After the 30-minute observation deadline, stop waiting and run the same read-only commands later; the named model cache is preserved.

**Embedding container exited:** Treat a non-zero exit as a failure rather than a slow download. Read the last 50 log lines and check that you downloaded the Compose asset for the host architecture. Do not delete volumes or bypass embedding identity.

**Port 8000 is occupied:** Find and stop the local process that already owns `127.0.0.1:8000`, or decide explicitly which service should use another loopback port. Do not change the bind to `0.0.0.0`.

**Application wait expired:** Inspect `docker compose ps` and `docker compose logs --tail 50 memory-mcp`. A failed `--wait --wait-timeout 120` is incomplete installation, even if dependency containers are running.

**Docker resources:** If a container is killed or Docker reports a resource error, increase Docker Desktop/Engine resources and resume with the existing named volumes. This project does not publish an unmeasured minimum RAM claim.
