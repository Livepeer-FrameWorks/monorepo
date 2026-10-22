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
    TEST_BLOCKED_REASON='no STACK_TEST_TENANT_WALLET_KEY in secrets/staging.env'
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
  [ "$rc" = 2 ] && [[ "$out" == BLOCKED*"no STACK_TEST_TENANT_WALLET_KEY"* ]]
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

# The sweep covers what test tenant scenarios created: a second pass over the
# test tenant's own ledger with its own token, without repeating the doctor.
mkdir -p "$work/sweeprepo/scripts/stack"
cat >"$work/sweeprepo/scripts/stack/sweep.sh" <<'EOF'
#!/usr/bin/env bash
printf '%s|%s|%s|%s|%s|%s\n' "$STACK_TENANT_ID" "$STACK_API_TOKEN" "$STACK_STATE_DIR" "${STACK_SWEEP_DOCTOR:-1}" "$1" "$2" >>"$FIXTURE_DIR/sweeps"
EOF
sweep_as_test_tenant() { # sweep_as_test_tenant <test token> <ledger present 0|1>: the sweeps it ran
  (
    export STACK_STATE_DIR="$work/sweepstate" STACK_TENANT_ID=operator-tenant STACK_API_TOKEN=operator-token \
      STACK_REPO="$work/sweeprepo" FIXTURE_DIR="$work"
    rm -rf "$STACK_STATE_DIR" "$work/sweeps"
    mkdir -p "$STACK_STATE_DIR/test-tenant"
    [ "$2" = 1 ] && echo '{"kind":"created"}' >"$STACK_STATE_DIR/test-tenant/sweep-ledger.jsonl"
    # shellcheck source=scripts/stack/test-tenant.sh
    . "$here/test-tenant.sh" || exit 1
    TEST_TENANT_ID=test-tenant TEST_API_TOKEN=$1
    test_tenant_sweep 1700000000 "$work/windows.tsv"
    cat "$work/sweeps" 2>/dev/null
  )
}
sweeps_test_ledger() {
  [ "$(sweep_as_test_tenant test-token 1)" = "test-tenant|test-token|$work/sweepstate/test-tenant|0|1700000000|$work/windows.tsv" ]
}
expect "the sweep checks the test tenant's ledger with its token, without a second doctor" sweeps_test_ledger
no_test_sweep_without_ledger() { [ -z "$(sweep_as_test_tenant test-token 0)" ]; }
expect "no test tenant sweep when no test tenant scenario created anything" no_test_sweep_without_ledger

key_is_named_for_its_role() {
  grep -q 'STACK_TEST_TENANT_WALLET_KEY' "$here/test-tenant.sh" && ! grep -q 'STACK_PRICED_TENANT_WALLET_KEY' "$here/test-tenant.sh"
}
expect "the wallet key is read as STACK_TEST_TENANT_WALLET_KEY" key_is_named_for_its_role

# Credentials never outlive the run in its evidence directory.
credentials_purged() {
  local d="$work/evidence"
  rm -rf "$d"
  mkdir -p "$d/test-tenant" "$d/target"
  for f in session-04.jwt operator.jar login.abc123 billing-cookies test-tenant/session-22.jwt test-tenant/session.jar \
    test-tenant/wallet.key test-tenant/walletsign target/operator.env cli-restart.env; do echo secret >"$d/$f"; done
  for f in sweep-ledger.jsonl cleanup.log target/target.env test-tenant/sweep-ledger.jsonl; do echo evidence >"$d/$f"; done
  (
    # shellcheck source=scripts/stack/run-credentials.sh
    . "$here/run-credentials.sh" && purge_run_credentials "$d"
  )
  [ -z "$(find "$d" -type f -exec grep -l secret {} +)" ] && [ "$(find "$d" -type f | wc -l | tr -d ' ')" = 4 ]
}
expect "a run's credential files are removed and its evidence kept" credentials_purged
purge_installed_before_first_credential() {
  # The purge trap is set before the operator login writes its jar, so a run
  # that stops at login (BLOCKED) leaves no credential behind either.
  local trap_line jar_line
  trap_line=$(grep -n "trap 'purge_run_credentials" "$here/verify-target.sh" | head -1 | cut -d: -f1)
  jar_line=$(grep -n '^code=\$(operator_login' "$here/verify-target.sh" | head -1 | cut -d: -f1)
  [ -n "$trap_line" ] && [ -n "$jar_line" ] && [ "$trap_line" -lt "$jar_line" ] &&
    grep -q 'purge_run_credentials "$STACK_STATE_DIR"' "$here/verify-target.sh" &&
    grep -q 'purge_run_credentials "$stale_dir"' "$here/verify-target.sh"
}
expect "verify-target purges credentials on every exit and a killed run's leftovers at start" purge_installed_before_first_credential

[ "$failures" = 0 ] && echo "test tenant checks: ok"
exit "$failures"
