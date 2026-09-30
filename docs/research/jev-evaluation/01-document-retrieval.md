# 1. Exploratory document retrieval

Date: 28 September 2026. Status: exploratory, no runtime integration.

A small synthetic probe reranked an existing bounded candidate pool. Top-1 improved from **3/7 to 5/7**. Two required documents were absent from the pool; reranking could not recover them.

The recorded result is encouraging only as a prompt for a controlled experiment. Heading, filename and path information may explain the gain. The later proposed matrix separated the existing retrieval baseline, deterministic metadata ranking, bounded candidate union and Jev ranking. That complete matrix is a plan, not a completed benchmark in this archive.

The proposed sequence was quality evaluation, then local runtime evaluation, then a separate production decision. A quality comparison would need to beat the strongest deterministic baseline, preserve Russian and exact-name/path slices and keep retrieval behavior stable on provider failure. Runtime latency and privacy decisions were not established by this seven-query probe.

**Conclusion:** no production retrieval change is justified. Candidate recall must be evaluated independently of reranking.
