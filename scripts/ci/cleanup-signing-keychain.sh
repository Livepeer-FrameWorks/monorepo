#!/bin/bash
set -euo pipefail

: "${RUNNER_TEMP:?RUNNER_TEMP is required}"

KEYCHAIN_PATH="${RUNNER_TEMP}/signing.keychain-db"

if security list-keychains -d user | grep -Fq "\"${KEYCHAIN_PATH}\""; then
  security delete-keychain "$KEYCHAIN_PATH"
  echo "Signing keychain deleted."
elif [[ -f "$KEYCHAIN_PATH" ]]; then
  rm -f "$KEYCHAIN_PATH"
  echo "Signing keychain file removed."
else
  echo "Signing keychain not found."
fi
