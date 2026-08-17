#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 --release vX.Y.Z FILE" >&2
}

release=""
file=""
while (($#)); do
  case "$1" in
    --release) release=${2:-}; shift 2 ;;
    *)
      if [[ -n $file ]]; then usage; exit 2; fi
      file=$1
      shift
      ;;
  esac
done

if [[ ! $release =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || [[ ! -f $file ]]; then
  usage
  exit 2
fi

repo_root=$(cd "$(dirname "$0")/.." && pwd -P)
source "$repo_root/install/agent-setup-cli-versions.env"

required_literals=(
  "Bound release: \`$release\`"
  "http://127.0.0.1:8000/memory"
  "codex mcp add personal-memory --url"
  "claude mcp add --transport http personal-memory --scope user"
  "quick-install codex"
  "quick-verify codex"
  "get_stats"
  "docker compose down -v"
  '"release_name": "compose.local-arm64.yaml"'
  '"local_name": "compose.yaml"'
  "reconcile existing project containers"
  "$CODEX_CLI_VERSION"
  "$CLAUDE_CODE_VERSION"
)
for literal in "${required_literals[@]}"; do
  grep -Fq -- "$literal" "$file" || {
    echo "agent setup is missing required content: $literal" >&2
    exit 1
  }
done

required_states=(
  assets_verified
  dependencies_started
  waiting_for_embeddings
  embeddings_ready
  service_ready
  awaiting_reconnect
  embeddings_failed
  complete
)
for state in "${required_states[@]}"; do
  grep -Fq -- "$state" "$file" || {
    echo "agent setup is missing checkpoint state: $state" >&2
    exit 1
  }
done

if grep -Eq '@(RELEASE_VERSION|CODEX_CLI_VERSION|CLAUDE_CODE_VERSION)@|https://github\.com/[^[:space:]]+/releases/latest' "$file"; then
  echo "agent setup contains an unresolved or moving release reference" >&2
  exit 1
fi

download_prefix="https://github.com/Dzarlax-AI/personal_memory/releases/download/$release/"
grep -Fq -- "$download_prefix" "$file" || {
  echo "agent setup does not bind downloads to $release" >&2
  exit 1
}

if grep -Eo 'https://github\.com/Dzarlax-AI/personal_memory/releases/download/v[0-9]+\.[0-9]+\.[0-9]+/' "$file" |
  grep -Fvx "$download_prefix" >/dev/null; then
  echo "agent setup mixes release download identities" >&2
  exit 1
fi

documentation_prefix="https://github.com/Dzarlax-AI/personal_memory/blob/$release/"
if grep -Eo 'https://github\.com/Dzarlax-AI/personal_memory/blob/[^/]+/' "$file" |
  grep -Fvx "$documentation_prefix" >/dev/null; then
  echo "agent setup links to documentation outside $release" >&2
  exit 1
fi
