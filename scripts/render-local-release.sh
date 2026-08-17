#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 --app-image IMAGE@sha256:DIGEST --tei-amd64-image IMAGE@sha256:DIGEST --tei-arm64-image IMAGE@sha256:DIGEST --output DIR" >&2
}

app_image=""
tei_amd64_image=""
tei_arm64_image=""
output_dir=""

while (($#)); do
  case "$1" in
    --app-image) app_image=${2:-}; shift 2 ;;
    --tei-amd64-image) tei_amd64_image=${2:-}; shift 2 ;;
    --tei-arm64-image) tei_arm64_image=${2:-}; shift 2 ;;
    --output) output_dir=${2:-}; shift 2 ;;
    *) usage; exit 2 ;;
  esac
done

digest_ref='^[a-z0-9./_-]+@sha256:[a-f0-9]{64}$'
for value in "$app_image" "$tei_amd64_image" "$tei_arm64_image"; do
  if [[ ! $value =~ $digest_ref ]]; then
    echo "all image references must use an immutable sha256 digest" >&2
    exit 2
  fi
done
if [[ -z $output_dir ]]; then
  usage
  exit 2
fi

repo_root=$(cd "$(dirname "$0")/.." && pwd -P)
mkdir -p "$output_dir"

render() {
  local architecture=$1
  local tei_image=$2
  local template="$repo_root/deploy/local/compose.local-${architecture}.yaml.tmpl"
  local target="$output_dir/compose.local-${architecture}.yaml"

  sed \
    -e "s|@@APP_IMAGE@@|$app_image|g" \
    -e "s|@@TEI_IMAGE@@|$tei_image|g" \
    "$template" >"$target"
}

render amd64 "$tei_amd64_image"
render arm64 "$tei_arm64_image"

"$repo_root/scripts/verify-local-release.sh" "$output_dir"
(
  cd "$output_dir"
  shasum -a 256 compose.local-amd64.yaml compose.local-arm64.yaml >SHA256SUMS
)
