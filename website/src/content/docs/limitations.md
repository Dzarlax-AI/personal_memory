---
title: Limitations
---

- Semantic similarity is not contradiction or entailment detection; related facts require semantic review.
- Lifecycle history inspection does not reconstruct historical intervals; `as_of` changes expiry reference only.
- Recall-counter writes are best effort under a hard process kill.
- RAG indexes only Markdown and text files and requires a separate source-document mount.
- Viz graph similarity is computed in-process and bounded; it is not a general graph database.
- The public evaluation benchmarks did not justify enabling hybrid retrieval, alternative embedding profiles, document-routing changes, or a reranker in production.
