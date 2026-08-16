---
title: MCP tools
---

Memory writes: `store_fact`, `update_fact`, `set_fact_lifecycle`, `delete_fact`, `forget_old`, and `import_facts`.

Memory reads: `recall_facts`, `list_facts`, `find_related`, `get_stats`, `list_tags`, and `export_facts`.

When enabled, RAG adds `search_documents` and `reindex_documents`; Todoist adds `get_projects`, `get_labels`, `get_tasks`, `create_task`, `update_task`, `complete_task`, and `delete_task`.

Default recall returns valid, non-expired current facts. Lifecycle history requires explicit inspection modes. See the [lifecycle contract](../lifecycle/fact-lifecycle-contract/).
