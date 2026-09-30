# 2. Synthetic fact relations, Stage A

Date: 29 September 2026. Status: completed offline evaluation; aggregate no-winner.

The corpus contained **240 synthetic pairs**, split into 120 development and 120 holdout pairs. Holdout evidence covered 40 semantic families and 30 dependency groups. Labels were model-assisted; 188 pairs received blind review. This was not human gold.

Development selection froze `concise-v1`, confidence threshold **0.8**, and an action class mask of `equivalent`, `conflict`, and `temporal_change`. D0 used cosine-based duplicate/related thresholds; D1 was a conservative deterministic comparator. The selected workflow and full seven-class classifier were evaluated separately.

## Three holdout runs

Precision below concerns accepted predictions for the selected classes, not every request. Recall includes abstentions and invalid responses in the class denominator.

| Run | Class | Correct / accepted | Correct / reference class | Class gate |
| --- | --- | --- | --- | --- |
| 1 | equivalent | 17/17 | 17/24 | insufficient_evidence |
| 1 | conflict | 21/21 | 21/24 | pass |
| 1 | temporal_change | 11/11 | 11/18 | insufficient_evidence |
| 2 | equivalent | 18/18 | 18/24 | pass |
| 2 | conflict | 20/20 | 20/24 | pass |
| 2 | temporal_change | 7/7 | 7/18 | insufficient_evidence |
| 3 | equivalent | 18/18 | 18/24 | pass |
| 3 | conflict | 20/20 | 20/24 | pass |
| 3 | temporal_change | 12/12 | 12/18 | pass |

All accepted predictions in those selected-class rows had zero recorded dangerous or direction errors. Small accepted sample sizes still prevented admission in runs 1 and 2. Temporal-change evidence was particularly unstable. Admission required at least 18 accepted equivalent/conflict predictions, at least 12 correct temporal predictions, precision of at least 95%, recall of at least 60%, and separate language conditions. Classes could not be dropped after inspecting holdout results.

| Run | Full seven-class gate | Selected classifier | Saved-memory workflow B | Pre-write workflow C |
| --- | --- | --- | --- | --- |
| 1 | fail | fail | fail | fail |
| 2 | fail | fail | fail | fail |
| 3 | fail | pass | pass | pass |

The aggregate contract required success across all three runs. Every aggregate gate failed. A passing third run does not override the earlier failures.

## Workflow and robustness findings

At a review budget of 20, the recorded useful-item counts were 18, 16 and 19 for Jev, versus 2 for D0 and 0 for D1. Recorded critical errors were zero for Jev and D1, versus 10 for D0. Much of the gain came from one artificial group of similar facts, limiting generalization. Pre-write recovery counts were 7, 6 and 8 drafts. These workflow signals did not satisfy the overall repeatability gates.

The order control swapped 20 pairs: 18 were comparable, with one relation disagreement and one direction disagreement. Two comparisons were invalid. This is a robustness diagnostic, not proof of order invariance.

The campaign recorded 621 attempts and 620 receipts, with one original ambiguous intent retained rather than silently rewritten as success. One retry was explicitly approved. Known input usage was 741,724 tokens, with 304,920 reserved for unknown usage, against limits of 5,000,000 input tokens and 800 attempts. Quality gates take precedence over favorable subsets and implementation checks.

Historical implementation checks included the Go test suite, focused race checks, public v3/v4 evaluation replays and isolated Qdrant candidate checks. These are implementation checks from the campaign, not evidence of semantic quality or new checks run during publication. Visual inspection through CUA was not confirmed; renderer checks passed.

**Conclusion:** retain the offline evidence; do not add automatic fact maintenance or a server adapter from this experiment.
