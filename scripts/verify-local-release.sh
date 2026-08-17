#!/usr/bin/env bash
set -euo pipefail

if (($# != 1)); then
  echo "usage: $0 RELEASE_DIR" >&2
  exit 2
fi

release_dir=$1
repo_root=$(cd "$(dirname "$0")/.." && pwd -P)

for architecture in amd64 arm64; do
  file="$release_dir/compose.local-${architecture}.yaml"
  if [[ ! -f $file ]]; then
    echo "missing release asset: $file" >&2
    exit 1
  fi
  if grep -Eq '(^|[[:space:]])(env_file:|external:)|\$\{' "$file"; then
    echo "local release validation failed: external configuration dependency in $file" >&2
    exit 1
  fi
  docker compose -f "$file" config --format json |
    python3 "$repo_root/scripts/verify-local-release.py" "$architecture"
done
