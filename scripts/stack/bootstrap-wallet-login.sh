#!/usr/bin/env bash
# Headless wallet sign-in of a bootstrap-declared account user, run by up.sh
# inside the stack runner. scripts/stack/bootstrap.yaml links the address of
# the well-known test key 0x…01 to qa@stack.test; signing in with that key must
# return that user in its declared tenant, not a new wallet-only account.
#
#   bootstrap-wallet-login.sh <expected email> <expected tenant id>
set -uo pipefail

expected_email=$1 expected_tenant=$2
: "${BRIDGE_URL:=http://bridge:18000}"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

if ! (cd "$(dirname "$0")/walletsign" && go build -o "$work/walletsign" .) >"$work/build.log" 2>&1; then
  echo "walletsign build failed: $(tail -5 "$work/build.log")" >&2
  exit 1
fi
printf '%064x' 1 >"$work/wallet.key"
signed=$(BRIDGE_URL=$BRIDGE_URL STACK_WALLET_SIGNER="$work/walletsign" STACK_WALLET_KEY_FILE="$work/wallet.key" \
  bash "$(dirname "$0")/wallet-login.sh" body) || exit 1
code=$(curl -s -m 30 -o "$work/login.json" -w '%{http_code}' "$BRIDGE_URL/auth/wallet-login" \
  -H 'Content-Type: application/json' --data "$(jq -c '.body' <<<"$signed")")
email=$(jq -r '.user.email // empty' "$work/login.json" 2>/dev/null)
tenant=$(jq -r '.user.tenant_id // empty' "$work/login.json" 2>/dev/null)
if [ "$code" != 200 ] || [ "$email" != "$expected_email" ] || [ "$tenant" != "$expected_tenant" ]; then
  echo "wallet sign-in answered HTTP $code as ${email:-no email} in tenant ${tenant:-none}; want $expected_email in $expected_tenant" >&2
  exit 1
fi
echo "wallet sign-in resolved to $email in tenant $tenant"
