# Optional AI memory protocol fixtures

This synthetic, author-labelled corpus exercises payload construction, paired providers and catalog/origin ablations. It is not an independent quality benchmark. Its eight writes and five reads contain no private memory records. Read expectations describe relevance, not permission to override lifecycle authority.

Build an exact private preview without credentials or HTTP dispatch:

```sh
go run ./cmd/eval-ai-memory --corpus evaldata/experiments/optional-ai-memory-v1/synthetic.json --dir eval-results/optional-ai-memory-v1/preview
```

The preview contains both providers, four write arms (text, catalog, origin, both), and two read arms (text, catalog). Allowed choices remain identical; semantic catalog evidence is removed only from the shared state in text-only arms. Facts without a declared origin remain null in every origin arm. No historical origin is reconstructed. The preview binds corpus bytes and every serialized request to SHA-256.

Score an offline response manifest against that exact preview without credentials or HTTP:

```sh
go run ./cmd/eval-ai-memory --corpus evaldata/experiments/optional-ai-memory-v1/synthetic.json --dir eval-results/optional-ai-memory-v1/preview --results /path/to/offline-results.json
```

The optional results file has schema version 1 and binds its rows to the preview's `corpus_sha256`. Each row carries `request_sha256`, `task`, `case_id`, `provider`, `arm`, and `status` (`decided`, `abstained`, or `invalid`). Write rows use `primary_tag` for known projects and an explicit `subject_decision` for abstentions (`insufficient_context`, `other_project`, `non_project`, or `multiple_projects`); read rows use one finite `scores` value for every request-local candidate alias and a `none_relevant` boolean. Rows are joined to the exact provider/arm/case request and checked against its hash and labels. Duplicate or unknown rows, hash mismatches, and invalid statuses reject the results file. Missing rows and malformed decisions stay in the expected denominators as invalid; they are never dropped to improve a score.

Schematic rows (replace the hashes with values copied from `preview.json`):

```json
{
  "schema_version": 1,
  "corpus_sha256": "<preview corpus hash>",
  "rows": [
    {
      "request_sha256": "<write request hash>",
      "task": "write",
      "case_id": "w1",
      "provider": "decisions",
      "arm": "both",
      "status": "decided",
      "primary_tag": "personal-memory"
    },
    {
      "request_sha256": "<read request hash>",
      "task": "read",
      "case_id": "r5",
      "provider": "jev",
      "arm": "catalog",
      "status": "decided",
      "scores": {"c1": 0.1, "c2": 0.2},
      "none_relevant": true
    }
  ]
}
```

Provider, transport, malformed-response, or refusal failures should be represented as `status: "invalid"` for the bound request. `abstained` write rows require the exact `subject_decision` and have no `primary_tag`, `scores`, or `none_relevant` field. Missing or mismatched abstention labels receive no credit.

`report.json` is written beside `preview.json` with private file permissions. It reports write primary accuracy, abstention/invalid counts, and read MRR plus separate no-relevant accuracy for each paired provider/arm. Read order first follows lifecycle authority (`canonical_current`, `other_current`, `disputed`, `historical`, `superseded`); scores reorder candidates only within the same authority bucket, and score ties preserve input order. MRR is reported against that same comparator alongside baseline MRR and delta MRR; no-relevant cases contribute zero to MRR and have their own accuracy metric. The report marks the quality verdict `not_evaluated` and makes no model-quality claim. The bundled labels are synthetic author references, not human-adjudicated or independent holdout truth. Do not publish provider responses containing private memory facts as public fixtures.

Existing real-fact campaigns and references are retained separately. Human adjudication of reference-audit-v3 and a newly frozen independent holdout remain necessary before quality claims or operational enablement. The runtime feature remains experimental and off by default. There is deliberately no credential-bearing live-run command in this preview CLI.
