# 7. Real-fact tagging: frozen control cohort

Date: 30 September 2026. Status: completed; no advantage over deterministic rules.

The remaining 33 facts from the approved 65-fact selection were disjoint from the 32 tagging-development facts. The six question definitions and deterministic baseline code were preserved exactly. Labels were fixed before provider calls.

This is a tagging holdout, **not a wholly unseen corpus**: its facts had already appeared in the conflict study. It was skewed toward one project: 22 assistant references, two infrastructure references, one memory reference and eight insufficient-context references. There were no Todoist examples.

All 33 requests returned HTTP 200 and all **198 heads** were valid. Usage: 51,722 input and 10,230 output tokens.

## Primary project results

Agreement includes correct abstentions. Correct proposals exclude abstentions, so their denominator differs from all-item agreement.

| Method | Agreement | Correct / proposed tags | Abstentions |
| --- | --- | --- | --- |
| D0: explicit aliases | 30/33 | 22/22 | 11 |
| D1: frozen deterministic rules | 31/33 | 24/25 | 8 |
| Jev | 25/33 | 24/32 | 1 |

All three methods agreed on **22/22** explicit-alias facts. The remaining cases were more informative:

| Method | Agreement without explicit aliases | Correct / proposed tags | Abstentions |
| --- | --- | --- | --- |
| D0 | 8/11 | 0/0 (no proposals) | 11 |
| D1 | 9/11 | 2/3 | 8 |
| Jev | 3/11 | 2/10 | 1 |

Of eight insufficient-context references, Jev abstained once and proposed a project seven times. Jev correctly handled one implicit-project case missed by D1, but this did not compensate for additional unsupported assignments. The reported confidence of a guess does not supply missing project evidence.

## Secondary facet diagnostics

| Head | Agreement |
| --- | --- |
| Architecture | 16/33 |
| Configuration | 14/33 |
| Bug | 26/33 |
| Decision | 17/33 |
| Preference | 29/33 |

Project classification was the primary comparison. These secondary facet results are diagnostics, not separately established production gates. No facts or tags were changed.

**Conclusion:** frozen D1 outperformed Jev on this control selection. No server-side automatic primary tagging or facet enrichment is justified by the measured results.
