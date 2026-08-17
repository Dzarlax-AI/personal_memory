---
title: Client integration bundle
---

# Versioned client integration bundle

The client integration bundle turns the [Model Memory Usage Contract](../../reference/model-memory-usage-contract/) into deterministic, client-specific policy artifacts. It configures how a client should choose between Personal Memory facts, document search, Todoist, and ordinary conversation context. It does not configure or modify the Personal Memory server, Qdrant, deployment, credentials, or MCP endpoints.

## Versions and architecture

The first public release has these independently validated identities:

| Component | Version |
|---|---|
| Bundle | `0.1.0` |
| Contract | `1.0.0` |
| Public conformance suite | `1.1.0` |
| Client artifact formats | `1.0.0` |

The standalone executable embeds the manifest, canonical policy, client templates, normative contract, and public conformance suite. It verifies their bound identities, renders one capability-specific artifact set, then installs, updates, verifies, rolls back, or exports it. A client wrapper may make a rule more concrete, but it must preserve every canonical rule.

No prompt, memory content, document content, task content, credential, user identifier, path, endpoint, vector, or hidden reasoning is embedded in the bundle.

## Supported client surfaces

| Client | Installed surface |
|---|---|
| Codex | A managed block in the target configuration root's `AGENTS.md`, plus `skills/personal-memory/SKILL.md` |
| Claude Code | `rules/personal-memory.md`, `skills/personal-memory/SKILL.md`, and a managed reminder hook merged into `settings.json` |
| ChatGPT | Exported behavior prompt and remote-MCP registration guidance; official UI or administrator action is still required |
| Generic MCP | Portable `generic-mcp/policy.json` and `generic-mcp/tool-mapping.json` artifacts for a host application to consume |

ChatGPT installation and verification return `manual_action_required`. The installer cannot configure ChatGPT's official UI, workspace controls, or administrator-managed connector registration automatically.

## Quick commands for Codex and Claude Code

Most users do not need the repository or a Go toolchain. Download the matching `memory-integration-{darwin|linux}-{amd64|arm64}` binary and `SHA256SUMS` from the [latest release](https://github.com/Dzarlax-AI/personal-memory/releases/latest), verify the checksum, and make the binary executable. The complete download example is in [Connect clients](../../getting-started/connect-clients/).

After registering the MCP endpoint and observing the Memory tools in a fresh client session:

```bash
./memory-integration-darwin-arm64 quick-install codex --target-root "$HOME/.codex" --confirm-tools-discovered
./memory-integration-darwin-arm64 quick-verify codex --target-root "$HOME/.codex" --confirm-tools-discovered
```

Use `claude` for Claude Code. Add `--with-documents` only when `search_documents` is also visible. The default is Memory available, Documents disabled, and Todoist disabled. `quick-update` and `quick-verify` preserve the verified installed capability configuration unless `--with-documents` or `--memory-only` explicitly changes it. `quick-rollback` restores the previous verified transaction and does not accept a discovery confirmation.

The confirmation flag records a real observation made in the client. It is not a server probe. Quick commands require an explicit absolute `--target-root`, refuse missing or symlinked roots, and never create them. Standard installations normally use `~/.codex` or `~/.claude`; custom installations must pass their actual configuration root. Use `--json` when another local tool needs a privacy-safe result containing only the client, root, capability states, bundle version, outcome, and changed flag.

## Advanced interface and target roots

The advanced interface supports custom roots, dry runs, explicit capability states, ChatGPT exports, and generic MCP hosts. From the repository root:

```bash
go build -o ./memory-integration ./cmd/memory-integration
```

`--target-root` means the root of the selected client's configuration, not the repository and not the Personal Memory service data directory:

- Codex: normally `~/.codex`; the installer merges its block into `~/.codex/AGENTS.md` and writes the skill below that root.
- Claude Code: normally `~/.claude`; the installer writes rules and skills and preserves unrelated entries while merging its hook into `~/.claude/settings.json`.
- ChatGPT: an explicit local export directory. The produced files are instructions for a later manual UI/admin step.
- Generic MCP: an explicit export or host-application configuration directory.

The target directory must already exist. For safe filesystem mutation, bundle `0.1.0` supports only Darwin and Linux. Rendering and installation mutations are deliberately unsupported on other operating systems.

Create the configuration roots before using the examples below:

```bash
mkdir -p "$HOME/.codex" "$HOME/.claude"
```

## Capability discovery

Capabilities describe the tools available to the client session. `ordinary_context` is always required. The optional states are `available`, `disabled`, or `unavailable` for Memory, Documents, and Todoist.

When any capability is `available`, installation and update require explicit discovery evidence. Create a local file such as:

```json
{
  "performed": true,
  "tools": [
    "recall_facts",
    "store_fact",
    "update_fact",
    "set_fact_lifecycle",
    "search_documents"
  ]
}
```

The complete tool requirements are:

- Memory: `recall_facts`, `store_fact`, `update_fact`, `set_fact_lifecycle`
- Documents: `search_documents`
- Todoist: `get_tasks`, `create_task`, `update_task`, `complete_task`, `delete_task`

The file records discovery results; it does not contain endpoints, credentials, prompts, or tool payloads. Supply only tools actually discovered in the target client.

## Install, verify, update, and rollback

Install Codex with Memory available and the other optional capabilities disabled:

```bash
./memory-integration install \
  --client codex \
  --target-root "$HOME/.codex" \
  --capability memory=available \
  --capability documents=disabled \
  --capability todoist=disabled \
  --discovery-file ./memory-tools.json
```

Preview exactly the same operation without writing:

```bash
./memory-integration install \
  --client codex \
  --target-root "$HOME/.codex" \
  --capability memory=available \
  --capability documents=disabled \
  --capability todoist=disabled \
  --discovery-file ./memory-tools.json \
  --dry-run
```

Verify installed artifacts and rediscovered tools:

```bash
./memory-integration verify \
  --client codex \
  --target-root "$HOME/.codex" \
  --capability memory=available \
  --capability documents=disabled \
  --capability todoist=disabled \
  --discovery-performed \
  --tool recall_facts \
  --tool store_fact \
  --tool update_fact \
  --tool set_fact_lifecycle
```

Update uses the same capability and discovery inputs as install:

```bash
./memory-integration update \
  --client codex \
  --target-root "$HOME/.codex" \
  --capability memory=available \
  --capability documents=disabled \
  --capability todoist=disabled \
  --discovery-file ./memory-tools.json
```

Roll back to the previous verified transaction:

```bash
./memory-integration rollback \
  --client codex \
  --target-root "$HOME/.codex" \
  --capability memory=available \
  --capability documents=disabled \
  --capability todoist=disabled
```

Use the same command shapes with `--client claude` and a Claude configuration root. The reported JSON contains status, changed state, privacy-safe action paths and digests, capability states, and missing tool names; it never contains generated policy content or credentials.

## Disabled and unavailable examples

An entirely local, ordinary-context-only Codex policy needs no discovery file:

```bash
./memory-integration install \
  --client codex \
  --target-root "$HOME/.codex" \
  --capability memory=disabled \
  --capability documents=disabled \
  --capability todoist=disabled
```

To state that Memory and Documents are not reachable while Todoist is intentionally disabled:

```bash
./memory-integration install \
  --client claude \
  --target-root "$HOME/.claude" \
  --capability memory=unavailable \
  --capability documents=unavailable \
  --capability todoist=disabled
```

`disabled` means intentionally not enabled for the client; `unavailable` means expected or relevant but not usable. Both are distinct from a runtime timeout, which clients must disclose as a failed attempt rather than an empty result.

Todoist is independent of fact memory and document search. When Todoist is disabled, undiscovered, unavailable, or fails, the task remains uncreated. The client may provide manual task text, but it must never fall back to storing the task as a fact or document.

## Render-only exports and ChatGPT

`render` writes the validated source artifact inventory without activating client-native paths or writing installer state:

```bash
mkdir -p "$(pwd)/rendered-generic"
./memory-integration render \
  --client generic_mcp \
  --target-root "$(pwd)/rendered-generic"
```

For ChatGPT, render or install into a dedicated export directory:

```bash
mkdir -p "$(pwd)/chatgpt-export"
./memory-integration install \
  --client chatgpt \
  --target-root "$(pwd)/chatgpt-export" \
  --capability memory=available \
  --capability documents=disabled \
  --capability todoist=disabled \
  --discovery-file ./memory-tools.json
```

The `manual_action_required` result is expected. Review the generated behavior prompt and registration guidance, then apply them through the official ChatGPT UI or your workspace administrator's supported process. This repository does not claim an automatic ChatGPT installation path.

## Hermes Agent and OpenClaw

Hermes Agent and OpenClaw both support remote Streamable HTTP MCP servers, but bundle `0.1.0` does not have native `hermes` or `openclaw` client families. Use two separate layers:

1. Register the Personal Memory MCP endpoint with the client and prove which tools it exposes.
2. Export the `generic_mcp` policy and load it through the client's own instruction surface.

Connecting the MCP endpoint alone makes tools available; it does not install the versioned usage policy. Conversely, rendering the policy does not configure an endpoint or credential. The generic artifacts are deterministic and contract-bound, but the repository does not claim Hermes- or OpenClaw-specific conformance evidence until native adapters are added.

### Export the shared policy

Create a dedicated export with capability states that match the tools you will enable. The example below enables Memory and Documents while keeping Todoist disabled:

```bash
cat >./memory-tools.json <<'JSON'
{
  "performed": true,
  "tools": [
    "recall_facts",
    "store_fact",
    "update_fact",
    "set_fact_lifecycle",
    "search_documents"
  ]
}
JSON

mkdir -p "$(pwd)/personal-memory-policy"
./memory-integration install \
  --client generic_mcp \
  --target-root "$(pwd)/personal-memory-policy" \
  --capability memory=available \
  --capability documents=available \
  --capability todoist=disabled \
  --discovery-file ./memory-tools.json
```

The authoritative generated files are `generic-mcp/policy.json` and `generic-mcp/tool-mapping.json` below that target root. Do not put an API key, endpoint, prompt, or retrieved content into either file. When a capability changes, regenerate the export with `update` and the new discovery evidence.

### Hermes Agent

Hermes reads remote MCP definitions from `~/.hermes/config.yaml`. Keep the API key outside the repository; Hermes supports `${ENV_VAR}` substitution from its environment, including values stored in its local `~/.hermes/.env`:

```yaml
mcp_servers:
  personal_memory:
    url: "https://mcp.example.com/memory"
    headers:
      X-API-Key: "${PERSONAL_MEMORY_API_KEY}"
    tools:
      include:
        - recall_facts
        - store_fact
        - update_fact
        - set_fact_lifecycle
        - search_documents
      prompts: false
      resources: false
```

If MCP OAuth is enabled on the Personal Memory server, use `auth: oauth` instead of the static header and run `hermes mcp login personal_memory`. After editing the configuration, start a new session or use `/reload-mcp`. Hermes prefixes registered tools with the server name, for example `mcp_personal_memory_recall_facts`; discovery evidence for this bundle still records the raw server tool name `recall_facts`.

Hermes loads only the first matching project context file from the active project, in this order: `.hermes.md`, `AGENTS.md`, then `CLAUDE.md`. Put the generated canonical policy and tool mapping directly into that selected context file. Do not add only a pointer that the agent may never read: the policy must be injected by the host wrapper or included in the loaded context. Keep project-specific refinements outside the generated files so an update cannot overwrite them.

Verify connectivity and the effective tool filter before declaring a capability available:

```bash
hermes mcp configure personal_memory
hermes chat
```

The configuration and reload behavior above follows the [official Hermes MCP guide](https://hermes-agent.nousresearch.com/docs/user-guide/features/mcp); context-file precedence follows the [official context-files guide](https://hermes-agent.nousresearch.com/docs/user-guide/features/context-files).

### OpenClaw

OpenClaw stores outbound MCP definitions in its own registry. When Personal Memory OAuth is enabled, prefer the OAuth flow:

```bash
openclaw mcp add personal-memory \
  --url https://mcp.example.com/memory \
  --transport streamable-http \
  --auth oauth \
  --include 'recall_facts,store_fact,update_fact,set_fact_lifecycle,search_documents'

openclaw mcp login personal-memory
openclaw mcp doctor personal-memory --probe
```

For an API-key-only deployment, configure an `X-API-Key` header through the local OpenClaw MCP settings or `openclaw mcp add --header`. OpenClaw warns about literal secrets in configuration: never commit its config, avoid putting the key directly in shell history, and keep the local state owner-only. Add `/todoist` as a separate MCP server only when Todoist is enabled; otherwise keep the bundle capability explicitly `disabled`.

OpenClaw loads operating instructions from `AGENTS.md` in the active agent workspace, normally `~/.openclaw/workspace`. Inject or include the generated canonical policy and tool mapping there without replacing unrelated workspace instructions. In multi-agent setups, repeat this for each workspace that should use Personal Memory; an MCP registry entry alone does not guarantee that every agent receives the same policy.

After changing the registry, reload the owning runtime if necessary and probe the live server:

```bash
openclaw mcp reload
openclaw mcp status --verbose
openclaw mcp doctor personal-memory --probe
```

The registry, probe, transport, and OAuth commands above follow the [official OpenClaw MCP guide](https://docs.openclaw.ai/cli/mcp); workspace instruction behavior follows the [official OpenClaw workspace guide](https://docs.openclaw.ai/agent-workspace).

For both clients, mark Memory, Documents, and Todoist independently. A successful `/memory` probe does not prove that `/todoist` exists, and a missing or disabled Todoist capability must never fall back to storing a task as a fact.

## Overrides

Each installation creates one user-owned override stub under `<target-root>/overrides/`:

- Codex: `overrides/AGENTS.local.md`
- Claude: `overrides/personal-memory.local.md`
- ChatGPT: `overrides/behavior.local.md`
- Generic MCP: `overrides/policy.local.json`

The installer records that the override exists but never overwrites its content. Keep local refinements there. Do not edit managed blocks, managed hooks, or generated policy files; verification reports those edits as drift.

## Telemetry and privacy

Bundle telemetry is disabled by default and, when a host explicitly enables local telemetry, is allowlist-only. The exact allowed fields are `contract_version`, `scenario_id`, `capability`, `operation`, `outcome`, `latency_bucket`, `retry_count`, and `client_family`.

Prompts, responses, queries, fact/document/task content, identifiers, paths, credentials, users, endpoints, payloads, vectors, and hidden reasoning are forbidden. If the allowlist cannot be guaranteed, telemetry must remain disabled. The integration installer itself does not transmit telemetry.

## Transactions, backups, and locking

Install, update, and rollback use a lock scoped to the target root and client. Writes are atomic and no-follow path checks reject unsafe symlink traversal. Installation state and content-addressed backups live below `<target-root>/.personal-memory-integration/`; completed backup retention is bounded. Before mutation, the installer verifies owned content and state identity. If a write fails, it attempts compare-before-restore recovery and refuses to overwrite content changed concurrently.

Do not edit or copy installer state between target roots. A backup is bound to its client, target-root identity, bundle sources, and recorded file inventory.

## Conformance evidence

Run the deterministic public artifact gate with:

```bash
make integration-bundle-public
```

The gate builds `memory-integration` and evaluates all 32 public scenarios for `codex`, `claude`, `chatgpt`, and `generic_mcp` through the existing command-adapter protocol. Its normalized traces come from explicit enum-only trace recipes owned by the canonical bundle policy. Each recipe is bound to the exact scenario identity and policy references, validated independently for capability and operation authorization, and used only after the selected rendered client artifact decodes to the canonical policy with the matching capability configuration. The adapter does not derive output from the suite's expected assertions and reads no real prompts, tools, secrets, users, endpoints, or client state.

Although the conformance report uses the existing `source: "live"` command-adapter mode, this is bundle-policy/artifact conformance only. It is not a live model run and is not proprietary-client evidence for Codex, Claude, or ChatGPT behavior. The current normalized Trace schema has no evidence-source field, so reports and documentation carry this distinction without changing that stable schema.

## Troubleshooting

- `tool discovery must be explicitly performed`: provide a discovery file for install/update, or discovery flags for verify, whenever any capability is `available`.
- Missing tools or `drifted`: rediscover the session's tools and check that all required tools are present; restore edited managed files before updating.
- `incompatible`: the installed state, bundle version, contract identity, client inventory, or managed structure cannot be safely reconciled. Do not delete state blindly; inspect the target and use a known compatible executable.
- Destination or marker conflict: move unowned content out of the managed destination, or migrate it into the user-owned override file. The installer will not claim ownership silently.
- Lock timeout: another install, update, verify, or rollback is active for the same target. Let it finish and retry.
- Unsupported platform: perform mutations on Darwin or Linux, or use the generated artifacts manually on the target platform.
- ChatGPT `manual_action_required`: apply or verify the output in the official UI/admin surface; repeated local installation cannot complete that external action.
- Rollback unavailable: no compatible previous transaction exists, or installed files have drifted. Restore managed files to their verified state first; rollback deliberately refuses to overwrite unverified changes.

Always run `verify` after install, update, or rollback. If verification reports drift, preserve user-owned overrides and investigate before another mutation.
