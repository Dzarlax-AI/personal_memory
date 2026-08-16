---
title: Conformance suite
---

# Model memory conformance

The model memory conformance suite tests whether an AI client follows the
[Model Memory Usage Contract](../../reference/model-memory-usage-contract/). It evaluates observable
tool choices, results, disclosures, claims, fallbacks, and retry relationships.
It does not evaluate general model intelligence or require a model provider in
ordinary CI.

## Public suite

The public v1 suite contains one synthetic scenario and one passing synthetic
trace for every stable scenario identifier in contract version 1.0.0:

```text
conformancedata/public/v1/
  scenarios.json
  traces/
    passing.json
    failing.json
```

`passing.json` is the public release gate. `failing.json` is regression evidence
that the validator detects required-event, forbidden-event, order, count, retry,
and observability failures.

Suite version 1.1.0 adds `assertions.any_of`, whose branches contain required
events and optional ordering rules. A scenario passes this assertion only when
at least one complete branch passes. This represents normative alternatives
without weakening ordering requirements on the disclosure branch.

Run the release gate:

```bash
make conformance-public
```

The command writes local reports to:

```text
conformance-results/public.json
conformance-results/public.md
```

Both files are ignored by Git. The JSON report is intended for automation. The
Markdown report is a client-by-scenario compliance matrix.

## Result meanings

| Status | Meaning |
|---|---|
| `pass` | Every required observation was present and every assertion passed. |
| `fail` | The trace contains a normative behavior violation. |
| `inconclusive` | The adapter did not observe enough behavior to prove conformance. |
| `error` | The adapter, trace, scenario, or runner failed a technical check. |

The gate passes only when every result is `pass`. Missing observability never
becomes an implicit success.

Reason codes are stable, privacy-safe identifiers:

- `required_event_missing`
- `forbidden_event_present`
- `event_count_invalid`
- `event_order_invalid`
- `retry_limit_exceeded`
- `observation_incomplete`
- `contract_version_mismatch`
- `scenario_unknown`
- `adapter_error`

## Privacy boundary

Scenario inputs are public synthetic text. Normalized traces and reports use a
closed JSON schema and cannot contain:

- prompt or response text;
- tool arguments or results;
- fact, document, or task content;
- tags, namespaces, file paths, or point/task identifiers;
- credentials, endpoints, or arbitrary adapter messages.

Unknown JSON fields and unknown enum values are rejected before a report is
written. Native client logs must be normalized outside the conformance core.
When safe normalization cannot be guaranteed, the adapter must fail instead of
emitting a best-effort redaction.

Private suites may be stored under `conformancedata/private/`, which is ignored
by Git. Generated reports remain subject to the same closed schema.

## Live adapter protocol

Live mode is optional and never runs in public CI. It launches an explicitly
selected executable directly, without a shell. The runner sends one JSON request
per scenario on stdin:

```json
{
  "schema_version": 1,
  "contract_version": "1.0.0",
  "client_family": "codex",
  "scenario_id": "TASK-002",
  "intent_class": "task_create_disabled",
  "synthetic_input": "Create a synthetic review task.",
  "capabilities": {
    "memory": "available",
    "documents": "disabled",
    "todoist": "disabled"
  }
}
```

The executable must write exactly one normalized trace JSON object to stdout:

```json
{
  "schema_version": 1,
  "contract_version": "1.0.0",
  "scenario_id": "TASK-002",
  "client_family": "codex",
  "observed": ["capabilities", "tool_events", "user_visible_claims"],
  "events": [
    {
      "sequence": 1,
      "event": "capability",
      "capability": "todoist",
      "outcome": "unavailable"
    },
    {
      "sequence": 2,
      "event": "disclosure",
      "code": "task_not_created"
    }
  ]
}
```

Example invocation:

```bash
go run ./cmd/conformance-memory run \
  --source live \
  --suite conformancedata/public/v1/scenarios.json \
  --contract docs/model-usage-contract.md \
  --client-family codex \
  --adapter-exec /absolute/path/to/codex-conformance-adapter \
  --adapter-arg=--profile \
  --adapter-arg=local \
  --adapter-env=CODEX_CONFORMANCE_TOKEN \
  --json conformance-results/codex.json \
  --markdown conformance-results/codex.md
```

Environment variables are inherited only when their names are supplied through
`--adapter-env`. Adapter stdout is bounded and strictly decoded. Stderr, malformed
stdout, and process errors are not copied into reports. The overall run is
bounded by `--timeout`.

The selected variables completely replace the child process environment; an
empty selection starts the adapter with an empty environment. Every normalized
`tool_result` must have a preceding unmatched `tool_call` with the same
capability and operation. Duplicate traces for the same client and scenario are
rejected.

Supported client-family profiles are `codex`, `claude`, `chatgpt`, and
`generic_mcp`. A client-specific wrapper is responsible for observing its native
tool and response events and producing the common trace. The future integration
bundle can add such wrappers without changing scenario or validator semantics.

## Contract evolution

The runner extracts the published scenario identifiers and contract version from
`docs/model-usage-contract.md`. CI fails when the contract and public suite
differ. Do not renumber or reuse a published scenario identifier.

Schema and suite versions use semantic versioning:

- patch: clarification or new evidence that does not change observable behavior;
- minor: backward-compatible scenarios or fields;
- major: incompatible schema or safety semantics.

Every contract change must update the public suite in the same reviewed change.
