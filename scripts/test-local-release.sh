#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd -P)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
"$repo_root/scripts/render-local-release.sh" \
  --app-image "ghcr.io/dzarlax-ai/personal-memory@$digest" \
  --tei-amd64-image "ghcr.io/huggingface/text-embeddings-inference@$digest" \
  --tei-arm64-image "ghcr.io/dzarlax-ai/personal-memory-tei-arm64@$digest" \
  --output "$scratch/valid"

test -s "$scratch/valid/SHA256SUMS"
grep -q 'platform: linux/amd64' "$scratch/valid/compose.local-amd64.yaml"
grep -q 'platform: linux/arm64' "$scratch/valid/compose.local-arm64.yaml"

cp -R "$scratch/valid" "$scratch/unsafe"
sed -i.bak 's/127\.0\.0\.1:8000:8000/0.0.0.0:8000:8000/' "$scratch/unsafe/compose.local-amd64.yaml"
rm "$scratch/unsafe/compose.local-amd64.yaml.bak"
if "$repo_root/scripts/verify-local-release.sh" "$scratch/unsafe" >/dev/null 2>&1; then
  echo "validator accepted a non-loopback bind" >&2
  exit 1
fi

cp -R "$scratch/valid" "$scratch/moving-tag"
sed -i.bak 's|qdrant/qdrant@sha256:f1c7272cdac52b38c1a0e89313922d940ba50afd90d593a1605dbbc214e66ffb|qdrant/qdrant:latest|' "$scratch/moving-tag/compose.local-amd64.yaml"
rm "$scratch/moving-tag/compose.local-amd64.yaml.bak"
if "$repo_root/scripts/verify-local-release.sh" "$scratch/moving-tag" >/dev/null 2>&1; then
  echo "validator accepted a moving image tag" >&2
  exit 1
fi

cp -R "$scratch/valid" "$scratch/optional-feature"
sed -i.bak 's/ENABLE_RAG: "false"/ENABLE_RAG: "true"/' "$scratch/optional-feature/compose.local-amd64.yaml"
rm "$scratch/optional-feature/compose.local-amd64.yaml.bak"
if "$repo_root/scripts/verify-local-release.sh" "$scratch/optional-feature" >/dev/null 2>&1; then
  echo "validator accepted an enabled optional feature" >&2
  exit 1
fi

declare -a critical_environment_mutations=(
  'MCP_PORT: "8000"|MCP_PORT: "9000"'
  'QDRANT_URL: http://memory-qdrant:6333|QDRANT_URL: http://memory-qdrant:7333'
  'EMBED_URL: http://memory-embeddings:80|EMBED_URL: http://memory-embeddings:81'
  'ADOPT_EXISTING_EMBEDDING_IDENTITY: "false"|ADOPT_EXISTING_EMBEDDING_IDENTITY: "true"'
)
for index in "${!critical_environment_mutations[@]}"; do
  mutation=${critical_environment_mutations[$index]}
  before=${mutation%%|*}
  after=${mutation#*|}
  candidate="$scratch/critical-environment-$index"
  cp -R "$scratch/valid" "$candidate"
  sed -i.bak "s|$before|$after|" "$candidate/compose.local-amd64.yaml"
  rm "$candidate/compose.local-amd64.yaml.bak"
  if "$repo_root/scripts/verify-local-release.sh" "$candidate" >/dev/null 2>&1; then
    echo "validator accepted unsafe critical environment mutation: $before" >&2
    exit 1
  fi
done

cp -R "$scratch/valid" "$scratch/bind-mount"
sed -i.bak 's|tei_cache:/data|/tmp:/data|' "$scratch/bind-mount/compose.local-amd64.yaml"
rm "$scratch/bind-mount/compose.local-amd64.yaml.bak"
if "$repo_root/scripts/verify-local-release.sh" "$scratch/bind-mount" >/dev/null 2>&1; then
  echo "validator accepted a bind mount" >&2
  exit 1
fi

echo "local release validation tests passed"
