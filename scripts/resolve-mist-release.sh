#!/usr/bin/env bash
# Resolves the latest published MistServer release (Livepeer-FrameWorks/mistserver)
# into <out-dir>/mistserver-release.json and <out-dir>/mistserver-release-index.json.
#
#   scripts/resolve-mist-release.sh <out-dir>
#
# A platform release bakes the Mist release this resolves when its tag is pushed
# (.github/workflows/release.yml), and the dev stack edge (scripts/edge-dev-dist.sh)
# stages the same one, so the stack runs the Mist the next release ships.
#
# GH_TOKEN, when set, authenticates the GitHub API calls; without it the public
# API and the public index download are used.
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "usage: $0 <out-dir>" >&2
  exit 2
fi
out_dir=$1
mkdir -p "$out_dir"
release_json="$out_dir/mistserver-release.json"
index_json="$out_dir/mistserver-release-index.json"

auth=()
if [ -n "${GH_TOKEN:-}" ]; then
  auth=(-H "Authorization: Bearer ${GH_TOKEN}")
fi

curl -fsSL --retry 3 \
  -H "Accept: application/vnd.github+json" \
  ${auth[@]+"${auth[@]}"} \
  -H "X-GitHub-Api-Version: 2022-11-28" \
  https://api.github.com/repos/Livepeer-FrameWorks/mistserver/releases/latest \
  >"$release_json"
tag=$(jq -re '.tag_name' "$release_json")

if [ ${#auth[@]} -gt 0 ]; then
  index_url=$(jq -re '.assets[] | select(.name == "mistserver-release-index.json") | .url' "$release_json")
  curl -fsSL --retry 3 \
    -H "Accept: application/octet-stream" \
    ${auth[@]+"${auth[@]}"} \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "$index_url" >"$index_json"
else
  index_url=$(jq -re '.assets[] | select(.name == "mistserver-release-index.json") | .browser_download_url' "$release_json")
  curl -fsSL --retry 3 "$index_url" >"$index_json"
fi

jq -e --arg tag "$tag" '
  .schema == "mistserver.release/v1"
  and .release_tag == $tag
  and .default_profile == "cpu"
  and (.source_revision | type == "string" and length > 0)
' "$index_json" >/dev/null || {
  echo "ERROR: MistServer $tag release index is not a mistserver.release/v1 index for that tag" >&2
  exit 1
}

# Every artifact the index names must be an asset of the release with the same
# GitHub-computed digest, so its checksum is the one consumers verify against.
jq -e --slurpfile release "$release_json" '
  [.profiles[].platforms[].artifact]
  | all(. as $artifact
      | $release[0].assets
      | any(.name == $artifact.name and .digest == $artifact.checksum))
' "$index_json" >/dev/null || {
  echo "ERROR: MistServer $tag release index names artifacts that are not release assets with matching digests" >&2
  exit 1
}

echo "$tag"
