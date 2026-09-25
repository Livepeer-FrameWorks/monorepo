#!/usr/bin/env bash
# Stages edge/dist for the dev compose edge image (frameworks-edge:dev) through
# edge/stage-dist.sh: Helmsman built from this working tree, MistServer and Caddy
# from pinned release tarballs verified by checksum.
#
#   make edge-dev-dist && docker compose build edge
#
# EDGE_DEV_MIST_TAR=<tar.gz> stages a local MistServer install tree (bin/, lib/,
# share/) instead of the release, for example the tree
# scripts/verify-mist-current-source.sh exports with MIST_INSTALL_TREE_OUT.
#
# Every run stamps Helmsman (and a local Mist tree) with a unique version, so the
# container's seed step reinstalls them over the copies on the edge_opt volume.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# MistServer release pinned for the dev edge (Livepeer-FrameWorks/mistserver).
mist_version=v0.3.10
mist_sha256_amd64=95a71d797d31e87ac9b2e1061cbade9cb059667b1029e4b42e61a127c7097d0a
mist_sha256_arm64=2ff1a7f5ae6aa04831d7a7e6416ae1009a19cff1f8695b6cb73df9c94a27adb4

# The image targets the Docker engine's architecture, which can differ from the host shell's.
arch="$(docker version --format '{{.Server.Arch}}' 2>/dev/null || uname -m)"
case "$arch" in
  amd64 | x86_64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "ERROR: unsupported architecture $arch" >&2; exit 1 ;;
esac

# Caddy comes from the platform infrastructure manifest, the pin release images use.
read -r caddy_version caddy_url caddy_checksum < <(awk -v want="linux-$arch" '
  /^  - name: / { sel = ($3 == "caddy") }
  sel && /^    version:/ { v = $2; gsub(/"/, "", v) }
  sel && /- arch:/ { a = $3 }
  sel && /^        url:/ && a == want { u = $2 }
  sel && /^        checksum:/ && a == want { c = $2 }
  END { print v, u, c }
' "$repo_root/config/infrastructure.yaml")
if [ -z "$caddy_version" ] || [ -z "$caddy_url" ] || [ -z "$caddy_checksum" ]; then
  echo "ERROR: no caddy linux-$arch artifact in config/infrastructure.yaml" >&2
  exit 1
fi

stamp="$(date -u +%Y%m%d%H%M%S)"
helmsman_version="$(git -C "$repo_root" describe --tags --always --dirty)-dev.$stamp"

mist_args=()
if [ -n "${EDGE_DEV_MIST_TAR:-}" ]; then
  [ -f "$EDGE_DEV_MIST_TAR" ] || { echo "ERROR: EDGE_DEV_MIST_TAR=$EDGE_DEV_MIST_TAR is not a file" >&2; exit 1; }
  mist_args=(--mist-tar "$EDGE_DEV_MIST_TAR" --mist-version "local-$stamp")
else
  case "$arch" in
    amd64) mist_sha256=$mist_sha256_amd64 ;;
    arm64) mist_sha256=$mist_sha256_arm64 ;;
  esac
  mist_args=(
    --mist-url "https://github.com/Livepeer-FrameWorks/mistserver/releases/download/$mist_version/mistserver-linux-$arch-$mist_version.tar.gz"
    --mist-sha256 "$mist_sha256"
    --mist-version "$mist_version"
  )
fi

"$repo_root/edge/stage-dist.sh" \
  --arch "$arch" \
  --helmsman-from-source \
  --helmsman-version "$helmsman_version" \
  "${mist_args[@]}" \
  --caddy-url "$caddy_url" \
  --caddy-sha256 "$caddy_checksum" \
  --caddy-version "$caddy_version"
