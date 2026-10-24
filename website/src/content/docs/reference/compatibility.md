---
title: Compatibility
---

The server uses Streamable HTTP MCP. It accepts API-key authentication on `/memory`, with optional OAuth support there; `/todoist` is API-key-only. Qdrant 1.16 or newer is required when collection embedding identity metadata is used. Legacy numeric and text-only point IDs remain readable.

The runtime validates every active collection against its selected embedding identity before serving requests. The legacy TEI path checks model ID, immutable revision, dtype, pooling, and vector size. Opt-in backend profiles support TEI, llama.cpp EmbeddingGemma, OpenAI, and Google Gemini with explicit input profiles and dimensions. Cloud model IDs do not attest immutable weights; llama.cpp weights provenance is declared and must be verified by the operator. Switching vector spaces requires new matching collections, not an adoption override. See [Configuration](../configuration/) for profile selection. Client product-version compatibility is not pinned here: verify the current client transport and configuration syntax with its own documentation.

Standalone local release assets support `linux/amd64` and `linux/arm64`
containers. On Apple Silicon, Docker Desktop runs the ARM64 asset. AMD64 uses a
reviewed official TEI CPU image; because upstream does not publish an ARM64 CPU
container, the ARM64 asset uses a Personal Memory release image rebuilt from the
documented pinned upstream TEI commit and pinned by its resulting digest.
