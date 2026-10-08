#!/bin/sh
# Isolated storage/protocol acceptance; embeddings are synthetic, no model keys.
set -eu
cd "$(dirname "$0")/.."
scratch=$(mktemp -d "${TMPDIR:-/tmp}/memory-release-probe.XXXXXX")
name="personal-memory-release-probe-$$"
started=false
cleanup() {
  if [ "$started" = true ]; then docker stop "$name" >/dev/null 2>&1 || true; fi
  rm -rf "$scratch"
}
trap cleanup EXIT HUP INT TERM
# Existing pinned Qdrant image only; this command performs no image download.
image=sha256:f1c7272cdac52b38c1a0e89313922d940ba50afd90d593a1605dbbc214e66ffb
docker image inspect "$image" >/dev/null
mkdir -p eval-results/optional-ai-memory-v2/release-preparation
go build -o "$scratch/server" ./cmd/server
go build -o "$scratch/client" scripts/probes/optional-ai-memory-client.go
docker run --rm -d --name "$name" -p 127.0.0.1::6333 "$image" >/dev/null
started=true
port=$(docker port "$name" 6333/tcp | sed 's/.*://')
PROBE_SERVER_BINARY="$scratch/server" PROBE_QDRANT_URL="http://127.0.0.1:$port" "$scratch/client"
