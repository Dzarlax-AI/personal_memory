---
title: Compatibility
---

The server uses Streamable HTTP MCP. It accepts API-key authentication on `/memory`, with optional OAuth support there; `/todoist` is API-key-only. Qdrant 1.16 or newer is required when collection embedding identity metadata is used. Legacy numeric and text-only point IDs remain readable.

The runtime validates the configured TEI model ID, revision, dtype, pooling, and vector size against collection identity before serving requests. The server and standalone indexer accept only the legacy embedding input profile; other profiles are evaluation/materialization-only. Client product-version compatibility is not pinned here: verify the current client transport and configuration syntax with its own documentation.
