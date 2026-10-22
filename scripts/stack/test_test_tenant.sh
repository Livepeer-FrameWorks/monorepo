#!/usr/bin/env bash
# Unit checks for scripts/stack/test-tenant.sh, wallet-login.sh and the session
# path they give lib.sh, with stub scenarios, a stub signer and a stubbed curl,
# so they need no deployment: a scenario declared "tenant: test" runs as the
# wallet test tenant in its own state directory, every other scenario as the
# run's tenant, and a test tenant session a sign-out ended is signed in again
# with the wallet instead of a password.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/fw-stack-test-tenant-test.XXXXXX")
trap 'rm -rf "$work"' EXIT
failures=0
expect() { # expect <description> <command...>
  local what=$1
  shift
  if "$@"; then printf 'ok    %s\n' "$what"; else printf 'FAIL  %s\n' "$what"; failures=$((failures + 1)); fi
}

cat >"$work/test.sh" <<'EOF'
#!/usr/bin/env bash
# staging: safe
# tenant: test
printf '%s|%s|%s|%s\n' "$STACK_TENANT_ID" "$STACK_API_TOKEN" "${STACK_SESSION_JAR:-}" "$STACK_STATE_DIR"
EOF
cat >"$work/plain.sh" <<'EOF'
#!/usr/bin/env bash
# staging: safe
printf '%s|%s|%s|%s\n' "$STACK_TENANT_ID" "$STACK_API_TOKEN" "${STACK_SESSION_JAR:-}" "$STACK_STATE_DIR"
EOF

run_as() { # run_as <scenario> [test token]: the tenant, token, session jar and state dir it ran with
  (
    export STACK_STATE_DIR="$work/state" STACK_TENANT_ID=operator-tenant STACK_API_TOKEN=operator-token
    mkdir -p "$STACK_STATE_DIR"
    # shellcheck source=scripts/stack/test-tenant.sh
    . "$here/test-tenant.sh" || exit 1
    TEST_TENANT_ID=test-tenant TEST_API_TOKEN=${2-test-token}
    TEST_BLOCKED_REASON='no STACK_PRICED_TENANT_WALLET_KEY in secrets/staging.env'
    run_scenario_as_its_tenant "$work/$1"
  )
}
test_runs_as_test_tenant() {
  [ "$(run_as test.sh)" = "test-tenant|test-token|$work/state/test-tenant/session.jar|$work/state/test-tenant" ]
}
expect "a scenario declared tenant: test runs as the test tenant in its own state directory" test_runs_as_test_tenant
plain_runs_as_run_tenant() { [ "$(run_as plain.sh)" = "operator-tenant|operator-token||$work/state" ]; }
expect "any other scenario runs as the run's tenant" plain_runs_as_run_tenant
unavailable_blocks() {
  local out rc
  out=$(run_as test.sh '')
  rc=$?
  [ "$rc" = 2 ] && [[ "$out" == BLOCKED*"no STACK_PRICED_TENANT_WALLET_KEY"* ]]
}
expect "without the test tenant its scenarios are BLOCKED, never run as the run's tenant" unavailable_blocks

# Stubs: a signer that answers for any key, and a Bridge whose refresh refuses
# a signed-out jar, whose wallet challenge and sign-in work, and whose password
# login is recorded so the checks can see it was never attempted.
mkdir -p "$work/bin"
cat >"$work/bin/walletsign" <<'EOF'
#!/usr/bin/env bash
[ -n "$STACK_WALLET_KEY" ] || exit 1
case "$1" in address) echo 0xabc ;; sign) echo "sig-of-${2%%$'\n'*}" ;; esac
EOF
cat >"$work/bin/curl" <<'EOF'
#!/usr/bin/env bash
jar='' prev=''
for a in "$@"; do
  [ "$prev" = -c ] && jar=$a
  prev=$a
done
case " $* " in
  *"/auth/refresh"*) printf 401 ;;
  *"/auth/wallet-challenge"*) echo '{"message":"bridge wants you to sign in"}' ;;
  *"/auth/wallet-login"*) printf 'bridge\tFALSE\t/\tFALSE\t0\taccess_token\twallet-jwt\n' >"$jar"; printf 200 ;;
  *"/auth/login"*) echo login >>"$FIXTURE_DIR/logins"; printf 401 ;;
esac
EOF
chmod +x "$work/bin/walletsign" "$work/bin/curl"
printf 'deadbeef' >"$work/wallet.key"

signed_out_session_signs_in_again() {
  (
    export PATH="$work/bin:$PATH" FIXTURE_DIR="$work" STACK_STATE_DIR="$work/libstate" BRIDGE_URL=http://bridge.test \
      STACK_SESSION_JAR="$work/session.jar" STACK_WALLET_SIGNER="$work/bin/walletsign" STACK_WALLET_KEY_FILE="$work/wallet.key" \
      STACK_SCENARIO=signout STACK_TARGET=staging STACK_ADAPTER_FILE=/dev/null
    mkdir -p "$STACK_STATE_DIR"
    : >"$STACK_SESSION_JAR"
    # shellcheck source=scripts/stack/lib.sh
    . "$here/lib.sh" >/dev/null 2>&1
    [ "$(session_token)" = wallet-jwt ] && [ ! -e "$work/logins" ]
  )
}
expect "a test tenant session a sign-out ended is signed in again with the wallet, not a password" signed_out_session_signs_in_again

body_is_fresh_wallet_login() {
  local out
  out=$(PATH="$work/bin:$PATH" BRIDGE_URL=http://bridge.test STACK_WALLET_SIGNER="$work/bin/walletsign" \
    STACK_WALLET_KEY_FILE="$work/wallet.key" bash "$here/wallet-login.sh" body)
  [ "$(jq -r '.path' <<<"$out")" = /auth/wallet-login ] &&
    [ "$(jq -r '.body.signature' <<<"$out")" = "sig-of-bridge wants you to sign in" ] &&
    [ "$(jq -r '.body.address' <<<"$out")" = 0xabc ]
}
expect "wallet-login.sh body signs a fresh challenge for the sign-out probe" body_is_fresh_wallet_login

[ "$failures" = 0 ] && echo "test tenant checks: ok"
exit "$failures"
