#!/bin/sh
# Install the FrameWorks CLI binary.
#
# Usage:
#   curl -sSfL https://github.com/Livepeer-FrameWorks/monorepo/releases/latest/download/install.sh | sh
#
# Environment variables:
#   FRAMEWORKS_VERSION     - Version to install (default: latest)
#   FRAMEWORKS_INSTALL_DIR - Installation directory (default: /usr/local/bin)
#   FRAMEWORKS_REPO        - GitHub repo (default: Livepeer-FrameWorks/monorepo)

set -eu

REPO="${FRAMEWORKS_REPO:-Livepeer-FrameWorks/monorepo}"
VERSION="${FRAMEWORKS_VERSION:-latest}"
INSTALL_DIR="${FRAMEWORKS_INSTALL_DIR:-/usr/local/bin}"

log() { printf '%s\n' "$@"; }
err() { printf 'error: %s\n' "$@" >&2; exit 1; }

detect_os() {
  case "$(uname -s)" in
    Linux*)  echo "linux" ;;
    Darwin*) echo "darwin" ;;
    *)       err "Unsupported OS: $(uname -s). Only Linux and macOS are supported." ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)   echo "amd64" ;;
    aarch64|arm64)   echo "arm64" ;;
    *)               err "Unsupported architecture: $(uname -m). Only amd64 and arm64 are supported." ;;
  esac
}

sha256_check() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    return 1
  fi
}

resolve_latest() {
  url="https://api.github.com/repos/${REPO}/releases/latest"
  if command -v curl >/dev/null 2>&1; then
    tag=$(curl -sSfL "$url" | grep '"tag_name"' | sed 's/.*"tag_name": *"//;s/".*//')
  elif command -v wget >/dev/null 2>&1; then
    tag=$(wget -qO- "$url" | grep '"tag_name"' | sed 's/.*"tag_name": *"//;s/".*//')
  else
    err "curl or wget is required"
  fi
  [ -z "$tag" ] && err "Failed to resolve latest version from GitHub"
  echo "$tag"
}

download() {
  if command -v curl >/dev/null 2>&1; then
    curl -sSfL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    err "curl or wget is required"
  fi
}

main() {
  os=$(detect_os)
  arch=$(detect_arch)

  if [ "$VERSION" = "latest" ]; then
    log "Resolving latest version..."
    VERSION=$(resolve_latest)
  fi

  binary="frameworks-${os}-${arch}"
  base_url="https://github.com/${REPO}/releases/download/${VERSION}"

  log "Installing frameworks ${VERSION} (${os}/${arch})..."

  tmpdir=$(mktemp -d)
  trap 'rm -rf "$tmpdir"' EXIT

  log "Downloading ${binary}..."
  download "${base_url}/${binary}" "${tmpdir}/frameworks" || err "Download failed. Check that version ${VERSION} exists."

  # Verify checksum if available
  if download "${base_url}/${binary}.sha256" "${tmpdir}/frameworks.sha256" 2>/dev/null; then
    expected=$(awk '{print $1}' "${tmpdir}/frameworks.sha256")
    actual=$(sha256_check "${tmpdir}/frameworks") || {
      log "Warning: no sha256sum/shasum available, skipping checksum verification"
      expected=""
    }
    if [ -n "$expected" ] && [ "$expected" != "$actual" ]; then
      err "Checksum mismatch (expected ${expected}, got ${actual})"
    fi
    [ -n "$expected" ] && log "Checksum verified."
  else
    log "No checksum file available, skipping verification."
  fi

  chmod +x "${tmpdir}/frameworks"

  # Install to target directory
  if [ -w "$INSTALL_DIR" ]; then
    mv "${tmpdir}/frameworks" "${INSTALL_DIR}/frameworks"
  else
    log "Elevated permissions required to install to ${INSTALL_DIR}"
    sudo mv "${tmpdir}/frameworks" "${INSTALL_DIR}/frameworks"
  fi

  # Verify
  if "${INSTALL_DIR}/frameworks" version >/dev/null 2>&1; then
    log ""
    "${INSTALL_DIR}/frameworks" version
    log ""
    log "Installed to ${INSTALL_DIR}/frameworks"
  else
    err "Installation verification failed"
  fi

  # Initialize configuration
  "${INSTALL_DIR}/frameworks" config init 2>/dev/null || true

  # Check for common dependencies
  for dep in ssh git; do
    if ! command -v "$dep" >/dev/null 2>&1; then
      log "Warning: ${dep} not found (required for cluster provisioning)"
    fi
  done
}

main
