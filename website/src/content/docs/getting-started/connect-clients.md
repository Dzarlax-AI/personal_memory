---
title: Connect clients
description: Connect an MCP client to the Streamable HTTP endpoints.
---

Memory is available at `https://mcp.example.com/memory`; document-search tools share that endpoint when RAG is enabled. Todoist is a separate, API-key-only endpoint at `/todoist` and is absent until enabled.

Send either `X-API-Key: <API_KEY>` or `Authorization: Bearer <API_KEY>` to `/memory`. OAuth tokens are accepted there only when OAuth is enabled.

For client behavior policy, build and install the versioned bundle with explicit capability discovery:

```bash
go build -o ./memory-integration ./cmd/memory-integration
./memory-integration install --client codex --target-root "$HOME/.codex" \
  --capability memory=disabled --capability documents=disabled --capability todoist=disabled
./memory-integration verify --client codex --target-root "$HOME/.codex" \
  --capability memory=disabled --capability documents=disabled --capability todoist=disabled
```

The bundle supports Codex, Claude, ChatGPT (manual UI/admin step), and generic MCP hosts. See the [integration bundle guide](../../integration-bundle/guide/).
