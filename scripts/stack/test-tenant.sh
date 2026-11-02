#!/usr/bin/env bash
# The deployed-target run's declared tenants (sourced by verify-target.sh).
# A scenario's "# tenant:" line names the tenant it runs as; a scenario without
# one runs as the run's own (the operator's) tenant. Both tenants are declared
# in the cluster's gitops bootstrap.yaml, each keeps its own state directory,
# and its sweep ledger and cleanup use its own token.
#
# "# tenant: qa" is the QA customer (alias frameworks-qa), for customer flows:
# a normal tenant, not a platform operator, on the tier and operator grant its
# purser.customer_billing row declares. It signs in with its email and the
# QA_ACCOUNT_PASSWORD in the cluster's SOPS env.
#
# "# tenant: test" is the wallet test tenant (alias frameworks-stack-test):
# - it is on a priced tier, for scenarios that measure what a tier charges (22);
# - its sign-outs end only its own sessions, never a person's operator session
#   (37 signs out every session of the account it uses).
# It is on the developer tier with a zero-fee, usage-waived operator grant and
# signs in with the wallet whose key is STACK_TEST_TENANT_WALLET_KEY in the
# cluster's SOPS env.

QA_TENANT_ALIAS=frameworks-qa
TEST_TENANT_ID=${TEST_TENANT_ID:-}
TEST_API_TOKEN=${TEST_API_TOKEN:-}
TEST_TOKEN_ID=${TEST_TOKEN_ID:-}
TEST_BLOCKED_REASON=${TEST_BLOCKED_REASON:-}

test_tenant_dir() { printf '%s/test-tenant' "$STACK_STATE_DIR"; }

# scenario_tenant <path>: the tenant a scenario declares (qa, test or empty).
scenario_tenant() { sed -n 's/^# tenant: //p' "$1" | head -1; }
scenario_needs_test_tenant() { [ "$(scenario_tenant "$1")" = test ]; }
scenario_needs_qa_tenant() { [ "$(scenario_tenant "$1")" = qa ]; }

RUN_TOKEN_ID='' RUN_TOKEN_VALUE='' RUN_TOKEN_ERROR=''

# mint_run_token <session jwt> <token name prefix> <who> [busy seconds]: the
# run's API token for the tenant the jwt signs in to. A killed run leaves an
# active token named with the prefix and webhook endpoints described "stack
# scenarios"; those are revoked and deleted first. With busy seconds, a token
# used within that window belongs to a run still in progress, so nothing is
# changed and it returns 3. Sets RUN_TOKEN_ID and RUN_TOKEN_VALUE, or
# RUN_TOKEN_ERROR.
mint_run_token() {
  local jwt=$1 prefix=$2 who=$3 busy=${4:-} stale created id name idle
  RUN_TOKEN_ID='' RUN_TOKEN_VALUE='' RUN_TOKEN_ERROR=''
  stale=$(gql '{developerTokensConnection(page:{first:200}){nodes{id tokenName status lastUsedAt}}}' '{}' "$jwt" |
    jq -r --arg prefix "$prefix" --argjson now "$(date +%s)" '.data.developerTokensConnection.nodes[]?
      | select(.status == "active" and (.tokenName | startswith($prefix)))
      | [.id, .tokenName, (if .lastUsedAt then ($now - (.lastUsedAt | sub("\\.[0-9]+"; "") | fromdateiso8601)) else 999999 end)] | @tsv')
  if [ -n "$busy" ]; then
    while IFS=$'\t' read -r id name idle; do
      [ -n "$id" ] && [ "${idle%.*}" -lt "$busy" ] || continue
      RUN_TOKEN_ERROR="another run is in progress (its token $name was used ${idle%.*}s ago)"
      return 3
    done <<<"$stale"
  fi
  while IFS=$'\t' read -r id name idle; do
    [ -n "$id" ] || continue
    echo "recovering a killed run: revoking $who token $name $(gql 'mutation($id:ID!){revokeDeveloperToken(id:$id){__typename}}' \
      "$(jq -cn --arg id "$id" '{id:$id}')" "$jwt" | jq -r '.data.revokeDeveloperToken.__typename // .errors[0].message // "?"')"
  done <<<"$stale"
  gql '{webhookEndpointsConnection(page:{first:200}){nodes{id description}}}' '{}' "$jwt" |
    jq -r '.data.webhookEndpointsConnection.nodes[]? | select(.description == "stack scenarios") | .id' |
    while IFS= read -r id; do
      echo "recovering a killed run: deleting $who webhook endpoint $id $(gql 'mutation($id:ID!){deleteWebhookEndpoint(id:$id){__typename}}' \
        "$(jq -cn --arg id "$id" '{id:$id}')" "$jwt" | jq -r '.data.deleteWebhookEndpoint.__typename // .errors[0].message // "?"')"
    done
  created=$(gql 'mutation($i:CreateDeveloperTokenInput!){createDeveloperToken(input:$i){__typename ... on DeveloperToken{id tokenValue} ... on ValidationError{message} ... on AuthError{message}}}' \
    "$(jq -cn --arg n "$prefix$(date -u +%Y%m%dT%H%M%SZ)" \
      '{i:{name:$n, expiresIn:1, permissions:"streams:read,streams:write,analytics:read,developer:read,developer:write,account:read,billing:read,infrastructure:read,placement:read"}}')" \
    "$jwt")
  RUN_TOKEN_ID=$(jq -r '.data.createDeveloperToken.id // empty' <<<"$created")
  RUN_TOKEN_VALUE=$(jq -r '.data.createDeveloperToken.tokenValue // empty' <<<"$created")
  if [ -z "$RUN_TOKEN_ID" ] || [ -z "$RUN_TOKEN_VALUE" ]; then
    RUN_TOKEN_ERROR="createDeveloperToken for the $who failed: $(jq -c 'del(.data.createDeveloperToken.tokenValue)' <<<"$created" 2>/dev/null)"
    RUN_TOKEN_ID='' RUN_TOKEN_VALUE=''
    return 1
  fi
}

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
  local env_file=$1 cluster=$2 dir jwt
  dir=$(test_tenant_dir)
  mkdir -p "$dir" && chmod 700 "$dir"
  (umask 077 && sops --decrypt "$env_file" 2>/dev/null | sed -n 's/^STACK_TEST_TENANT_WALLET_KEY=//p' >"$dir/wallet.key")
  if [ ! -s "$dir/wallet.key" ]; then
    TEST_BLOCKED_REASON="no STACK_TEST_TENANT_WALLET_KEY in $env_file"
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
  # verify-target.sh has already refused to start while another run's token is
  # in use, so any active test tenant token is a killed run's.
  if ! mint_run_token "$jwt" "stack-$cluster-test-" "test tenant" || [ -z "$TEST_TENANT_ID" ]; then
    TEST_BLOCKED_REASON=${RUN_TOKEN_ERROR:-"the test tenant session names no tenant"}
    return 1
  fi
  TEST_TOKEN_ID=$RUN_TOKEN_ID TEST_API_TOKEN=$RUN_TOKEN_VALUE
  echo "test tenant run API token $TEST_TOKEN_ID for tenant $TEST_TENANT_ID"
  STACK_STATE_DIR="$dir" cleanup_run_objects "$TEST_API_TOKEN" | sed 's/^cleanup:/recovering a killed run:/'
}

# run_scenario_as_its_tenant <path>: runs the scenario as the tenant it declares.
run_scenario_as_its_tenant() {
  local dir
  case "$(scenario_tenant "$1")" in
    test) ;;
    qa)
      if [ -z "$QA_API_TOKEN" ]; then
        printf 'BLOCKED  %s: the QA tenant is unavailable: %s\n' "$(basename "$1" .sh)" "${QA_BLOCKED_REASON:-not signed in}"
        return 2
      fi
      dir=$(qa_tenant_dir)
      STACK_TENANT_ID=$QA_TENANT_ID STACK_API_TOKEN=$QA_API_TOKEN STACK_DEMO_EMAIL=$QA_EMAIL STACK_DEMO_PASSWORD=$QA_PASSWORD \
        STACK_STATE_DIR="$dir" bash "$1"
      return
      ;;
    *)
      bash "$1"
      return
      ;;
  esac
  if [ -z "$TEST_API_TOKEN" ]; then
    printf 'BLOCKED  %s: the test tenant is unavailable: %s\n' "$(basename "$1" .sh)" "${TEST_BLOCKED_REASON:-not signed in}"
    return 2
  fi
  dir=$(test_tenant_dir)
  STACK_TENANT_ID=$TEST_TENANT_ID STACK_API_TOKEN=$TEST_API_TOKEN STACK_SESSION_JAR="$dir/session.jar" \
    STACK_WALLET_SIGNER="$dir/walletsign" STACK_WALLET_KEY_FILE="$dir/wallet.key" STACK_STATE_DIR="$dir" bash "$1"
}

# test_tenant_sweep <since epoch> <windows file>: the sweep's entity checks over
# what test tenant scenarios created, read with the test tenant's own token
# from its own ledger. The run's sweep already ran the doctor, so this pass
# skips it. Nothing to check when no test tenant scenario created anything.
test_tenant_sweep() {
  local dir
  dir=$(test_tenant_dir)
  [ -n "$TEST_API_TOKEN" ] && [ -s "$dir/sweep-ledger.jsonl" ] || return 0
  STACK_TENANT_ID=$TEST_TENANT_ID STACK_API_TOKEN=$TEST_API_TOKEN STACK_STATE_DIR="$dir" STACK_SWEEP_DOCTOR=0 \
    bash "$STACK_REPO/scripts/stack/sweep.sh" "$1" "$2"
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

QA_TENANT_ID=${QA_TENANT_ID:-}
QA_API_TOKEN=${QA_API_TOKEN:-}
QA_TOKEN_ID=${QA_TOKEN_ID:-}
QA_EMAIL=${QA_EMAIL:-}
QA_PASSWORD=${QA_PASSWORD:-}
QA_BLOCKED_REASON=${QA_BLOCKED_REASON:-}

qa_tenant_dir() { printf '%s/qa-tenant' "$STACK_STATE_DIR"; }

# qa_tenant_declaration <bootstrap.yaml>: the QA tenant's declared user email,
# tier, operator grant base fee and usage waiver, tab-separated. The gitops
# overlay is read with the CLI's own bootstrap loader (bootstrapdecl), so a
# declaration the release apply would reject fails here too.
qa_tenant_declaration() {
  local bin="$STACK_STATE_DIR/bin/bootstrapdecl"
  if [ ! -x "$bin" ]; then
    mkdir -p "$STACK_STATE_DIR/bin" &&
      (cd "$STACK_REPO/scripts/stack/bootstrapdecl" && go build -o "$bin" .) >"$STACK_STATE_DIR/bootstrapdecl-build.log" 2>&1 ||
      { echo "bootstrapdecl build failed: $STACK_STATE_DIR/bootstrapdecl-build.log" >&2; return 1; }
  fi
  "$bin" "$1" "$QA_TENANT_ALIAS"
}

# qa_tenant_session_jwt: a live QA tenant access token from its own jar.
qa_tenant_session_jwt() {
  local jar
  jar="$(qa_tenant_dir)/session.jar"
  refresh_session_token "$jar" 2>/dev/null && return 0
  curl -s -m 30 -c "$jar" -o /dev/null "$BRIDGE_URL/auth/login" -H 'Content-Type: application/json' \
    --data "$(jq -cn --arg e "$QA_EMAIL" --arg p "$QA_PASSWORD" --argjson b "$(bot_fields)" '{email:$e, password:$p} + $b')" || return 1
  awk '$6 == "access_token" { print $7 }' "$jar" | tail -1
}

# qa_tenant_login <secrets env file> <bootstrap.yaml> <cluster>: sign in as the
# QA customer, check it is a normal customer and mint the run's token for it.
# On failure it records why in QA_BLOCKED_REASON; QA scenarios then report
# BLOCKED.
qa_tenant_login() {
  local env_file=$1 declaration=$2 cluster=$3 dir body code jwt
  dir=$(qa_tenant_dir)
  mkdir -p "$dir" && chmod 700 "$dir"
  QA_EMAIL=$(qa_tenant_declaration "$declaration" | cut -f1)
  if [ -z "$QA_EMAIL" ]; then
    QA_BLOCKED_REASON="no account user for $QA_TENANT_ALIAS in $declaration"
    return 1
  fi
  QA_PASSWORD=$(sops --decrypt "$env_file" 2>/dev/null | sed -n 's/^QA_ACCOUNT_PASSWORD=//p')
  if [ -z "$QA_PASSWORD" ]; then
    QA_BLOCKED_REASON="no QA_ACCOUNT_PASSWORD in $env_file"
    return 1
  fi
  body=$(mktemp "$dir/login.XXXXXX")
  code=$(curl -s -m 30 -c "$dir/session.jar" -o "$body" -w '%{http_code}' "$BRIDGE_URL/auth/login" -H 'Content-Type: application/json' \
    --data "$(jq -cn --arg e "$QA_EMAIL" --arg p "$QA_PASSWORD" --argjson b "$(bot_fields)" '{email:$e, password:$p} + $b')")
  QA_TENANT_ID=$(jq -r '.user.tenant_id // empty' "$body" 2>/dev/null)
  if [ "$(jq -r '.user.platform_operator' "$body" 2>/dev/null)" != false ]; then
    QA_BLOCKED_REASON="QA sign-in as $QA_EMAIL returned HTTP $code without a normal customer user"
    rm -f "$body"
    return 1
  fi
  rm -f "$body"
  jwt=$(awk '$6 == "access_token" { print $7 }' "$dir/session.jar" | tail -1)
  if ! mint_run_token "$jwt" "stack-$cluster-qa-" "QA tenant" || [ -z "$QA_TENANT_ID" ]; then
    QA_BLOCKED_REASON=${RUN_TOKEN_ERROR:-"the QA sign-in names no tenant"}
    return 1
  fi
  QA_TOKEN_ID=$RUN_TOKEN_ID QA_API_TOKEN=$RUN_TOKEN_VALUE
  echo "QA tenant run API token $QA_TOKEN_ID for tenant $QA_TENANT_ID ($QA_EMAIL, not a platform operator)"
  STACK_STATE_DIR="$dir" cleanup_run_objects "$QA_API_TOKEN" | sed 's/^cleanup:/recovering a killed run:/'
}

# qa_tenant_check <bootstrap.yaml>: the QA tenant is on the tier and operator
# grant its customer_billing row declares. Prints PASS or FAIL lines.
qa_tenant_check() {
  local declared tier fee waive r got_tier got_fee got_waive rc=0
  declared=$(qa_tenant_declaration "$1")
  tier=$(cut -f2 <<<"$declared") fee=$(cut -f3 <<<"$declared") waive=$(cut -f4 <<<"$declared")
  r=$(gql '{billingStatus{currentTier{tierName}} billingLifecycle{operatorGrant{basePrice waiveUsage}}}' '{}' "$QA_API_TOKEN")
  got_tier=$(jq -r '.data.billingStatus.currentTier.tierName // empty' <<<"$r")
  got_fee=$(jq -r '.data.billingLifecycle.operatorGrant.basePrice // empty' <<<"$r")
  got_waive=$(jq -r '.data.billingLifecycle.operatorGrant.waiveUsage // false' <<<"$r")
  if [ -n "$tier" ] && [ "$got_tier" = "$tier" ]; then
    echo "PASS  sweep[qa]: $QA_EMAIL is on the declared $tier tier"
  else
    echo "FAIL  sweep[qa]: $QA_EMAIL tier ${got_tier:-unreadable}, declared ${tier:-none}: $(jq -c '.errors // empty' <<<"$r")"
    rc=1
  fi
  # Purser reports a decimal base price; the declaration may write "0" for 0.00.
  if [ -n "$fee" ] && [ -n "$got_fee" ] && awk -v a="$fee" -v b="$got_fee" 'BEGIN { exit !(a + 0 == b + 0) }' &&
    [ "$got_waive" = "$waive" ]; then
    echo "PASS  sweep[qa]: operator grant base fee $got_fee, usage waived $got_waive, as declared"
  else
    echo "FAIL  sweep[qa]: operator grant base fee ${got_fee:-none}, usage waived $got_waive; declared base fee ${fee:-none}, usage waived $waive"
    rc=1
  fi
  return "$rc"
}

# qa_tenant_sweep <since epoch> <windows file> <bootstrap.yaml>: the QA
# tenant's declared billing, then the sweep's entity checks over what QA
# scenarios created, with the QA token and ledger. The run's sweep already ran
# the doctor.
qa_tenant_sweep() {
  local dir rc=0
  dir=$(qa_tenant_dir)
  [ -n "$QA_API_TOKEN" ] || return 0
  qa_tenant_check "$3" || rc=1
  if [ -s "$dir/sweep-ledger.jsonl" ]; then
    STACK_TENANT_ID=$QA_TENANT_ID STACK_API_TOKEN=$QA_API_TOKEN STACK_STATE_DIR="$dir" STACK_SWEEP_DOCTOR=0 \
      bash "$STACK_REPO/scripts/stack/sweep.sh" "$1" "$2"
    case $? in
      0) ;;
      2) [ "$rc" = 0 ] && rc=2 ;;
      *) rc=1 ;;
    esac
  fi
  return "$rc"
}

# qa_tenant_cleanup: deletes what QA scenarios created and their webhook
# endpoint, then revokes the QA token and confirms the revocation.
qa_tenant_cleanup() {
  local dir jwt code endpoint
  dir=$(qa_tenant_dir)
  if [ -n "$QA_TOKEN_ID" ]; then
    STACK_STATE_DIR="$dir" cleanup_run_objects "$QA_API_TOKEN" | tee "$dir/cleanup.log"
    jwt=$(qa_tenant_session_jwt) || jwt=''
    endpoint=$(awk '{print $1}' "$dir/webhook-endpoint" 2>/dev/null)
    if [ -n "$endpoint" ]; then
      gql 'mutation($id:ID!){deleteWebhookEndpoint(id:$id){__typename}}' "$(jq -cn --arg id "$endpoint" '{id:$id}')" "$jwt" >/dev/null
      echo "QA tenant webhook endpoint $endpoint deleted"
    fi
    gql 'mutation($id:ID!){revokeDeveloperToken(id:$id){__typename}}' "$(jq -cn --arg id "$QA_TOKEN_ID" '{id:$id}')" "$jwt" >/dev/null
    code=$(curl -s -m 15 -o /dev/null -w '%{http_code}' "$BRIDGE_GRAPHQL_URL" -H "Authorization: Bearer $QA_API_TOKEN" \
      -H 'Content-Type: application/json' --data '{"query":"{streamsConnection(page:{first:1}){totalCount}}"}')
    if [ "$code" = 401 ]; then
      echo "QA tenant run API token $QA_TOKEN_ID revoked (HTTP 401 after revocation)"
    else
      echo "WARN: QA tenant run API token $QA_TOKEN_ID still answers HTTP $code after revocation" >&2
    fi
  fi
  rm -f "$dir/session.jar"
  QA_PASSWORD=''
}
