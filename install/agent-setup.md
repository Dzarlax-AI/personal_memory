# Personal Memory agent setup

Bound release: `@RELEASE_VERSION@`

This guide is an instruction contract for Codex and Claude Code. It installs a
new local, Memory-only Personal Memory service on macOS or Linux, registers its
loopback MCP endpoint through the client's supported CLI, and deliberately
stops for a reconnect before installing the client policy bundle and again
after installation so the client can load that policy.

Do not use this guide from a branch or mix it with assets from another release.
Do not claim completion merely because the server is healthy or an MCP entry is
configured. Completion requires a fresh client session with the installed
policy loaded to rediscover every required Memory tool and successfully call
`get_stats`.

## Fixed safety boundaries

- The endpoint is exactly `http://127.0.0.1:8000/memory`. Never change it to a
  LAN or public bind: this local stack intentionally has no API key.
- The default and only automatic installation path is
  `$HOME/personal-memory`. An unrelated or unverifiable existing directory is a
  stop condition.
- This flow selects `memory-only`. The simple local Compose asset has RAG
  disabled. If the user wants Documents, stop before mutation and use the
  advanced RAG-enabled service and manual client guide instead.
- Never run `docker compose down -v`, remove named volumes, delete or replace an
  existing MCP entry, edit client configuration files directly, or retry an
  ambiguous mutation blindly.
- Never put credentials, prompts, responses, tool payloads, facts, document
  content, user identifiers, or hidden reasoning in the checkpoint or logs.

## Checkpoint contract

Store the checkpoint at
`$HOME/personal-memory/agent-setup-state.json` with mode `0600`. Write updates
atomically through a mode-`0600` temporary file in the same directory and a
rename. Preserve all identity fields across state transitions.

The allowed states are:

1. `assets_verified`
2. `dependencies_started`
3. `waiting_for_embeddings`
4. `embeddings_ready`
5. `service_ready`
6. `registration_pending`
7. `awaiting_reconnect`
8. `bundle_pending`
9. `awaiting_policy_reconnect`
10. `embeddings_failed`
11. `complete`

The schema is:

```json
{
  "schema_version": 1,
  "state": "awaiting_reconnect",
  "release": "@RELEASE_VERSION@",
  "platform": "darwin-arm64",
  "install_path": "$HOME/personal-memory",
  "client": "codex",
  "capabilities": "memory-only",
  "endpoint": "http://127.0.0.1:8000/memory",
  "artifacts": {
    "compose": {
      "release_name": "compose.local-arm64.yaml",
      "local_name": "compose.yaml",
      "sha256": "<64 lowercase hex characters>"
    },
    "integration": {
      "name": "memory-integration-darwin-arm64",
      "sha256": "<64 lowercase hex characters>"
    },
    "playbook": {
      "name": "agent-setup.md",
      "sha256": "<64 lowercase hex characters>"
    }
  }
}
```

`platform` must be one of `darwin-amd64`, `darwin-arm64`, `linux-amd64`, or
`linux-arm64`. `client` must be `codex` or `claude`. The release, platform,
path token, client, preset, endpoint, artifact names, and all three hashes must
match before resuming. A missing, malformed, mismatched, symlinked, or
permission-weakened checkpoint is not compatible state: stop without rewriting
it or the installation directory.

## Session 1: preflight, service, and registration

### 1. Read-only preflight

Do not download, create, or modify anything yet.

1. Identify the active client as Codex or Claude Code. Do not guess a third
   client family. If both CLIs exist and the active family is unclear, ask.
2. Run `uname -s` and `uname -m`. Accept only Darwin or Linux and map
   `x86_64` to `amd64`, and `arm64` or `aarch64` to `arm64`.
3. Verify `docker compose version` reports Compose v2.
4. Verify the client CLI and required command shape:
   - Codex: version `@CODEX_CLI_VERSION@` or newer, and
     `codex mcp add --help` must include `--url`.
   - Claude Code: version `@CLAUDE_CODE_VERSION@` or newer, and
     `claude mcp add --help` must include both `--transport` and `--scope`.
   Stop on an older or incompatible command surface.
5. For a fresh install, prove TCP port 8000 is unused. On Darwin use
   `lsof -nP -iTCP:8000 -sTCP:LISTEN`; on Linux use
   `ss -ltn 'sport = :8000'` (or `lsof` if `ss` is unavailable). If no
   supported inspection command exists, stop rather than assuming the port is
   free. On resume, an occupied port is acceptable only after the checkpoint,
   Compose file, expected containers, and exact health endpoint all validate.
6. Inspect `$HOME/personal-memory` without changing it. A missing directory is
   a fresh install. An existing directory is resumable only when its checkpoint
   passes the complete contract above and each artifact still matches its
   recorded hash. Otherwise stop and ask the user to resolve the directory;
   never rename, merge, delete, or overwrite it automatically.
7. Inspect configured MCP servers before registration:
   - Codex: `codex mcp list --json` and
     `codex mcp get personal-memory --json`.
   - Claude Code: `claude mcp list` and
     `claude mcp get personal-memory`.
   A `personal-memory` entry or the same endpoint under another name is a
   conflict on a fresh install. Stop; do not remove or replace it.
8. Resolve and validate the playbook's bound `@RELEASE_VERSION@` release
   directly. Require it to be a published stable release. From this point every
   URL must use
   `https://github.com/Dzarlax-AI/personal_memory/releases/download/@RELEASE_VERSION@/`.
   Do not use `latest/download` for any artifact.

The matching assets are:

- `compose.local-<architecture>.yaml`
- `memory-integration-<os>-<architecture>`
- `agent-setup.md`
- `SHA256SUMS`

### 2. Ask for one bounded approval

Before the first write, present one concise approval request containing:

- release `@RELEASE_VERSION@` and detected platform;
- `$HOME/personal-memory` and the four files to download;
- the three Docker services (`memory-qdrant`, `memory-embeddings`, and
  `memory-mcp`) and their named volumes;
- the loopback health and MCP endpoints;
- the `personal-memory` MCP entry and Codex user configuration or Claude Code
  user scope;
- the fact that Session 1 must stop at `awaiting_reconnect` and the policy
  installation later requires one more reconnect.

No approval means no mutation. Approval covers only this stated scope.

### 3. Download and verify the bound assets

After approval, create `$HOME/personal-memory`, enter it, and download all four
matching assets from the exact release URL. Keep the Compose asset under its
release name until checksum verification finishes. Never pipe a download into
a shell.

Before making the integration binary executable or starting Docker:

1. Select exactly the three relevant lines from `SHA256SUMS`.
2. On Darwin verify them with `shasum -a 256 -c`; on Linux use
   `sha256sum -c`.
3. Stop on a missing, duplicate, malformed, or mismatched checksum entry.
4. Rename the verified Compose asset to `compose.yaml`, then recalculate its
   hash and require it to equal the verified release hash.
5. Record both Compose names and the verified hashes in a new mode-`0600`
   checkpoint with state `assets_verified`.
6. Make only the integration binary executable.

### 4. Start dependencies once and observe them

From `assets_verified`, first run read-only `docker compose ps --all` and inspect
the exact project containers. If neither dependency container exists, run these
mutations once:

```bash
docker compose pull
docker compose up -d memory-qdrant memory-embeddings
```

If both dependency containers already exist, advance without another `up` only
after proving they belong to this Compose project and use its exact pinned image
identities. A partial, mismatched, or unverifiable container set is a stop
condition. This reconciliation handles a successful start followed by a failed
checkpoint write without repeating an ambiguous mutation.

After a successful start or reconciliation, write `dependencies_started`, then
`waiting_for_embeddings`. From either state, do not repeat `up` as a retry or
pull replacement images. Observe the existing service with short read-only calls:

```bash
docker compose ps --format json memory-embeddings
docker compose logs --tail 50 memory-embeddings
```

Report progress at least once per minute. A running but unhealthy container is
still `waiting_for_embeddings`. At 10 minutes show status and bounded recent
logs, but continue observing. At 30 minutes stop observation, preserve the
container, volumes, cache, and checkpoint, and give this continuation request:

> Continue Personal Memory setup from
> `$HOME/personal-memory/agent-setup-state.json`. Validate the checkpoint and
> observe the existing embedding container; do not repeat its start mutation.

A non-zero embedding-container exit becomes `embeddings_failed`. Show only the
bounded recent logs and stop. Restart requires a fresh user decision; never
delete its cache as recovery.

### 5. Start and verify the application

Once embeddings are healthy, write `embeddings_ready`, then run:

```bash
docker compose up -d --wait --wait-timeout 120 memory-mcp
curl -fsS http://127.0.0.1:8000/health
docker compose port memory-mcp 8000
```

Only the exact health response `ok` and exactly one live port result equal to
`127.0.0.1:8000` advance the checkpoint to `service_ready`. Reject wildcard,
LAN, public, additional, or missing bindings. If the bounded wait, health check,
or binding proof fails, retain `embeddings_ready`, show `docker compose ps --all`
and the last 50 `memory-mcp` log lines, and stop.

### 6. Register the client through its CLI

First repeat the conflict check. Write `registration_pending` before executing
exactly one matching command:

```bash
# Codex user configuration
codex mcp add personal-memory --url http://127.0.0.1:8000/memory

# Claude Code user scope
claude mcp add --transport http personal-memory --scope user http://127.0.0.1:8000/memory
```

Verify with `codex mcp get personal-memory --json` or
`claude mcp get personal-memory`. A trust refusal, managed-policy restriction,
existing entry, unsupported CLI, ambiguous result, or mismatched endpoint is a
stop condition. Do not edit TOML, JSON, `.mcp.json`, or project files directly.

After a confirmed matching entry, write `awaiting_reconnect` and stop Session 1.
Tell the user to close this session, start a fresh session in the same client,
and submit exactly:

> Continue Personal Memory setup from
> `$HOME/personal-memory/agent-setup-state.json`. Validate the checkpoint,
> service, MCP registration, and tools visible to this new client session. Do
> not repeat Docker startup or MCP registration.

The Session 1 agent must not advance beyond `awaiting_reconnect`, even if it can
probe the server directly.

When resuming `registration_pending`, do not repeat `mcp add`. Inspect the
client-native entry first. Reconcile only an exact `personal-memory` endpoint in
the expected Codex user configuration or Claude Code user scope, then write
`awaiting_reconnect`. A missing, mismatched, duplicate, or unverifiable entry is
an ambiguous registration outcome: stop and request a fresh user decision.

## Session 2: discovery and policy bundle installation

### 1. Validate before continuing

Read the checkpoint, require state `awaiting_reconnect`, and revalidate its
schema, release, platform, `$HOME` path token, client, preset, endpoint,
artifact names, hashes, mode, and non-symlink status. Recheck:

- `docker compose ps --all`, exact `/health` response, and exactly one live
  `docker compose port memory-mcp 8000` result equal to `127.0.0.1:8000`;
- the client-native `personal-memory` entry and exact endpoint;
- that this is a new client session.

Any mismatch remains incomplete. Do not repair it by rewriting state.

### 2. Prove discovery in this client session

Inspect the tools actually available to this Codex or Claude Code session. The
complete Memory-only inventory is:

- `store_fact`
- `update_fact`
- `set_fact_lifecycle`
- `delete_fact`
- `forget_old`
- `import_facts`
- `recall_facts`
- `list_facts`
- `find_related`
- `get_stats`
- `list_tags`
- `export_facts`

All 12 must be present. A configured MCP entry, direct HTTP `tools/list`, server
log, or assumption based on documentation is not client-session discovery. If
any tool is absent, keep `awaiting_reconnect`, report the missing names, give
the client's supported MCP inspection steps, and stop without reinstalling or
reregistering anything.

### 3. Install and verify the policy bundle

Only after real discovery, write `bundle_pending`, then run the matching
commands from the installation directory, using the actual downloaded binary
name and existing real client root:

```bash
./memory-integration-<os>-<architecture> quick-install codex \
  --target-root "$HOME/.codex" --memory-only --confirm-tools-discovered
./memory-integration-<os>-<architecture> quick-verify codex \
  --target-root "$HOME/.codex" --memory-only --confirm-tools-discovered
```

For Claude Code substitute `claude` and `$HOME/.claude`. The root must already
exist and must not be a symlink. After successful `quick-install` and
`quick-verify`, write `awaiting_policy_reconnect` and stop Session 2. Tell the
user to close this session, start a fresh session in the same client, and submit:

> Continue Personal Memory setup from
> `$HOME/personal-memory/agent-setup-state.json`. Validate the checkpoint,
> service, MCP registration, policy bundle, and tools visible to this new client
> session. Do not repeat Docker startup, MCP registration, or bundle installation.

When resuming `bundle_pending`, run read-only `quick-verify` before any mutation.
If it succeeds, do not repeat `quick-install`; write `awaiting_policy_reconnect`
and stop for the required reconnect. If it fails or is ambiguous, keep
`bundle_pending`, report the safe verification result, and request a fresh user
decision. Never retry `quick-install` automatically.

## Session 3: loaded-policy verification and read check

Read the checkpoint, require `awaiting_policy_reconnect`, and repeat the complete
identity, artifact, service, live loopback binding, registration, new-session,
and 12-tool discovery checks above. Run the matching read-only `quick-verify`
command again; do not run `quick-install`.

Finally, call the discovered `get_stats` tool through this fresh client session.
Do not create a sample fact. Only the previously confirmed install, successful
fresh-session quick verify, rediscovery, and client-visible `get_stats` permit
an atomic transition to `complete`.

## Safe diagnostics and resume map

| Checkpoint | Safe next action |
|---|---|
| `assets_verified` | Validate hashes and reconcile existing project containers; start only when both dependencies are proven absent. |
| `dependencies_started` | Write `waiting_for_embeddings`; observe the existing container. |
| `waiting_for_embeddings` | Observe `ps` and bounded logs; never repeat `up`. |
| `embeddings_ready` | Run the bounded `memory-mcp` start and exact health check. |
| `service_ready` | Recheck MCP conflicts, then perform one client-native registration. |
| `registration_pending` | Inspect the client-native entry; reconcile only the exact endpoint and expected scope, never repeat registration automatically. |
| `awaiting_reconnect` | Start a fresh client session and prove its tool inventory. |
| `bundle_pending` | Run read-only `quick-verify`; never repeat `quick-install` automatically. |
| `awaiting_policy_reconnect` | Start a fresh client session, rediscover tools, run read-only bundle verification, then call `get_stats`. |
| `embeddings_failed` | Show bounded diagnostics and ask for a fresh decision; do not restart automatically. |
| `complete` | Run read-only verification only; do not reinstall. |

General diagnostics are limited to `docker compose ps --all`, live Compose port
inspection, the last 50 relevant
log lines, the exact health endpoint, client-native MCP list/get commands, and
artifact checksum verification. Never treat destructive cleanup, volume
removal, direct client-file editing, a non-loopback bind, or a moving release
asset as recovery.

Manual fallbacks:

- Local service installation:
  https://github.com/Dzarlax-AI/personal_memory/blob/@RELEASE_VERSION@/website/src/content/docs/getting-started/installation.md
- Client registration and bundle:
  https://github.com/Dzarlax-AI/personal_memory/blob/@RELEASE_VERSION@/website/src/content/docs/getting-started/connect-clients.md
- Troubleshooting:
  https://github.com/Dzarlax-AI/personal_memory/blob/@RELEASE_VERSION@/website/src/content/docs/operations/troubleshooting.md
