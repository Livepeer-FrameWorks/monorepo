#!/usr/bin/env bash
# The deployed-target run's test tenant (sourced by verify-target.sh).
# Scenarios declared "# tenant: test" run as this tenant, every other scenario
# as the run's own (the operator's) tenant:
# - it is on a priced tier, for scenarios that measure what a tier charges (22);
# - its sign-outs end only its own sessions, never a person's operator session
#   (37 signs out every session of the account it uses).
# The tenant was created once by wallet sign-in (no email or password) and put
# on the developer tier with a zero-fee, usage-waived operator grant; its key
# lives in the cluster's SOPS env as STACK_PRICED_TENANT_WALLET_KEY. A test
# tenant scenario keeps its own state directory, so its sweep ledger and
# cleanup use the test tenant's token.

TEST_TENANT_ID=${TEST_TENANT_ID:-}
TEST_API_TOKEN=${TEST_API_TOKEN:-}
TEST_TOKEN_ID=${TEST_TOKEN_ID:-}
TEST_BLOCKED_REASON=${TEST_BLOCKED_REASON:-}

test_tenant_dir() { printf '%s/test-tenant' "$STACK_STATE_DIR"; }

scenario_needs_test_tenant() { grep -qx '# tenant: test' "$1"; }

# test_tenant_session_jwt: a live test tenant access token. Scenario 37 signs
# out every session of the test tenant, so a refused refresh signs in again.
test_tenant_session_jwt() {
  local dir
  dir=$(test_tenant_dir)
  refresh_session_token "$dir/session.jar" 2>/dev/null && return 0
  STACK_WALLET_SIGNER="$dir/walletsign" STACK_WALLET_KEY_FILE="$dir/wallet.key" \
    bash "$STACK_REPO/scripts/stack/wallet-login.sh" jar "$dir/session.jar" || return 1
  awk '$6 == "access_token" { print $7 }' "$dir/session.jar" | tail -1
}

# test_tenant_login <secrets env file> <cluster>: sign in as the test tenant and
# mint the run's token for it. On failure it records why in
# TEST_BLOCKED_REASON; test tenant scenarios then report BLOCKED.
test_tenant_login() {
  local env_file=$1 cluster=$2 dir code created jwt
  dir=$(test_tenant_dir)
  mkdir -p "$dir" && chmod 700 "$dir"
  (umask 077 && sops --decrypt "$env_file" 2>/dev/null | sed -n 's/^STACK_PRICED_TENANT_WALLET_KEY=//p' >"$dir/wallet.key")
  if [ ! -s "$dir/wallet.key" ]; then
    TEST_BLOCKED_REASON="no STACK_PRICED_TENANT_WALLET_KEY in $env_file"
    return 1
  fi
  if ! (cd "$STACK_REPO/scripts/stack/walletsign" && go build -o "$dir/walletsign" .) >"$dir/walletsign-build.log" 2>&1; then
    TEST_BLOCKED_REASON="walletsign build failed: $dir/walletsign-build.log"
    return 1
  fi
  if ! STACK_WALLET_SIGNER="$dir/walletsign" STACK_WALLET_KEY_FILE="$dir/wallet.key" \
    bash "$STACK_REPO/scripts/stack/wallet-login.sh" jar "$dir/session.jar"; then
    TEST_BLOCKED_REASON="test tenant wallet sign-in failed"
    return 1
  fi
  jwt=$(awk '$6 == "access_token" { print $7 }' "$dir/session.jar" | tail -1)
  TEST_TENANT_ID=$(awk '$6 == "tenant_id" { print $7 }' "$dir/session.jar" | tail -1)
  # Any active test tenant token is a killed run's: verify-target.sh has already
  # refused to start while another run's token is in use.
  gql '{developerTokensConnection(page:{first:200}){nodes{id tokenName status}}}' '{}' "$jwt" |
    jq -r --arg prefix "stack-$cluster-test-" '.data.developerTokensConnection.nodes[]?
      | select(.status == "active" and (.tokenName | startswith($prefix))) | .id' |
    while IFS= read -r stale; do
      echo "recovering a killed run: revoking test tenant token $stale $(gql 'mutation($id:ID!){revokeDeveloperToken(id:$id){__typename}}' \
        "$(jq -cn --arg id "$stale" '{id:$id}')" "$jwt" | jq -r '.data.revokeDeveloperToken.__typename // .errors[0].message // "?"')"
    done
  created=$(gql 'mutation($i:CreateDeveloperTokenInput!){createDeveloperToken(input:$i){__typename ... on DeveloperToken{id tokenValue} ... on ValidationError{message} ... on AuthError{message}}}' \
    "$(jq -cn --arg n "stack-$cluster-test-$(date -u +%Y%m%dT%H%M%SZ)" \
      '{i:{name:$n, expiresIn:1, permissions:"streams:read,streams:write,analytics:read,developer:read,developer:write,account:read,billing:read,infrastructure:read,placement:read"}}')" \
    "$jwt")
  TEST_TOKEN_ID=$(jq -r '.data.createDeveloperToken.id // empty' <<<"$created")
  TEST_API_TOKEN=$(jq -r '.data.createDeveloperToken.tokenValue // empty' <<<"$created")
  if [ -z "$TEST_TENANT_ID" ] || [ -z "$TEST_TOKEN_ID" ] || [ -z "$TEST_API_TOKEN" ]; then
    TEST_BLOCKED_REASON="createDeveloperToken for the test tenant failed: $(jq -c 'del(.data.createDeveloperToken.tokenValue)' <<<"$created" 2>/dev/null)"
    TEST_API_TOKEN=''
    return 1
  fi
  echo "test tenant run API token $TEST_TOKEN_ID for tenant $TEST_TENANT_ID"
  STACK_STATE_DIR="$dir" cleanup_run_objects "$TEST_API_TOKEN" | sed 's/^cleanup:/recovering a killed run:/'
}

# run_scenario_as_its_tenant <path>: runs the scenario as the tenant it declares.
run_scenario_as_its_tenant() {
  local dir
  if ! scenario_needs_test_tenant "$1"; then
    bash "$1"
    return
  fi
  if [ -z "$TEST_API_TOKEN" ]; then
    printf 'BLOCKED  %s: the test tenant is unavailable: %s\n' "$(basename "$1" .sh)" "${TEST_BLOCKED_REASON:-not signed in}"
    return 2
  fi
  dir=$(test_tenant_dir)
  STACK_TENANT_ID=$TEST_TENANT_ID STACK_API_TOKEN=$TEST_API_TOKEN STACK_SESSION_JAR="$dir/session.jar" \
    STACK_WALLET_SIGNER="$dir/walletsign" STACK_WALLET_KEY_FILE="$dir/wallet.key" STACK_STATE_DIR="$dir" bash "$1"
}

# test_tenant_cleanup: deletes what test tenant scenarios created and revokes
# the test tenant's token, confirming the revocation as the run token's is.
test_tenant_cleanup() {
  local dir jwt code
  dir=$(test_tenant_dir)
  if [ -n "$TEST_TOKEN_ID" ]; then
    STACK_STATE_DIR="$dir" cleanup_run_objects "$TEST_API_TOKEN" | tee "$dir/cleanup.log"
    jwt=$(test_tenant_session_jwt) || jwt=''
    gql 'mutation($id:ID!){revokeDeveloperToken(id:$id){__typename}}' "$(jq -cn --arg id "$TEST_TOKEN_ID" '{id:$id}')" "$jwt" >/dev/null
    code=$(curl -s -m 15 -o /dev/null -w '%{http_code}' "$BRIDGE_GRAPHQL_URL" -H "Authorization: Bearer $TEST_API_TOKEN" \
      -H 'Content-Type: application/json' --data '{"query":"{streamsConnection(page:{first:1}){totalCount}}"}')
    if [ "$code" = 401 ]; then
      echo "test tenant run API token $TEST_TOKEN_ID revoked (HTTP 401 after revocation)"
    else
      echo "WARN: test tenant run API token $TEST_TOKEN_ID still answers HTTP $code after revocation" >&2
    fi
  fi
  rm -f "$dir/session.jar" "$dir/walletsign" "$dir/wallet.key"
}
