#!/usr/bin/env bash
# Stages component artifacts into edge/dist/ for the edge image build.
#
# The image never downloads anything itself; this script resolves, verifies
# (sha256) and lays out:
#   dist/versions.env            HELMSMAN_VERSION / MIST_VERSION / CADDY_VERSION [/ CONFIG_SCHEMA_VERSION]
#   dist/helmsman/helmsman       helmsman binary
#   dist/mistserver/{bin,lib}    MistServer native tree
#   dist/caddy/caddy             caddy binary
#
# CI passes pre-downloaded tarballs with --*-tar; local dev can build
# helmsman from source and pull mist/caddy by URL.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: stage-dist.sh [options]
  --arch <amd64|arm64>              target arch (default: host arch)
  --helmsman-tar <file>             helmsman release tarball
  --helmsman-from-source            build helmsman from the monorepo working tree
  --helmsman-version <v>            required with --helmsman-tar (derived for from-source)
  --mist-tar <file>                 MistServer native tarball (bin/ lib/)
  --mist-url <url> --mist-sha256 <hex>
  --mist-version <v>
  --caddy-tar <file>                caddy release tarball
  --caddy-url <url> --caddy-sha256 <hex>
  --caddy-version <v>
  --config-schema-version <v>       optional
  --allow-unverified                permit URL downloads without a checksum (local dev only)
EOF
  exit 1
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DIST="${SCRIPT_DIR}/dist"

ARCH="$(uname -m)"
case "${ARCH}" in
  x86_64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
esac

HELMSMAN_TAR="" HELMSMAN_FROM_SOURCE=0 HELMSMAN_VERSION=""
MIST_TAR="" MIST_URL="" MIST_SHA256="" MIST_VERSION=""
CADDY_TAR="" CADDY_URL="" CADDY_SHA256="" CADDY_VERSION=""
CONFIG_SCHEMA_VERSION=""
ALLOW_UNVERIFIED=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --arch) ARCH="$2"; shift 2 ;;
    --helmsman-tar) HELMSMAN_TAR="$2"; shift 2 ;;
    --helmsman-from-source) HELMSMAN_FROM_SOURCE=1; shift ;;
    --helmsman-version) HELMSMAN_VERSION="$2"; shift 2 ;;
    --mist-tar) MIST_TAR="$2"; shift 2 ;;
    --mist-url) MIST_URL="$2"; shift 2 ;;
    --mist-sha256) MIST_SHA256="$2"; shift 2 ;;
    --mist-version) MIST_VERSION="$2"; shift 2 ;;
    --caddy-tar) CADDY_TAR="$2"; shift 2 ;;
    --caddy-url) CADDY_URL="$2"; shift 2 ;;
    --caddy-sha256) CADDY_SHA256="$2"; shift 2 ;;
    --caddy-version) CADDY_VERSION="$2"; shift 2 ;;
    --config-schema-version) CONFIG_SCHEMA_VERSION="$2"; shift 2 ;;
    --allow-unverified) ALLOW_UNVERIFIED=1; shift ;;
    -h | --help | *) usage ;;
  esac
done

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

fetch() { # url checksum out — checksum is bare hex or "sha256:<hex>"/"sha512:<hex>"
  local url="$1" sum="$2" out="$3" algo=sha256
  echo "==> fetching ${url}"
  curl -fsSL -o "${out}" "${url}"
  if [[ -z "${sum}" ]]; then
    if [[ "${ALLOW_UNVERIFIED}" != 1 ]]; then
      echo "ERROR: no checksum given for ${url}; baked artifacts must be pinned (or pass --allow-unverified for local dev)" >&2
      exit 1
    fi
    echo "    WARNING: skipping checksum verification for ${url} (--allow-unverified)" >&2
    return 0
  fi
  case "${sum}" in
    sha256:*) algo=sha256; sum="${sum#sha256:}" ;;
    sha512:*) algo=sha512; sum="${sum#sha512:}" ;;
  esac
  echo "${sum}  ${out}" | "${algo}sum" -c - >/dev/null
}

rm -rf "${DIST}"
mkdir -p "${DIST}/helmsman" "${DIST}/mistserver" "${DIST}/caddy"

# --- helmsman ---------------------------------------------------------------
if [[ "${HELMSMAN_FROM_SOURCE}" == 1 ]]; then
  echo "==> building helmsman from source (linux/${ARCH})"
  HELMSMAN_VERSION="${HELMSMAN_VERSION:-$(git -C "${REPO_ROOT}" describe --tags --always --dirty)}"
  (cd "${REPO_ROOT}/api_sidecar" && \
    CGO_ENABLED=0 GOOS=linux GOARCH="${ARCH}" go build -tags=nomsgpack \
      -ldflags "-X github.com/Livepeer-FrameWorks/monorepo/pkg/version.Version=${HELMSMAN_VERSION} \
                -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.ComponentName=helmsman \
                -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.ComponentVersion=${HELMSMAN_VERSION}" \
      -o "${DIST}/helmsman/helmsman" ./cmd/helmsman)
elif [[ -n "${HELMSMAN_TAR}" ]]; then
  [[ -n "${HELMSMAN_VERSION}" ]] || { echo "--helmsman-version required with --helmsman-tar" >&2; exit 1; }
  tar -xzf "${HELMSMAN_TAR}" -C "${TMP}"
  HELMSMAN_BIN="$(find "${TMP}" -maxdepth 2 -type f \( -name helmsman -o -name 'frameworks-helmsman-*' \) | head -1)"
  [[ -n "${HELMSMAN_BIN}" ]] || { echo "no helmsman binary in ${HELMSMAN_TAR}" >&2; exit 1; }
  install -m 0755 "${HELMSMAN_BIN}" "${DIST}/helmsman/helmsman"
else
  echo "need --helmsman-tar or --helmsman-from-source" >&2; exit 1
fi

# --- mistserver ---------------------------------------------------------------
if [[ -z "${MIST_TAR}" && -n "${MIST_URL}" ]]; then
  MIST_TAR="${TMP}/mistserver.tar.gz"
  fetch "${MIST_URL}" "${MIST_SHA256}" "${MIST_TAR}"
fi
[[ -n "${MIST_TAR}" ]] || { echo "need --mist-tar or --mist-url" >&2; exit 1; }
[[ -n "${MIST_VERSION}" ]] || { echo "--mist-version required" >&2; exit 1; }
tar -xzf "${MIST_TAR}" -C "${DIST}/mistserver"
if [[ ! -x "${DIST}/mistserver/bin/MistController" ]]; then
  # Tolerate a single top-level wrapper directory in the tarball.
  WRAPPER="$(find "${DIST}/mistserver" -maxdepth 2 -type f -name MistController -path '*/bin/*' | head -1)"
  [[ -n "${WRAPPER}" ]] || { echo "MistController missing from ${MIST_TAR}" >&2; exit 1; }
  WRAPPER_DIR="$(dirname "$(dirname "${WRAPPER}")")"
  mv "${WRAPPER_DIR}"/* "${DIST}/mistserver/"
  find "${DIST}/mistserver" -maxdepth 1 -type d -empty -delete
fi

# --- caddy ---------------------------------------------------------------
if [[ -z "${CADDY_TAR}" && -n "${CADDY_URL}" ]]; then
  CADDY_TAR="${TMP}/caddy.tar.gz"
  fetch "${CADDY_URL}" "${CADDY_SHA256}" "${CADDY_TAR}"
fi
[[ -n "${CADDY_TAR}" ]] || { echo "need --caddy-tar or --caddy-url" >&2; exit 1; }
[[ -n "${CADDY_VERSION}" ]] || { echo "--caddy-version required" >&2; exit 1; }
tar -xzf "${CADDY_TAR}" -C "${TMP}/"
CADDY_BIN="$(find "${TMP}" -maxdepth 2 -type f -name caddy | head -1)"
[[ -n "${CADDY_BIN}" ]] || { echo "no caddy binary in ${CADDY_TAR}" >&2; exit 1; }
install -m 0755 "${CADDY_BIN}" "${DIST}/caddy/caddy"

# --- versions ---------------------------------------------------------------
{
  echo "HELMSMAN_VERSION=${HELMSMAN_VERSION}"
  echo "MIST_VERSION=${MIST_VERSION}"
  echo "CADDY_VERSION=${CADDY_VERSION}"
  [[ -n "${CONFIG_SCHEMA_VERSION}" ]] && echo "CONFIG_SCHEMA_VERSION=${CONFIG_SCHEMA_VERSION}"
} > "${DIST}/versions.env"

echo "==> staged edge dist for linux/${ARCH}:"
cat "${DIST}/versions.env"
