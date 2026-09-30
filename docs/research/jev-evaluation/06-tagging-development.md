# 6. Real-fact tagging: development cohort

Date: 30 September 2026. Status: completed development diagnostics.

Thirty-two previously approved facts were classified without changing their stored metadata. Each request contained one project-choice head and five facet heads: architecture, configuration, bug, decision and preference. Project choices used the four technical-project tags plus `other_project` and `insufficient_context`; facet choices were applies, not-applies or insufficient-context.

Independent model labels were frozen before provider calls. Existing stored tags were inspected separately and were not treated as gold. All 32 requests succeeded; all **192 heads** were valid.

| Head | Agreement |
| --- | --- |
| Project | 30/32 |
| Architecture | 11/32 |
| Configuration | 20/32 |
| Bug | 28/32 |
| Decision | 23/32 |
| Preference | 26/32 |

Exact agreement across all five facets was **7/32**; across all six heads it was **6/32**. Architecture detected only one of 22 positive references, missed 20 and abstained once. Binary micro-F1 was 0.708661 on 145 covered facet decisions out of 160, excluding 15 abstentions; TP 45, FP 0, FN 37. This conditional score must not be read as full coverage quality.

## Post-hoc deterministic comparisons

D0 selected explicit project aliases and agreed on 26/32. D1 added a small set of infrastructure and application rules and agreed on 32/32. D0 abstained seven times; D1 once. These rules were designed after inspecting development texts, labels and results. Their development scores are not independent validation. The exact rule implementation was then frozen for the [control cohort](07-tagging-control.md).

Only seven of 32 facts had an explicit stored primary tag. A missing primary field is not a proven classification error. Formatting inconsistencies were observed in stored tags; current server normalization trims and deduplicates exact strings rather than interpreting semantic equivalence. No metadata cleanup was performed.

Usage: 49,930 input tokens and 9,920 output tokens across all 32 calls.

**Conclusion:** project classification looked promising on development examples; facet classification was uneven. Validate against frozen rules before proposing automatic tagging.
