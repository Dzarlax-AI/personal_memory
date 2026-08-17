#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd -P)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

"$repo_root/scripts/render-agent-setup.sh" --release v1.2.3 --output "$scratch/valid"
grep -Fq 'releases/download/v1.2.3/' "$scratch/valid/agent-setup.md"
grep -Fq 'docker compose port memory-mcp 8000' "$scratch/valid/agent-setup.md"
if grep -Fq 'Resolve the newest stable GitHub release' "$scratch/valid/agent-setup.md"; then
  echo "agent setup resolves a moving release instead of its bound release" >&2
  exit 1
fi
if grep -Eq 'path token|client, preset' "$scratch/valid/agent-setup.md"; then
  echo "agent setup uses checkpoint aliases instead of JSON field names" >&2
  exit 1
fi

registration_state_line=$(grep -nF 'Write `registration_pending` before executing' "$scratch/valid/agent-setup.md" | cut -d: -f1)
registration_mutation_line=$(grep -nF 'codex mcp add personal-memory --url' "$scratch/valid/agent-setup.md" | cut -d: -f1 | head -1)
bundle_state_line=$(grep -nF 'write `bundle_pending`, then run' "$scratch/valid/agent-setup.md" | cut -d: -f1)
bundle_mutation_line=$(grep -nF 'quick-install codex' "$scratch/valid/agent-setup.md" | cut -d: -f1 | head -1)
if ((registration_state_line >= registration_mutation_line || bundle_state_line >= bundle_mutation_line)); then
  echo "agent setup does not checkpoint before an external mutation" >&2
  exit 1
fi

cp "$scratch/valid/agent-setup.md" "$scratch/mixed-release.md"
sed -i.bak 's|releases/download/v1.2.3/|releases/download/v9.9.9/|' "$scratch/mixed-release.md"
rm "$scratch/mixed-release.md.bak"
if "$repo_root/scripts/verify-agent-setup.sh" --release v1.2.3 "$scratch/mixed-release.md" >/dev/null 2>&1; then
  echo "agent setup validator accepted a mixed release" >&2
  exit 1
fi

echo "agent setup validation tests passed"
