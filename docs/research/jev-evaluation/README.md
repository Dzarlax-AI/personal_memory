# Jev evaluation reports

English publication editions of the Jev experiments conducted on 28–30 September 2026. These reports retain measured results, negative findings, annotation revisions and stopping decisions. Private fact texts, identifiers, request payloads and personal infrastructure details are omitted.

## Decision

The experiments have not established a reliable advantage that justifies adding Jev to the Personal Memory server. Keep production retrieval and fact writes unchanged. Automatic tagging, deletion, supersession and conflict resolution are not supported by this evidence. This conclusion applies to these datasets, prompts and the pinned model; it is not a general claim that Jev is useless.

| Experiment | Main result | Evidence boundary |
| --- | --- | --- |
| [Document retrieval probe](01-document-retrieval.md) | Top-1: 3/7 → 5/7 | Exploratory synthetic probe; candidate recall remains a limit |
| [Synthetic fact relations](02-synthetic-fact-relations.md) | Aggregate gates failed across three runs | Some selected classes worked, but repeatability gates failed |
| [Conflict advisory technical probe](03-conflict-advisory-probe.md) | J3 valid 20/20; J7 valid 17/20 | Combined technical gate failed; planned quality study stopped |
| [Six real pairs](04-real-pairs-smoke.md) | Preliminary agreement 5/6 | No independently confirmed positive conflicts |
| [Sixty real pairs and adjudication](05-real-pairs-expanded.md) | Reviewed agreement 46/60; constant baseline 49/60 | Model-assisted annotation, dependent pairs, no positive conflict gold |
| [Tagging development cohort](06-tagging-development.md) | Project 30/32; all six heads exact 6/32 | Rule baseline designed after inspection; development evidence |
| [Tagging control cohort](07-tagging-control.md) | Jev 25/33; frozen rule baseline 31/33 | Disjoint tagging cohort, but facts previously used for conflict evaluation |

## Shared method and interpretation

The fact and tagging experiments used `jev-1.13.0` through direct TypeSafe HTTP requests. Authentication remained local. They did not use the Jev MCP connector to execute the campaign. HTTP success, valid JSON, model confidence and semantic correctness are separate measurements. Confidence was not calibrated against a representative independent dataset.

Real-data egress was explicitly approved for bounded selections. No live facts, tags or lifecycle records were changed. The experiments did not deploy a runtime adapter. Independent model annotation is described as agreement with a rubric, not human-verified accuracy. Invalid responses remain in all-item denominators unless a metric explicitly states otherwise.

A possible future research direction is prioritizing suspicious pairs for human review. It would require confirmed positive conflicts, representative independent labels and comparison of reviewer effort against deterministic candidate selection. No such advantage has been demonstrated here.

## Archive and reproducibility

[metrics.json](metrics.json) contains aggregate numeric evidence selected from local reports, without private examples or per-fact identifiers. [sources.json](sources.json) lists the types of evidence used. Private source paths, checksums, identifiers and request payloads are omitted; raw artifacts remain local and ignored by Git. The publication is an English report archive, not a release of the private corpus or a fully reproducible real-data benchmark. Original local reports remain unchanged.

Repeated capture, replay and comparison files are represented within their experiment report rather than presented as separate experiments. Plans for unexecuted stages are marked as such.
