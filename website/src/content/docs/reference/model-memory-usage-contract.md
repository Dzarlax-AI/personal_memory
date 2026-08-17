---
title: Model-memory usage contract
description: Location and identity requirements for the normative client behavior contract.
---

# Model-memory usage contract

The normative contract is source-identity-bound by SHA-256 and remains at
[`docs/model-usage-contract.md`](https://github.com/Dzarlax-AI/personal_memory/blob/main/docs/model-usage-contract.md).
It cannot carry Starlight frontmatter or rewritten links without invalidating
the integration bundle and its public conformance evidence.

The contract defines mandatory recall, storage routing, lifecycle precedence,
retry behavior, capability fallbacks, result disclosure, and telemetry safety.
Use the canonical source when validating or installing a client integration
bundle.
