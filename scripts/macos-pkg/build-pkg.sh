#!/bin/bash
set -euo pipefail

# Build a signed and notarized .pkg installer for FrameWorks (CLI + tray app).
#
# Usage: ./build-pkg.sh <version> <cli-binary> [tray-app-bundle]
#
# Environment variables (required for signing):
#   APPLE_INSTALLER_ID  — "Developer ID Installer: ..." identity
#   APPLE_DEVELOPER_ID  — "Developer ID Application: ..." identity (for binary signing)
#   APPLE_ID            — Apple ID email for notarization
#   APPLE_TEAM_ID       — Apple Developer Team ID
#   APPLE_APP_SPECIFIC_PASSWORD — app-specific password for notarytool

VERSION="${1:?Usage: build-pkg.sh <version> <cli-binary> [tray-app-bundle]}"
CLI_BIN="${2:?Missing CLI binary path}"
TRAY_APP="${3:-}"

if [ -n "${CI:-}" ]; then
  : "${APPLE_DEVELOPER_ID:?Required in CI}"
  : "${APPLE_INSTALLER_ID:?Required in CI}"
  : "${APPLE_ID:?Required in CI}"
  : "${APPLE_TEAM_ID:?Required in CI}"
  : "${APPLE_APP_SPECIFIC_PASSWORD:?Required in CI}"
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WORK_DIR="$(mktemp -d)"
PKG_ROOT="${WORK_DIR}/root"
PKG_SCRIPTS="${WORK_DIR}/scripts"
OUTPUT_DIR="${SCRIPT_DIR}/../../dist/pkg"

echo "Building FrameWorks .pkg v${VERSION}"
echo "  CLI:      ${CLI_BIN}"
[ -n "${TRAY_APP}" ] && echo "  Tray app: ${TRAY_APP}"
echo "  Work dir: ${WORK_DIR}"

# -- Layout the package root --
mkdir -p "${PKG_ROOT}/usr/local/bin"

# CLI binary
cp "${CLI_BIN}" "${PKG_ROOT}/usr/local/bin/frameworks"
chmod 755 "${PKG_ROOT}/usr/local/bin/frameworks"

# Sign CLI binary
if [ -n "${APPLE_DEVELOPER_ID:-}" ]; then
  echo "Signing CLI binary..."
  codesign --sign "${APPLE_DEVELOPER_ID}" --timestamp --options runtime --force \
    "${PKG_ROOT}/usr/local/bin/frameworks"
fi

# Tray app (optional — bundled when the release workflow provides it)
if [ -n "${TRAY_APP}" ] && [ -d "${TRAY_APP}" ]; then
  mkdir -p "${PKG_ROOT}/Applications"
  cp -R "${TRAY_APP}" "${PKG_ROOT}/Applications/FrameWorks.app"
  # App bundle should already be signed by xcodebuild; verify
  if [ -n "${APPLE_DEVELOPER_ID:-}" ]; then
    codesign --verify --verbose=2 "${PKG_ROOT}/Applications/FrameWorks.app"
  fi
fi

# Uninstaller
cp "${SCRIPT_DIR}/uninstall.sh" "${PKG_ROOT}/usr/local/bin/frameworks-uninstall"
chmod 755 "${PKG_ROOT}/usr/local/bin/frameworks-uninstall"

# -- Pre/post install scripts --
mkdir -p "${PKG_SCRIPTS}"
cp "${SCRIPT_DIR}/preinstall.sh" "${PKG_SCRIPTS}/preinstall"
cp "${SCRIPT_DIR}/postinstall.sh" "${PKG_SCRIPTS}/postinstall"
chmod 755 "${PKG_SCRIPTS}/preinstall" "${PKG_SCRIPTS}/postinstall"

# -- Build component package --
pkgbuild \
  --root "${PKG_ROOT}" \
  --scripts "${PKG_SCRIPTS}" \
  --identifier "com.livepeer.frameworks" \
  --version "${VERSION}" \
  --install-location "/" \
  "${WORK_DIR}/frameworks-component.pkg"

# -- Build product package with Distribution.xml --
mkdir -p "${OUTPUT_DIR}"

productbuild \
  --distribution "${SCRIPT_DIR}/Distribution.xml" \
  --resources "${SCRIPT_DIR}" \
  --package-path "${WORK_DIR}" \
  --version "${VERSION}" \
  "${WORK_DIR}/frameworks-${VERSION}-unsigned.pkg"

# -- Sign the .pkg --
if [ -n "${APPLE_INSTALLER_ID:-}" ]; then
  echo "Signing .pkg with: ${APPLE_INSTALLER_ID}"
  productsign \
    --sign "${APPLE_INSTALLER_ID}" \
    "${WORK_DIR}/frameworks-${VERSION}-unsigned.pkg" \
    "${OUTPUT_DIR}/frameworks-${VERSION}.pkg"
else
  echo "Warning: APPLE_INSTALLER_ID not set, .pkg will be unsigned"
  cp "${WORK_DIR}/frameworks-${VERSION}-unsigned.pkg" \
    "${OUTPUT_DIR}/frameworks-${VERSION}.pkg"
fi

# -- Notarize --
if [ -n "${APPLE_ID:-}" ] && [ -n "${APPLE_APP_SPECIFIC_PASSWORD:-}" ]; then
  echo "Notarizing .pkg..."
  xcrun notarytool submit "${OUTPUT_DIR}/frameworks-${VERSION}.pkg" \
    --apple-id "${APPLE_ID}" \
    --team-id "${APPLE_TEAM_ID}" \
    --password "${APPLE_APP_SPECIFIC_PASSWORD}" \
    --wait

  echo "Stapling notarization ticket..."
  xcrun stapler staple "${OUTPUT_DIR}/frameworks-${VERSION}.pkg"
fi

# -- Checksums --
cd "${OUTPUT_DIR}"
shasum -a 256 "frameworks-${VERSION}.pkg" > "frameworks-${VERSION}.pkg.sha256"

echo ""
echo "Package built: ${OUTPUT_DIR}/frameworks-${VERSION}.pkg"
echo "SHA256: $(cat "frameworks-${VERSION}.pkg.sha256")"

# Cleanup
rm -rf "${WORK_DIR}"
