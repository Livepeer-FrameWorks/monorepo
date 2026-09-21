#!/usr/bin/env bash
set -euo pipefail

: "${MIST_SOURCE_DIR:?Set MIST_SOURCE_DIR to a clean local Mist checkout}"
: "${MIST_CONTRACT_BUILD_IMAGE:?Set MIST_CONTRACT_BUILD_IMAGE to a cached Linux image with Meson, dependencies and ffmpeg}"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_root="$(cd "$MIST_SOURCE_DIR" && pwd)"
if [[ ! -f "$source_root/meson.build" || ! -f "$source_root/src/output/output.cpp" ]]; then
  echo "ERROR: source directory is not a Mist checkout" >&2
  exit 1
fi
if [[ -n "$(git -C "$source_root" status --porcelain)" ]]; then
  echo "ERROR: source-contract export requires a clean Mist checkout" >&2
  exit 1
fi
revision="$(git -C "$source_root" rev-parse HEAD)"
builder_id="$(docker image inspect --format '{{.Id}}' "$MIST_CONTRACT_BUILD_IMAGE")"
if [[ "$builder_id" != sha256:* ]]; then
  echo "ERROR: builder must resolve to a cached immutable image ID" >&2
  exit 1
fi
task_dir="$(mktemp -d "${TMPDIR:-/tmp}/fw-mist-source-contract.XXXXXX")"
nonce="${task_dir##*.}"
builder_tag="fw-mist-contract-base:${nonce}"
result_tag="fw-mist-source-contract:${revision:0:12}-${nonce}"
mkdir "$task_dir/source"
git -C "$source_root" archive "$revision" | tar -xf - -C "$task_dir/source"
cp "$repo_root/pkg/mist/testdata/Dockerfile.source-contract" "$task_dir/Dockerfile"

# Build only the immutable export, never the sibling checkout or its build trees.
# Retain the export and images for diagnosis; nothing is published or deployed.
echo "Mist source-contract export: $task_dir"
echo "Mist source revision: $revision; cached builder: $builder_id"
docker tag "$builder_id" "$builder_tag"
docker build --pull=false --network=none --progress=plain \
  --build-arg "MIST_CONTRACT_BUILD_IMAGE=$builder_tag" \
  --build-arg "MIST_SOURCE_REVISION=$revision" \
  --build-arg "MIST_BUILD_ID=$builder_id" \
  --tag "$result_tag" "$task_dir"
result_id="$(docker image inspect --format '{{.Id}}' "$result_tag")"
actual_revision="$(docker image inspect --format '{{index .Config.Labels "org.frameworks.mist.source-revision"}}' "$result_id")"
if [[ "$actual_revision" != "$revision" ]]; then
  echo "ERROR: rebuilt image lost its source revision" >&2
  exit 1
fi
echo "Mist source-contract image: $result_id ($result_tag)"

# MIST_INSTALL_TREE_OUT=<file.tar.gz> also writes the build's install prefix as a
# MistServer tree (bin/, lib/, share/) for make edge-dev-dist EDGE_DEV_MIST_TAR=<file>.
# lib/ carries the shared libraries the binaries load from outside the C runtime, so
# the tree runs on the edge image when the builder's glibc is not newer than Debian
# bookworm's.
if [[ -n "${MIST_INSTALL_TREE_OUT:-}" ]]; then
  docker run --rm -i --network=none --entrypoint sh "$result_id" -s > "$MIST_INSTALL_TREE_OUT" <<'SH'
set -eu
stage=$(mktemp -d)
mkdir -p "$stage/bin" "$stage/lib" "$stage/share"
cp -a /usr/local/bin/. "$stage/bin/"
if [ -d /usr/local/lib ]; then cp -a /usr/local/lib/. "$stage/lib/"; fi
if [ -d /usr/local/share ]; then cp -a /usr/local/share/. "$stage/share/"; fi
for bin in "$stage"/bin/*; do ldd "$bin" 2>/dev/null || true; done |
  awk '$2 == "=>" && $3 ~ /^\// { print $3 }' | sort -u |
  while read -r lib; do
    name=${lib##*/}
    case "$name" in
      libc.so.* | libm.so.* | libdl.so.* | libpthread.so.* | librt.so.* | ld-linux*) continue ;;
    esac
    [ -e "$stage/lib/$name" ] || cp -L "$lib" "$stage/lib/$name"
  done
tar -C "$stage" -czf - .
SH
  echo "Mist install tree: $MIST_INSTALL_TREE_OUT"
fi

MIST_CONTRACT_IMAGE="$result_id" make -C "$repo_root" verify-mist-viewer-credentials
