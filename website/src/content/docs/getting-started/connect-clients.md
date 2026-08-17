---
title: Connect clients
description: Connect an MCP client to the Streamable HTTP endpoints.
---

Production or remote Memory is available at `https://mcp.example.com/memory`;
document-search tools share that endpoint when RAG is enabled. Todoist is a
separate, API-key-only endpoint at `/todoist` and is absent until enabled.

For production or remote `/memory`, send either `X-API-Key: <API_KEY>` or
`Authorization: Bearer <API_KEY>`. OAuth tokens are accepted only when OAuth is
enabled. The standalone local release is different: its exact endpoint is
`http://127.0.0.1:8000/memory` and intentionally needs no credentials because
it is loopback-only.

## Recommended for a new local setup

For a brand-new local Memory-only service, use the
[agent-assisted setup](../installation/#recommended-agent-assisted-local-setup).
Its first session registers the endpoint and deliberately stops for a reconnect;
the second session proves actual client tool discovery before installing this
policy bundle.

Documents are not part of the simple local stack because that release asset has
RAG disabled. For Documents, first deploy a RAG-enabled service and verify that
`search_documents` is visible in the fresh client session, then select the
Documents bundle capability below.

## Manual fallback: install the client policy without cloning

The standalone binaries below support Darwin and Linux on AMD64 or ARM64. No
Windows integration binary is currently released.

For the simple local service, register the endpoint through the client CLI:

```bash
codex mcp add personal-memory --url http://127.0.0.1:8000/memory
codex mcp get personal-memory --json

claude mcp add --transport http personal-memory --scope user http://127.0.0.1:8000/memory
claude mcp get personal-memory
```

Run only the pair matching your client. For remote/authenticated services, use
that client's supported credential configuration instead of copying these
unauthenticated local commands. Restart or reload the client, open a fresh
session, and confirm that the expected Memory tools are actually visible. If
you enabled document search, also confirm that `search_documents` is visible.

Download the standalone binary for your operating system and CPU from the [latest GitHub release](https://github.com/Dzarlax-AI/personal_memory/releases/latest). The four asset names are:

- `memory-integration-darwin-amd64`
- `memory-integration-darwin-arm64`
- `memory-integration-linux-amd64`
- `memory-integration-linux-arm64`

Download the matching binary and `SHA256SUMS`, then verify it before running it. For example, on Apple silicon:

```bash
curl -fLO https://github.com/Dzarlax-AI/personal_memory/releases/latest/download/memory-integration-darwin-arm64
curl -fLO https://github.com/Dzarlax-AI/personal_memory/releases/latest/download/SHA256SUMS
grep ' memory-integration-darwin-arm64$' SHA256SUMS | shasum -a 256 -c -
chmod +x ./memory-integration-darwin-arm64
```

Codex and Claude Code must already be installed and run at least once. Pass the actual client configuration root explicitly; the quick command refuses a missing or symlinked root instead of creating one. The standard roots are `~/.codex` and `~/.claude`, but custom installations must use their real root.

Install and verify the policy for Memory only:

```bash
./memory-integration-darwin-arm64 quick-install codex --target-root "$HOME/.codex" --confirm-tools-discovered
./memory-integration-darwin-arm64 quick-verify codex --target-root "$HOME/.codex" --confirm-tools-discovered
```

Use `claude` instead of `codex` for Claude Code. If `search_documents` was also visible, install that behavior explicitly:

```bash
./memory-integration-darwin-arm64 quick-install codex --target-root "$HOME/.codex" --with-documents --confirm-tools-discovered
```

`--confirm-tools-discovered` is your confirmation of what you observed in the client; the binary does not probe the MCP server or infer discovery. After installation, start a fresh client session so it loads the installed policy.

In that new session, confirm the selected tools are still visible and call the
read-only `get_stats` tool. Manual setup is complete only after `quick-install`,
`quick-verify`, fresh-session discovery, and `get_stats` all succeed. Do not
create a sample fact as a smoke test.

The quick path also provides `quick-update`, `quick-verify`, and `quick-rollback`. Update and verify preserve the installed Documents setting unless you explicitly pass `--with-documents` or `--memory-only`. Add `--json` for machine-readable, content-free output.

The bundle also supports ChatGPT (manual UI/admin step) and generic MCP hosts through its advanced interface. See the [integration bundle guide](../../integration-bundle/guide/).
