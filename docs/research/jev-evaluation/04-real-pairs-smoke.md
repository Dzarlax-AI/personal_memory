# 4. Six real fact pairs: smoke check

Date: 30 September 2026. Status: bounded real-data exploratory check.

Six approved pairs were submitted to the J3 contract. All six HTTP requests succeeded and all six outputs were valid. Preliminary lead labels were five `no_conflict` and one `insufficient_context`; agreement was **5/6**. There were **zero confirmed positive conflict examples**.

The uncertain pair concerned migration stages whose overlapping applicability was not established. Jev returned `conflict` with probability 0.56, `no_conflict` 0.39 and `insufficient_context` 0.05; reported confidence was 0.34. This does not establish a real contradiction.

Usage: 5,530 input tokens and 287 output tokens; no retries. The sample was handpicked, small and predominantly compatible. Labels were preliminary, without independent adjudication.

The exact same migration request later changed to `no_conflict` in the expanded campaign. See [the expanded report](05-real-pairs-expanded.md).

**Conclusion:** a successful smoke check, insufficient evidence for conflict detection or maintenance automation.
