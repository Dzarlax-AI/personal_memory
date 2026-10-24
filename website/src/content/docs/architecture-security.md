---
title: Architecture and security
---

Personal Memory is a Go service behind Traefik. The `/memory` MCP endpoint reaches Qdrant for fact vectors and the explicitly selected embedding backend for vectors; optional RAG uses separate document collections and may select its own profile. Local backends are TEI or llama.cpp; cloud backends are OpenAI or Google Gemini. Optional Todoist calls the Todoist API. Qdrant and local model servers remain internal Docker dependencies. Cloud selection sends the selected input to the provider, requires permission for that data, and has no automatic provider fallback.

The service fails closed when required auth is absent. Todoist is not registered without its feature flag and token. Viz combines Traefik ForwardAuth with an application-verified proxy secret; identity headers are not trusted alone. Deployment secrets belong in `.env`, never in source or client tool arguments.

Embedding identity validation prevents silent mixing of incompatible vectors. Lifecycle changes and maintenance mutations are explicit, validated, and protected by stopped-writer confirmations where required.
