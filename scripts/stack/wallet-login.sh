#!/usr/bin/env bash
# Wallet sign-in for the deployed-target run's test tenant (test-tenant.sh). A
# wallet tenant has no password, and each sign-in needs a fresh challenge, so
# every caller that would post a password login asks this script instead.
#
#   wallet-login.sh body        prints {"path": "/auth/wallet-login", "body": {...}}
#                               signed for a fresh challenge
#   wallet-login.sh jar <file>  signs in and writes the session's cookies to <file>
#
# Needs BRIDGE_URL, STACK_WALLET_SIGNER (scripts/stack/walletsign built) and
# STACK_WALLET_KEY_FILE (the 0600 key file the runner wrote).
set -uo pipefail

[ -x "${STACK_WALLET_SIGNER:-}" ] && [ -s "${STACK_WALLET_KEY_FILE:-}" ] || {
  echo 'wallet-login: STACK_WALLET_SIGNER and STACK_WALLET_KEY_FILE are required' >&2
  exit 2
}
signed_body() {
  local key address message signature
  key=$(cat "$STACK_WALLET_KEY_FILE")
  address=$(STACK_WALLET_KEY=$key "$STACK_WALLET_SIGNER" address) || return 1
  message=$(curl -s -m 30 "$BRIDGE_URL/auth/wallet-challenge" -H 'Content-Type: application/json' \
    --data "$(jq -cn --arg a "$address" '{address:$a, chain_id:8453}')" | jq -r '.message // empty')
  [ -n "$message" ] || { echo 'wallet-login: no wallet challenge' >&2; return 1; }
  signature=$(STACK_WALLET_KEY=$key "$STACK_WALLET_SIGNER" sign "$message") || return 1
  jq -cn --arg a "$address" --arg m "$message" --arg s "$signature" '{address:$a, message:$m, signature:$s}'
}

case "${1:-}" in
  body)
    body=$(signed_body) || exit 1
    jq -cn --argjson b "$body" '{path:"/auth/wallet-login", body:$b}'
    ;;
  jar)
    [ -n "${2:-}" ] || { echo 'usage: wallet-login.sh jar <file>' >&2; exit 2; }
    body=$(signed_body) || exit 1
    code=$(curl -s -m 30 -c "$2" -o /dev/null -w '%{http_code}' "$BRIDGE_URL/auth/wallet-login" \
      -H 'Content-Type: application/json' --data "$body")
    [ "$code" = 200 ] || { echo "wallet-login: sign-in answered HTTP $code" >&2; exit 1; }
    ;;
  *)
    echo 'usage: wallet-login.sh body | jar <file>' >&2
    exit 2
    ;;
esac
