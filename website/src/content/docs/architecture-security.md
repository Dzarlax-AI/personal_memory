---
title: Architecture and security
---

Personal Memory is a Go service behind Traefik. The `/memory` MCP endpoint reaches TEI for embeddings and Qdrant for fact vectors; optional RAG uses separate document collections. Optional Todoist calls the Todoist API. TEI and Qdrant are internal Docker dependencies.

The service fails closed when required auth is absent. Todoist is not registered without its feature flag and token. Viz combines Traefik ForwardAuth with an application-verified proxy secret; identity headers are not trusted alone. Deployment secrets belong in `.env`, never in source or client tool arguments.

Embedding identity validation prevents silent mixing of incompatible vectors. Lifecycle changes and maintenance mutations are explicit, validated, and protected by stopped-writer confirmations where required.
