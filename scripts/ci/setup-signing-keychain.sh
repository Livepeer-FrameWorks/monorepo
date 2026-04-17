#!/bin/bash
set -euo pipefail

: "${RUNNER_TEMP:?RUNNER_TEMP is required}"
: "${APPLE_CERTIFICATE_P12:?APPLE_CERTIFICATE_P12 is required}"
: "${APPLE_CERTIFICATE_PASSWORD:?APPLE_CERTIFICATE_PASSWORD is required}"

if [[ -n "${APPLE_INSTALLER_P12:-}" && -z "${APPLE_INSTALLER_PASSWORD:-}" ]]; then
  echo "APPLE_INSTALLER_PASSWORD is required when APPLE_INSTALLER_P12 is set" >&2
  exit 1
fi

KEYCHAIN_PATH="${RUNNER_TEMP}/signing.keychain-db"
KEYCHAIN_PASSWORD="$(openssl rand -base64 32)"

decode_base64() {
  if base64 --decode < /dev/null > /dev/null 2>&1; then
    base64 --decode
  elif base64 -d < /dev/null > /dev/null 2>&1; then
    base64 -d
  else
    base64 -D
  fi
}

import_certificate() {
  local encoded_cert="$1"
  local cert_password="$2"
  shift 2

  local cert_path
  cert_path="$(mktemp)"

  printf '%s' "$encoded_cert" | decode_base64 > "$cert_path"
  security import "$cert_path" -k "$KEYCHAIN_PATH" -P "$cert_password" "$@"
  rm -f "$cert_path"
}

security create-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN_PATH"
security set-keychain-settings "$KEYCHAIN_PATH"
security unlock-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN_PATH"

import_certificate \
  "$APPLE_CERTIFICATE_P12" \
  "$APPLE_CERTIFICATE_PASSWORD" \
  -T /usr/bin/codesign \
  -T /usr/bin/xcodebuild

if [[ -n "${APPLE_INSTALLER_P12:-}" ]]; then
  import_certificate \
    "$APPLE_INSTALLER_P12" \
    "$APPLE_INSTALLER_PASSWORD" \
    -T /usr/bin/productsign
fi

security set-key-partition-list -S apple-tool:,apple: -s -k "$KEYCHAIN_PASSWORD" "$KEYCHAIN_PATH"

existing_keychains=()
while IFS= read -r keychain; do
  keychain="${keychain//\"/}"
  if [[ -n "$keychain" && "$keychain" != "$KEYCHAIN_PATH" ]]; then
    existing_keychains+=("$keychain")
  fi
done < <(security list-keychains -d user)

security list-keychains -d user -s "$KEYCHAIN_PATH" "${existing_keychains[@]}"
security find-identity -v "$KEYCHAIN_PATH"
