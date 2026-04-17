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

extract_asset() {
  asset_name="$1"
  asset_path="$2"
  out_dir="$3"

  case "$asset_name" in
    *.tar.gz)
      tar -xzf "$asset_path" -C "$out_dir"
      ;;
    *.zip)
      if command -v unzip >/dev/null 2>&1; then
        unzip -q "$asset_path" -d "$out_dir"
      elif command -v ditto >/dev/null 2>&1; then
        ditto -x -k "$asset_path" "$out_dir"
      else
        err "A zip extractor is required to install macOS release assets"
      fi
      ;;
    *)
      cp "$asset_path" "$out_dir/frameworks"
      ;;
  esac
}

resolve_asset_name() {
  os="$1"
  arch="$2"
  version="$3"
  version_no_v=$(printf '%s' "$version" | sed 's/^v//')

  case "$os" in
    darwin)
      printf '%s\n' "frameworks-cli-v${version_no_v}-${os}-${arch}.zip"
      ;;
    *)
      printf '%s\n' "frameworks-cli-v${version_no_v}-${os}-${arch}.tar.gz"
      ;;
  esac
}

find_extracted_binary() {
  out_dir="$1"

  if [ -f "$out_dir/frameworks" ]; then
    printf '%s\n' "$out_dir/frameworks"
    return 0
  fi

  found=$(find "$out_dir" -maxdepth 2 -type f \( -name frameworks -o -name 'frameworks-*' \) | head -n 1)
  [ -n "$found" ] || return 1
  printf '%s\n' "$found"
}

main() {
  os=$(detect_os)
  arch=$(detect_arch)

  if [ "$VERSION" = "latest" ]; then
    log "Resolving latest version..."
    VERSION=$(resolve_latest)
  fi

  base_url="https://github.com/${REPO}/releases/download/${VERSION}"

  log "Installing frameworks ${VERSION} (${os}/${arch})..."

  tmpdir=$(mktemp -d)
  trap 'rm -rf "$tmpdir"' EXIT

  asset_name=$(resolve_asset_name "$os" "$arch" "$VERSION")
  log "Downloading ${asset_name}..."
  download "${base_url}/${asset_name}" "${tmpdir}/asset" || err "Download failed. Check that version ${VERSION} exists."

  # Verify checksum if available
  if download "${base_url}/${asset_name}.sha256" "${tmpdir}/asset.sha256" 2>/dev/null; then
    expected=$(awk '{print $1}' "${tmpdir}/asset.sha256")
    actual=$(sha256_check "${tmpdir}/asset") || {
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

  mkdir -p "${tmpdir}/extract"
  extract_asset "$asset_name" "${tmpdir}/asset" "${tmpdir}/extract"
  binary_path=$(find_extracted_binary "${tmpdir}/extract") || err "Installed asset did not contain a frameworks binary"
  chmod +x "$binary_path"

  # Install to target directory
  if [ -w "$INSTALL_DIR" ]; then
    mv "$binary_path" "${INSTALL_DIR}/frameworks"
  else
    log "Elevated permissions required to install to ${INSTALL_DIR}"
    sudo mv "$binary_path" "${INSTALL_DIR}/frameworks"
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
