---
title: Troubleshooting
---

**Authentication:** For production or a remote service, confirm `API_KEY` is set unless OAuth protects `/memory`; Todoist needs both `API_KEY` and `TODOIST_TOKEN` when enabled. Keep `ALLOW_INSECURE_AUTH=false` in staging and production. The standalone local release intentionally uses `ALLOW_INSECURE_AUTH=true` with no key and is safe only because the published port is exactly loopback-only `127.0.0.1:8000`.

**Embedding identity:** A startup mismatch between configured TEI identity and collection metadata is intentional. Do not override it; restore the compatible image/configuration or follow the explicit adoption path for verified legacy collections.

**Routes:** `/health` is public. Production or remote `/memory` requires API-key or configured OAuth auth; the standalone local loopback endpoint does not. `/todoist` is absent until enabled. Production `/viz` requires `VIZ_PROXY_SECRET`, and Traefik must overwrite the trusted proxy-secret header only after successful ForwardAuth.

**Indexing:** RAG requires `ENABLE_RAG=true` and a readable `RAG_DOCUMENTS_DIR`. Confirm the bind mount and use `cmd/indexer` or `reindex_documents`; incomplete walks and dangerous stale cleanup are deliberately refused.

**Logs:** Start with `docker compose ps` and `docker compose logs memory-mcp`; then test the exact external URL and an authenticated client request rather than relying only on container health.

## Local installation

**Embedding model still downloading:** Run `docker compose ps --format json memory-embeddings` and `docker compose logs --tail 50 memory-embeddings`. A running but not-yet-healthy container is waiting, not failed. Do not repeat `up` in a retry loop. After the 30-minute observation deadline, stop waiting and run the same read-only commands later; the named model cache is preserved.

**Embedding container exited:** Treat a non-zero exit as a failure rather than a slow download. Read the last 50 log lines and check that you downloaded the Compose asset for the host architecture. Do not delete volumes or bypass embedding identity.

**Port 8000 is occupied:** Find and stop the local process that already owns `127.0.0.1:8000`, or decide explicitly which service should use another loopback port. Do not change the bind to `0.0.0.0`.

**Application wait expired:** Inspect `docker compose ps` and `docker compose logs --tail 50 memory-mcp`. A failed `--wait --wait-timeout 120` is incomplete installation, even if dependency containers are running.

**Docker resources:** If a container is killed or Docker reports a resource error, increase Docker Desktop/Engine resources and resume with the existing named volumes. This project does not publish an unmeasured minimum RAM claim.

## Agent setup checkpoints

The agent-assisted path stores
`$HOME/personal-memory/agent-setup-state.json` with owner-only permissions. A
resume must validate the bound release, platform, `$HOME` path token, client,
Memory-only preset, endpoint, artifact names and hashes before doing anything.
A mismatch, unrelated directory, symlink, malformed JSON, or weakened file mode
is a stop condition; do not overwrite it as a repair.

| State | Safe continuation |
|---|---|
| `assets_verified` | Recheck hashes and reconcile exact project containers; start only if both dependencies are proven absent. |
| `dependencies_started` | Advance to observation; do not repeat the start call. |
| `waiting_for_embeddings` | Inspect structured status and the last 50 embedding log lines. Preserve the cache at the 30-minute deadline. |
| `embeddings_ready` | Run the bounded `memory-mcp` start and exact health check. |
| `service_ready` | Recheck conflicts, then register once through the client CLI. |
| `awaiting_reconnect` | Open a fresh client session and inspect that session's complete tool catalog. |
| `embeddings_failed` | Show bounded logs and request a fresh decision; do not restart automatically. |
| `complete` | Use read-only verification; do not reinstall. |

Safe service diagnostics are:

```bash
docker compose ps
docker compose ps --format json memory-embeddings
docker compose logs --tail 50 memory-embeddings
docker compose logs --tail 50 memory-mcp
curl -fsS http://127.0.0.1:8000/health
```

Use `codex mcp list --json` / `codex mcp get personal-memory --json` or
`claude mcp list` / `claude mcp get personal-memory` for registration
diagnostics. These prove configuration, not discovery in the active client
session. A direct server `tools/list` is also only a service diagnostic.

Resume with the exact request printed by the released playbook. Never recover
by repeating an ambiguous registration, editing client files directly, running
`docker compose down -v`, removing volumes, changing the loopback bind, or
switching to a moving release asset.
