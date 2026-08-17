#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 --release vX.Y.Z --output DIR" >&2
}

release=""
output_dir=""
while (($#)); do
  case "$1" in
    --release) release=${2:-}; shift 2 ;;
    --output) output_dir=${2:-}; shift 2 ;;
    *) usage; exit 2 ;;
  esac
done

if [[ ! $release =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || [[ -z $output_dir ]]; then
  usage
  exit 2
fi

repo_root=$(cd "$(dirname "$0")/.." && pwd -P)
source "$repo_root/install/agent-setup-cli-versions.env"
mkdir -p "$output_dir"

sed \
  -e "s|@RELEASE_VERSION@|$release|g" \
  -e "s|@CODEX_CLI_VERSION@|$CODEX_CLI_VERSION|g" \
  -e "s|@CLAUDE_CODE_VERSION@|$CLAUDE_CODE_VERSION|g" \
  "$repo_root/install/agent-setup.md" >"$output_dir/agent-setup.md"

"$repo_root/scripts/verify-agent-setup.sh" \
  --release "$release" \
  "$output_dir/agent-setup.md"
