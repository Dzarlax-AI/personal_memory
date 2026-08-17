#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd -P)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

"$repo_root/scripts/render-agent-setup.sh" --release v1.2.3 --output "$scratch/valid"
grep -Fq 'releases/download/v1.2.3/' "$scratch/valid/agent-setup.md"

cp "$scratch/valid/agent-setup.md" "$scratch/mixed-release.md"
sed -i.bak 's|releases/download/v1.2.3/|releases/download/v9.9.9/|' "$scratch/mixed-release.md"
rm "$scratch/mixed-release.md.bak"
if "$repo_root/scripts/verify-agent-setup.sh" --release v1.2.3 "$scratch/mixed-release.md" >/dev/null 2>&1; then
  echo "agent setup validator accepted a mixed release" >&2
  exit 1
fi

echo "agent setup validation tests passed"
