# 5. Sixty real pairs and independent reassessment

Date: 30 September 2026. Status: completed diagnostic study, no quality winner.

The approved technical-project selection contained 65 facts and 60 pairs. Fifty-seven pairs were new and three repeated earlier smoke cases. Pairs shared facts and are not 60 independent observations.

All 60 HTTP responses were 200. **58/60** outputs were valid; two failed probability-distribution validation. There were no automatic retries. Known usage covered 58 requests: 52,174 input and 2,780 output tokens. Usage for the two invalid responses was unknown.

## Annotation history

| Reference version | Label composition | Jev agreement | Always `no_conflict` |
| --- | --- | --- | --- |
| Initial lead labels | 41 no-conflict, 19 insufficient | 40/60 | 41/60 |
| Independent blind model annotation | 49 no-conflict, 10 insufficient, 1 conflict | 47/60 | 49/60 |
| Focused rubric reassessment | 49 no-conflict, 11 insufficient | 46/60 | 49/60 |

Blind annotation did not receive the original labels or Jev predictions. Focused review subsequently found that the single positive-conflict label assumed version overlap that was not established; it was revised to `insufficient_context`. Original annotations were retained. Eight lead labels disagreed with the final reviewed labels. No additional provider calls were made for this reassessment.

These are model-assisted rubric references, not human gold. The final cohort contains **zero confirmed positive conflicts**, so it cannot establish recall on actual contradictions.

## Final reviewed confusion matrix

| Reference | Predicted no-conflict | Predicted conflict | Predicted insufficient | Invalid |
| --- | --- | --- | --- | --- |
| No-conflict (49) | 45 | 3 | 0 | 1 |
| Insufficient context (11) | 6 | 3 | 1 | 1 |

Only one of eleven insufficient-context cases received the corresponding abstention. A constant no-conflict baseline scored 49/60, above Jev's 46/60. Such a majority baseline also cannot detect actual conflicts; this comparison diagnoses the current sample rather than endorsing that baseline as a product feature.

## Repeated-request stability

An identical migration-pair request changed from `conflict` in the six-pair smoke to `no_conflict` here. Its later probabilities were 0.50 no-conflict, 0.45 conflict and 0.05 insufficient-context, with confidence 0.25. The request hashes matched. Record creation timestamps do not establish the validity interval of the underlying claims.

**Conclusion:** no demonstrated advantage for real-fact conflict detection. A future evaluation needs independently confirmed positive conflicts and explicit applicability context before testing reviewer workload or automatic decisions.
