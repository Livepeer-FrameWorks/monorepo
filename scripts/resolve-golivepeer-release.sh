#!/usr/bin/env bash
# Resolves the latest published go-livepeer release (Livepeer-FrameWorks/go-livepeer)
# into <out-file> and prints its tag.
#
#   scripts/resolve-golivepeer-release.sh <out-file>
#
# A platform release bakes the go-livepeer release this resolves when its tag is
# pushed (.github/workflows/release.yml), and the dev stack (scripts/stack/up.sh)
# runs the same one, so the stack runs the go-livepeer the next release ships.
#
# GH_TOKEN, when set, authenticates the GitHub API call; without it the public API
# is used.
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "usage: $0 <out-file>" >&2
  exit 2
fi
out_file=$1
mkdir -p "$(dirname "$out_file")"

auth=()
if [ -n "${GH_TOKEN:-}" ]; then
  auth=(-H "Authorization: Bearer ${GH_TOKEN}")
fi

curl -fsSL --retry 3 \
  -H "Accept: application/vnd.github+json" \
  ${auth[@]+"${auth[@]}"} \
  -H "X-GitHub-Api-Version: 2022-11-28" \
  https://api.github.com/repos/Livepeer-FrameWorks/go-livepeer/releases/latest \
  >"$out_file"

jq -re '.tag_name | select(test("^v[0-9]+\\.[0-9]+\\.[0-9]+"))' "$out_file" || {
  echo "ERROR: the latest go-livepeer release has no vX.Y.Z tag" >&2
  exit 1
}
