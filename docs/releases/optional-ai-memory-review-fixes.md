# PR 64 review follow-up

## Confirmed CI failure

Linux CI reported 72 failures because shared test helpers and the evaluator test
used `/private/tmp`. They now use platform temporary directories, resolving
symlinks where private-path validation requires canonical paths. Shadow test
workers stop before their state directories are removed. The standalone probe is
now a regular Go package so type-checking tools can load it.

## Accepted findings

- Replay write rows carry an exact `subject_decision`. Unspecified abstentions are
  invalid; different abstention reasons no longer receive interchangeable credit.
  Non-project and multiple-project outcomes are representable.
- Scorable project tags use the runtime `contextcatalog.Eligible` predicate,
  including client-declared cards.
- Corpus parsing rejects duplicate/unknown fields and trailing JSON.
- Operator output creation preserves parent-directory permissions and rejects
  symlink parents; exclusive no-follow private file creation remains.
- Recall cache clones subject metadata on both insertion and retrieval.
- Registration evidence permits single-segment endpoint paths while retaining
  checks for private roots, multi-segment paths and Windows absolute paths.
- Initial registration no longer duplicates a summary. Maintenance consumes its
  original declared summary through a fallback and preserves that original before
  publishing a model-derived replacement.
- Maintenance has an explicit, bounded `response_models` allowlist for provider
  snapshot identifiers. There is no automatic prefix matching; the pinned
  Decisions/Jev adapters are unchanged.

## Contract decisions retained

`proposed_update` is deliberately not an accepted client identity under the
approved registration contract. A changed description is an unaccepted proposal;
clients fall back without origin/context fields. This can reduce context coverage
until review but never accepts proposed identity evidence. Existing identity can
still be resolved server-side. Changing this fallback requires an explicit
contract amendment and matching bundle version/manifest changes.

Proposal storage remains bounded and append-only. Removing count/byte limits or
replacing historical proposal artifacts would change the approved review and
retention behavior. At capacity, a new proposal returns unavailable while the
active catalog remains unchanged and baseline memory continues. Operators need to
review/archive their proposal queue; automatic coalescing is deferred. This is an
availability limitation, not a completed coalescing fix.

The two contract decisions above remain documented review concerns. This change
does not claim that every suggested behavior was adopted. No production mutation,
deployment, provider call or client installation is performed by these fixes.
