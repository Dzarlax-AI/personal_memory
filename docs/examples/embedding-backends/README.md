# Local and cloud embedding backends

Implementation supports TEI, llama.cpp EmbeddingGemma 2, OpenAI, and Google Gemini.
Facts and document search have independent profile bindings. This directory
contains opt-in configuration examples and a manifest-bound migration procedure;
copying an example does not change a running deployment. The local Gemma example
uses one model process for facts, document chunks, and folder summaries.

## Configuration

Mount `profiles.json` read-only and set `EMBED_PROFILES_FILE` in **both** the server
and standalone indexer. The supplied file deliberately keeps the existing E5
bindings active. Other profiles are inactive: no keys, probes or cloud calls occur.
Without this variable the historical `EMBED_*` behavior and collection names remain.
Strict parsing rejects unknown fields and duplicate keys.

After preparing the new collections, change both bindings in one configuration:

- `bindings.memory.profile=gemma`, `collection=memory_gemma2_v1`.
- `bindings.rag.profile=gemma`, `chunks_collection=doc_chunks_gemma2_v1`,
  `folders_collection=doc_folders_gemma2_v1`, `search_mode=flat`.
- Add `bindings.memory.thresholds` with explicit numeric `dedup`, `related`,
  `mutation`, `viz`, and a short `basis` describing the checked examples and
  threshold choice. `related < dedup`; values must be in `(0,1]`. `viz` controls
  graph and default duplicate suggestions for an opt-in profile.

No universal Gemma production thresholds are claimed here. E5's defaults are not
silently carried over. A brief, private fact-pair/mutation safety review determines
these values before production cutover; this is not another multi-hour soak.
A nonlegacy memory binding without the values and basis fails configuration loading.

All active collections are inspected before any identity creation/adoption. A
provider, model, dimension, purpose profile, normalization or declared weights
mismatch refuses startup. Existing E5 identity schema 1 stays compatible. New
providers use schema 2. OpenAI/Google identities identify a provider model ID,
not an immutable model revision. Gemma's HTTP server cannot attest file SHA256:
the descriptor records **declared** weights provenance. Verify the actual file
before starting the pinned service.

## Local model service

`gemma.compose.yml` contains the reviewed pinned image, 2 CPU/2 threads, one
request slot, 2048 context, 1 GiB memory and no swap. It exposes no host port and
attaches the model only to an internal Docker network. Merge the server's new
network with its existing deployment networks; do not replace Traefik/Qdrant
connectivity. Preserve API-key/OAuth, Todoist and browser auth configuration.

On the VPS, check the GGUF against:

```text
embeddinggemma-2-Q8_0.gguf
sha256:2188ac1deca4b77dffefd603c2776a9d76d9d74ec01841392982ebb840b09135
```

Use a dedicated verified model copy readable by the container UID (for example,
model file mode 0444 in its read-only bind mount). Keep the original cache
permissions unchanged; dropping capabilities removes root DAC override.

The application verifies reported context and alias and probes output dimensions.
Each Gemma input is tokenized with special tokens and rejected above 2048; it is
never silently shortened. Folder summaries must satisfy the same limit. Oversized
legacy summaries require rebuilding from source using the current bounded
summarizer, rather than truncating text during migration.

## Migration on the data host

Use `/personal-memory-migrate-embeddings` from a reviewed, pinned application
image on the data host; its source is `cmd/migrate-embeddings`. Keep manifests on the VPS with
mode 0600. Nothing below exports payloads, vectors, document paths or credentials.
No source collection is overwritten, and no server configuration is changed.

Prepare each role separately against a **new** collection, using the same profile
file (its active bindings do not determine a migration's target):

```sh
personal-memory-migrate-embeddings --profiles-file /config/embedding-profiles.json \
  --profile gemma --role memory --source memory --target memory_gemma2_v1 \
  --manifest /migration/facts.json
personal-memory-migrate-embeddings --profiles-file /config/embedding-profiles.json \
  --profile gemma --role chunks --source doc_chunks --target doc_chunks_gemma2_v1 \
  --manifest /migration/chunks.json
personal-memory-migrate-embeddings --profiles-file /config/embedding-profiles.json \
  --profile gemma --role folders --source doc_folders --target doc_folders_gemma2_v1 \
  --manifest /migration/folders.json
```

`QDRANT_URL` is required. `prepare` is read-only apart from exclusive creation of
the manifest and synthetic target-model probes. Manifest counts, source identity,
full source fingerprint and selected-payload fingerprint bind the operation.
The working set is bounded at 100,000 points / 128 MiB per scan; exceeding it is a
refusal, never partial acceptance. Larger corpora need a streaming implementation.

During the separately reviewed production window, stop all source and target
writers, including recall counters, maintenance and automatic indexing. Take
verified collection snapshots before transfer. Prepare fresh manifests after the
writers drain, then repeat each command with:

```text
--mode apply --confirm-writers-stopped
```

This is a real full backfill and can take time. It preserves IDs and **all** payload
fields: lifecycle, provenance, namespace, tags, timestamps and recall counters.
Chunks select the newest complete sealed generation for every source file and
exclude superseded generations. A file without a verified seal refuses migration;
use the standalone indexer to build both target document collections directly
from the unchanged source directory instead, then verify complete file/hash counts.
Do not call a partial document collection a successful migration.

An interrupted apply can resume with the same manifest and stopped writers. Only
missing points are embedded. Resume refuses foreign target metadata, unexpected
points or changed payloads. An uncertain storage write is resolved by scanning the
owned target on the next explicitly requested resume; no automatic dispatch loop.

Repeat each command with `--mode verify` before accepting the prepared collections.
Verification checks source freshness, target ownership/identity, exact payload/ID
hash, point count and finite nonzero vectors of the correct dimension. Any source
change invalidates the manifest. This is not a concurrent-write CAS guarantee:
writers must stay stopped through final verification and configuration switching.

Start the server with both Gemma bindings together and run authenticated MCP
initialize/tools-list, a fact store/recall/update/exact-ID lifecycle path, document
search/index/update, and health/restart/resource checks. Check that server, indexer,
backup, optional AI/maintenance and viz use the selected collections. Standalone `maintenance analyze` and `ai-maintenance` also read the memory
binding from `EMBED_PROFILES_FILE`; an explicit `--collection` overrides it.
Historical ID/lifecycle migration CLIs must receive explicit `--collection`
arguments; their historical default remains `memory`. Remove E5 only after no consumers use
it. Preserve its image/config/snapshots for rollback; do not delete old collections.

## Rollback, including new writes

Before any new writes, the unchanged E5 collections can be selected again.
After new writes, **do not** simply restore the old bindings: E5 is stale.
Stop writers, prepare/apply/verify new reverse migrations using `--profile e5`,
the current Gemma collections as sources and **fresh** E5 rollback collections as
targets. This preserves facts created/edited/deleted since cutover and their latest
metadata. For documents, rebuild from the authoritative directory or transfer
verified current generations. Switch all bindings only after verification.
No source or post-cutover payload is overwritten by an old snapshot.

## Explicit cloud selection

OpenAI supports `text-embedding-3-small` and `text-embedding-3-large` with explicit
dimensions and `openai-text-v1`. Google supports `gemini-embedding-2` with
`gemini-embedding-2-search-v1`, or `gemini-embedding-001` with
`gemini-embedding-001-task-v1`. Their vector spaces/profiles are distinct.

Only active cloud profiles read their named API-key environment variables.
Endpoints are fixed to official HTTPS hosts, credentials use headers, redirects
are refused, errors omit raw bodies, and transient retries are bounded. No
cross-provider fallback exists. Google explicitly requests no input truncation;
Gemini 2 uses search prefixes, while 001 uses task types. New provider vectors are
L2-normalized. Choosing cloud requires new matching collections and explicit
permission for the real data being sent; adapter support is not that permission.

Checks here use mocked/synthetic providers. No paid cloud smoke, live model
acceptance, production migration, deployment or private cloud egress is claimed.

API references: [OpenAI](https://developers.openai.com/api/reference/resources/embeddings/methods/create),
[Google](https://ai.google.dev/api/embeddings),
[llama.cpp server](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md).
