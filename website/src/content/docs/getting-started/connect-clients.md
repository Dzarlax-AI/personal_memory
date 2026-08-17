---
title: Connect clients
description: Connect an MCP client to the Streamable HTTP endpoints.
---

Memory is available at `https://mcp.example.com/memory`; document-search tools share that endpoint when RAG is enabled. Todoist is a separate, API-key-only endpoint at `/todoist` and is absent until enabled.

Send either `X-API-Key: <API_KEY>` or `Authorization: Bearer <API_KEY>` to `/memory`. OAuth tokens are accepted there only when OAuth is enabled.

## Install the client policy without cloning the repository

First register the `/memory` MCP endpoint in Codex or Claude Code using that client's supported MCP configuration. Restart or reload the client, open a fresh session, and confirm that the expected Memory tools are actually visible. If you enabled document search, also confirm that `search_documents` is visible.

Download the standalone binary for your operating system and CPU from the [latest GitHub release](https://github.com/Dzarlax-AI/personal-memory/releases/latest). The four asset names are:

- `memory-integration-darwin-amd64`
- `memory-integration-darwin-arm64`
- `memory-integration-linux-amd64`
- `memory-integration-linux-arm64`

Download the matching binary and `SHA256SUMS`, then verify it before running it. For example, on Apple silicon:

```bash
curl -fLO https://github.com/Dzarlax-AI/personal-memory/releases/latest/download/memory-integration-darwin-arm64
curl -fLO https://github.com/Dzarlax-AI/personal-memory/releases/latest/download/SHA256SUMS
grep ' memory-integration-darwin-arm64$' SHA256SUMS | shasum -a 256 -c -
chmod +x ./memory-integration-darwin-arm64
```

Codex and Claude Code must already be installed and run at least once: the quick command refuses a missing `~/.codex` or `~/.claude` configuration root instead of creating one.

Install and verify the policy for Memory only:

```bash
./memory-integration-darwin-arm64 quick-install codex --confirm-tools-discovered
./memory-integration-darwin-arm64 quick-verify codex --confirm-tools-discovered
```

Use `claude` instead of `codex` for Claude Code. If `search_documents` was also visible, install that behavior explicitly:

```bash
./memory-integration-darwin-arm64 quick-install codex --with-documents --confirm-tools-discovered
```

`--confirm-tools-discovered` is your confirmation of what you observed in the client; the binary does not probe the MCP server or infer discovery. After installation, start a fresh client session so it loads the installed policy.

The quick path also provides `quick-update`, `quick-verify`, and `quick-rollback`. Update and verify preserve the installed Documents setting unless you explicitly pass `--with-documents` or `--memory-only`. Add `--json` for machine-readable, content-free output.

The bundle also supports ChatGPT (manual UI/admin step) and generic MCP hosts through its advanced interface. See the [integration bundle guide](../../integration-bundle/guide/).
