# 3. Conflict advisory v2: technical probe

Date: 30 September 2026. Status: stopped at the technical gate.

Twenty distinct synthetic development families from v1 were tested with two contracts: J7 (seven relations with direction) and J3 (`conflict`, `no_conflict`, `insufficient_context`). The language composition was seven English, seven Russian and six mixed-language pairs. Both variants received the same 20 pairs, for **40 calls**.

| Variant | HTTP attempts | Valid | Invalid | Known input tokens | Reserved unknown tokens |
| --- | --- | --- | --- | --- | --- |
| J3 | 20 | 20 | 0 | 15,278 | 0 |
| J7 | 20 | 17 | 3 | 19,638 | 13,111 |

All HTTP responses were 200; no transport failure, local failure, missing receipt or ambiguous request was recorded. J7 had two `relation_direction_mismatch` responses and one `probability_distribution` failure. The combined gate allowed at most two invalid responses and observed three, yielding `technical_gate_failed` and an unresolved contract error.

Invalid raw bodies were not retained, so the exact probability-validation subtype cannot be reconstructed. Replay matched the saved report; it does not provide new model-quality evidence.

The planned 360-pair corpus, independent labels, development selection, full holdout and order-control campaign were **not executed**. J3's 20/20 valid responses establish technical validity on this probe only.

## Execution roles

The recorded allocation used Astra/high for contract work, Luna/high for the client and initial runner, Sol/high for independent review and repair, and the coordinating agent for integration and gates. This allocation describes how the probe was implemented, not comparative model quality.

**Conclusion:** stop before the larger campaign; no semantic winner and no runtime approval.
