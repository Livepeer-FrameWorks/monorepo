#!/usr/bin/env bash
# Run the stack scenarios against a deployed cluster: STACK_TARGET=staging make
# verify-stack. `frameworks cluster stack-target` describes the cluster from the
# gitops manifest (endpoints, edges, service journals, database access); the run
# logs in as the platform operator, mints a short-lived API token for its own
# test streams, and revokes it on exit. Only scenarios declared "staging: safe"
# run (scenarios/list.sh); this runner never provisions, restarts, drains or
# tears anything down.
#
#   STACK_TARGET_CLUSTER   gitops cluster to verify (default staging)
#   FRAMEWORKS_GITOPS_DIR  gitops checkout (default: ../gitops beside the repo)
#   STACK_CLI_CONTEXT      CLI context of that cluster (default staging)
#   STACK_ENDPOINTS_FILE   optional overrides sourced after the derived target
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
cd "$repo" || exit 1
export STACK_TARGET=staging STACK_REPO="$repo"
export STACK_ADAPTER_FILE=${STACK_ADAPTER_FILE:-$here/staging-adapter.sh}
cluster=${STACK_TARGET_CLUSTER:-staging}
gitops=${FRAMEWORKS_GITOPS_DIR:-$repo/../gitops}
export STACK_CLI_CONTEXT=${STACK_CLI_CONTEXT:-staging}
if [ ! -d "$gitops/clusters/$cluster" ]; then
  echo "BLOCKED: no gitops cluster $cluster under $gitops (set FRAMEWORKS_GITOPS_DIR)" >&2
  exit 2
fi

if [ -z "${STACK_STATE_DIR:-}" ]; then
  STACK_STATE_DIR="$repo/.stack/$cluster-$(date -u +%Y%m%dT%H%M%SZ)-$$"
fi
mkdir -p "$STACK_STATE_DIR" || exit 1
chmod 700 "$STACK_STATE_DIR"
export STACK_STATE_DIR
printf '%s evidence: %s\n' "$cluster" "$STACK_STATE_DIR"

# Selection first: an unsafe scenario named for staging is refused before
# anything touches the cluster.
scenarios=()
selection=$(STACK_SCENARIOS="${STACK_SCENARIOS:-default}" bash "$here/scenarios/list.sh") || exit 1
while IFS= read -r path; do
  [ -n "$path" ] && scenarios+=("$path")
done <<<"$selection"
[ "${#scenarios[@]}" -gt 0 ] || { echo 'no scenarios selected' >&2; exit 1; }

make build-bin-cli >"$STACK_STATE_DIR/build-cli.log" 2>&1 || { echo "CLI build failed: $STACK_STATE_DIR/build-cli.log" >&2; exit 1; }
target="$STACK_STATE_DIR/target"
if ! "$repo/bin/cli" --context "$STACK_CLI_CONTEXT" cluster stack-target --gitops-dir "$gitops" --cluster "$cluster" \
  --out-dir "$target" >"$STACK_STATE_DIR/stack-target.log" 2>&1; then
  echo "BLOCKED: cluster stack-target failed: $STACK_STATE_DIR/stack-target.log" >&2
  exit 2
fi
set -a
# shellcheck source=/dev/null
. "$target/target.env"
if [ -n "${STACK_ENDPOINTS_FILE:-}" ]; then
  # shellcheck source=/dev/null
  . "$STACK_ENDPOINTS_FILE"
fi
set +a
# shellcheck source=/dev/null
. "$target/operator.env"
rm -f "$target/operator.env"
# Scenarios that sign in (session_token) use the operator's login; object
# listings (s3-prefix-empty.py) use cell A's store keys.
export STACK_DEMO_EMAIL=$STACK_OPERATOR_EMAIL STACK_DEMO_PASSWORD=$STACK_OPERATOR_PASSWORD
unset STACK_OPERATOR_PASSWORD
[ -n "${STACK_S3_ACCESS_KEY:-}" ] && export STACK_S3_ACCESS_KEY STACK_S3_SECRET_KEY
# lib.sh fills in the compose stack's receiver; only an explicit one counts here.
receiver_override=${WEBHOOK_RECEIVER_URL:-}
# shellcheck source=scripts/stack/lib.sh
STACK_SCENARIO=verify-target . "$here/lib.sh"
if ! stack_services >/dev/null; then
  echo 'BLOCKED: the stack-target service map is empty' >&2
  exit 2
fi
if [ "${STACK_MAIL_MODE:-mailpit}" = imap ]; then
  for required in STACK_TEST_EMAIL_BASE STACK_MAIL_IMAP_HOST STACK_MAIL_IMAP_USER STACK_MAIL_IMAP_PASSWORD STACK_SMTP_PORT; do
    if [ -z "${!required:-}" ]; then
      echo "BLOCKED: staging IMAP verification needs $required" >&2
      exit 2
    fi
  done
fi

# The run's own API token: the operator's tenant, revoked when the run ends.
login=$(mktemp "$STACK_STATE_DIR/login.XXXXXX")
jar="$STACK_STATE_DIR/operator.jar"
# operator_login [body file]: a new operator session in $jar; prints its HTTP status.
operator_login() {
  curl -s -m 30 -c "$jar" -o "${1:-/dev/null}" -w '%{http_code}' "$BRIDGE_URL/auth/login" -H 'Content-Type: application/json' \
    --data "$(jq -cn --arg e "$STACK_DEMO_EMAIL" --arg p "$STACK_DEMO_PASSWORD" --argjson b "$(bot_fields)" '{email:$e, password:$p} + $b')"
}
# operator_session_jwt: a live operator access token. Scenario 37 signs out
# every session of the operator account, so a refused refresh signs in again.
operator_session_jwt() {
  refresh_session_token "$jar" 2>/dev/null && return 0
  [ "$(operator_login)" = 200 ] || return 1
  awk '$6 == "access_token" { print $7 }' "$jar" | tail -1
}
code=$(operator_login "$login")
STACK_TENANT_ID=$(jq -r '.user.tenant_id // empty' "$login" 2>/dev/null)
rm -f "$login"
operator_jwt=$(awk '$6 == "access_token" { print $7 }' "$jar" | tail -1)
if [ "$code" != 200 ] || [ -z "$STACK_TENANT_ID" ] || [ -z "$operator_jwt" ]; then
  echo "BLOCKED: operator login to $BRIDGE_URL failed (HTTP $code)" >&2
  exit 2
fi
token_id=""
receiver_ssh=()
receiver_dir=""
sdk_venv="$STACK_STATE_DIR/sdk-venv"
cleanup_run() {
  local jwt code endpoint
  rm -rf "$sdk_venv"
  # After the sweep: what the scenarios created, while the run's token still works.
  if [ -n "$token_id" ]; then
    cleanup_run_objects "$STACK_API_TOKEN" | tee "$STACK_STATE_DIR/cleanup.log"
  fi
  endpoint=$(awk '{print $1}' "$STACK_STATE_DIR/webhook-endpoint" 2>/dev/null)
  if [ -n "$endpoint" ]; then
    jwt=$(operator_session_jwt) || jwt=$operator_jwt
    gql 'mutation($id:ID!){deleteWebhookEndpoint(id:$id){__typename}}' "$(jq -cn --arg id "$endpoint" '{id:$id}')" "$jwt" >/dev/null
    echo "run webhook endpoint $endpoint deleted"
  fi
  if [ -n "$receiver_dir" ]; then
    "${receiver_ssh[@]}" "kill \$(cat $receiver_dir/pid) 2>/dev/null; rm -rf $receiver_dir" </dev/null
  fi
  if [ -n "$token_id" ]; then
    jwt=$(operator_session_jwt) || jwt=$operator_jwt
    gql 'mutation($id:ID!){revokeDeveloperToken(id:$id){__typename}}' "$(jq -cn --arg id "$token_id" '{id:$id}')" "$jwt" >/dev/null
    code=$(curl -s -m 15 -o /dev/null -w '%{http_code}' "$BRIDGE_GRAPHQL_URL" -H "Authorization: Bearer $STACK_API_TOKEN" \
      -H 'Content-Type: application/json' --data '{"query":"{streamsConnection(page:{first:1}){totalCount}}"}')
    if [ "$code" = 401 ]; then
      echo "run API token $token_id revoked (HTTP 401 after revocation)"
    else
      echo "WARN: run API token $token_id still answers HTTP $code after revocation" >&2
    fi
  fi
  rm -f "$jar"
}
trap cleanup_run EXIT

# A run killed before its exit cleanup leaves its token, webhook endpoint and
# objects behind; this run recovers them. A stack token used in the last 15
# minutes belongs to a run still in progress, which this one must not disturb.
stale_tokens=$(gql '{developerTokensConnection(page:{first:200}){nodes{id tokenName status lastUsedAt}}}' '{}' "$operator_jwt" |
  jq -r --arg prefix "stack-$cluster-" --argjson now "$(date +%s)" '.data.developerTokensConnection.nodes[]?
    | select(.status == "active" and (.tokenName | startswith($prefix)))
    | [.id, .tokenName, (if .lastUsedAt then ($now - (.lastUsedAt | sub("\\.[0-9]+"; "") | fromdateiso8601)) else 999999 end)] | @tsv')
while IFS=$'\t' read -r stale_id stale_name idle; do
  [ -n "$stale_id" ] || continue
  if [ "${idle%.*}" -lt 900 ]; then
    echo "BLOCKED: another $cluster run is in progress (its token $stale_name was used ${idle%.*}s ago)" >&2
    exit 2
  fi
  echo "recovering a killed run: revoking $stale_name $(gql 'mutation($id:ID!){revokeDeveloperToken(id:$id){__typename}}' \
    "$(jq -cn --arg id "$stale_id" '{id:$id}')" "$operator_jwt" | jq -r '.data.revokeDeveloperToken.__typename // .errors[0].message // "?"')"
done <<<"$stale_tokens"
gql '{webhookEndpointsConnection(page:{first:200}){nodes{id description}}}' '{}' "$operator_jwt" |
  jq -r '.data.webhookEndpointsConnection.nodes[]? | select(.description == "stack scenarios") | .id' |
  while IFS= read -r stale_endpoint; do
    echo "recovering a killed run: deleting webhook endpoint $stale_endpoint $(gql 'mutation($id:ID!){deleteWebhookEndpoint(id:$id){__typename}}' \
      "$(jq -cn --arg id "$stale_endpoint" '{id:$id}')" "$operator_jwt" | jq -r '.data.deleteWebhookEndpoint.__typename // .errors[0].message // "?"')"
  done

created=$(gql 'mutation($i:CreateDeveloperTokenInput!){createDeveloperToken(input:$i){__typename ... on DeveloperToken{id tokenValue} ... on ValidationError{message} ... on AuthError{message}}}' \
  "$(jq -cn --arg n "stack-$cluster-$(date -u +%Y%m%dT%H%M%SZ)" \
    '{i:{name:$n, expiresIn:1, permissions:"streams:read,streams:write,analytics:read,developer:read,developer:write,account:read,infrastructure:read,placement:read"}}')" \
  "$operator_jwt")
token_id=$(jq -r '.data.createDeveloperToken.id // empty' <<<"$created")
STACK_API_TOKEN=$(jq -r '.data.createDeveloperToken.tokenValue // empty' <<<"$created")
if [ -z "$token_id" ] || [ -z "$STACK_API_TOKEN" ]; then
  echo "BLOCKED: createDeveloperToken failed: $(jq -c 'del(.data.createDeveloperToken.tokenValue)' <<<"$created" 2>/dev/null)" >&2
  exit 2
fi
export STACK_TENANT_ID STACK_API_TOKEN
echo "run API token $token_id for tenant $STACK_TENANT_ID"
# Scenario objects a killed run left (its ledger died with it, so by name).
cleanup_run_objects "$STACK_API_TOKEN" | sed 's/^cleanup:/recovering a killed run:/'

# Webhook deliveries need a receiver the cluster can reach and this runner can
# read: the development host (scripts/remote-dev.sh's inventory) serves
# webhook-receiver.py for the run. Without it the webhook checks fail.
dev_inventory="$gitops/development/hosts.enc.yaml"
receiver_ready=$receiver_override
if [ -z "$receiver_override" ] && [ -f "$dev_inventory" ]; then
  dev_host=${REMOTE_DEV_HOST:-$(sops --decrypt --extract '["hosts"]["remotedev"]["address"]' "$dev_inventory" 2>/dev/null)}
  dev_port=${REMOTE_DEV_PORT:-$(sops --decrypt --extract '["hosts"]["remotedev"]["port"]' "$dev_inventory" 2>/dev/null)}
  if [ -n "$dev_host" ]; then
    receiver_ssh=(ssh -o BatchMode=yes -o ConnectTimeout=10 -p "${dev_port:-22}" "${REMOTE_DEV_USER:-$(id -un)}@$dev_host")
    receiver_dir="/tmp/fw-stack-receiver-$cluster-$$"
    for _ in 1 2 3 4 5; do
      receiver_port=$((20000 + RANDOM % 10000))
      "${receiver_ssh[@]}" "mkdir -p $receiver_dir && cat >$receiver_dir/receiver.py && cd $receiver_dir &&
        (RECEIVER_DATA=$receiver_dir RECEIVER_PORT=$receiver_port nohup python3 receiver.py >receiver.log 2>&1 & echo \$! >pid)" \
        <"$here/webhook-receiver.py" || break
      up=0
      for _ in 1 2 3 4 5; do
        curl -fs -m 3 -o /dev/null "http://$dev_host:$receiver_port/requests" && { up=1; break; }
        sleep 1
      done
      if [ "$up" = 1 ]; then
        export WEBHOOK_RECEIVER_URL="http://$dev_host:$receiver_port" WEBHOOK_TARGET_URL="http://$dev_host:$receiver_port/hooks"
        echo "webhook receiver for the run: $WEBHOOK_RECEIVER_URL"
        receiver_ready=$WEBHOOK_RECEIVER_URL
        break
      fi
      "${receiver_ssh[@]}" "kill \$(cat $receiver_dir/pid) 2>/dev/null" </dev/null
    done
  fi
fi
[ -n "$receiver_ready" ] || echo 'WARN: no webhook receiver for the run; webhook checks will fail' >&2

repeat=${STACK_REPEAT:-1}
case "$repeat" in
  '' | *[!0-9]*) echo 'STACK_REPEAT must be an integer from 1 to 100' >&2; exit 1 ;;
esac
if [ "$repeat" -lt 1 ] || [ "$repeat" -gt 100 ]; then
  echo 'STACK_REPEAT must be an integer from 1 to 100' >&2
  exit 1
fi

# Scenario 07's SDK smokes and 37's WebSocket probes run here, not in
# stack-runner: the TS smoke needs npm_api's dist, and the Python smoke and the
# probes a venv with the Python SDK's dependencies (websockets among them).
if printf '%s\n' "${scenarios[@]}" | grep -qE '/(07|37)-'; then
  # shellcheck source=scripts/stack/sdk-prereqs.sh
  . "$here/sdk-prereqs.sh"
  if printf '%s\n' "${scenarios[@]}" | grep -q '/07-'; then
    build_npm_api_dist "$repo"
  fi
  if sdk_bin=$(python_sdk_venv "$repo" "$sdk_venv"); then
    export PATH="$sdk_bin:$PATH"
  else
    echo 'WARN: the Python SDK venv could not be built; the Python SDK smoke and the WebSocket probes will report BLOCKED' >&2
  fi
fi

run_since="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
sweep_since=$(date +%s)
sweep_windows="$STACK_STATE_DIR/sweep-scenarios.tsv"
: >"$sweep_windows"
failed=0 blocked=0 summary=()
for path in "${scenarios[@]}"; do
  name="$(basename "$path" .sh)"
  for iteration in $(seq 1 "$repeat"); do
    label="$name ($iteration/$repeat)"
    printf '\n==== %s ====\n' "$label"
    scenario_since="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
    window_start=$(date +%s)
    bash "$path"
    rc=$?
    printf '%s\t%s\t%s\n' "$name" "$window_start" "$(date +%s)" >>"$sweep_windows"
    case "$rc" in
      0) summary+=("PASS     $label") ;;
      2) summary+=("BLOCKED  $label"); blocked=1 ;;
      *) summary+=("FAIL     $label (exit $rc)"); failed=1 ;;
    esac
    if bash "$here/logcheck.sh" "$scenario_since" "$name"; then
      summary+=("PASS     $label logs")
    else
      summary+=("FAIL     $label logs"); failed=1
    fi
  done
done
printf '\n==== logcheck ====\n'
if bash "$here/logcheck.sh" "$run_since"; then
  summary+=("PASS     logcheck")
else
  summary+=("FAIL     logcheck"); failed=1
fi
# The same sweep as the stack's, read-only: API, ClickHouse and Postgres reads,
# and the operator CLI's cluster doctor for host health.
if [ "${STACK_SWEEP:-1}" != 0 ]; then
  printf '\n==== sweep ====\n'
  sweep_started=$(date +%s)
  bash "$here/sweep.sh" "$sweep_since" "$sweep_windows"
  rc=$?
  label="sweep ($(($(date +%s) - sweep_started))s)"
  case "$rc" in
    0) summary+=("PASS     $label") ;;
    2) summary+=("BLOCKED  $label"); blocked=1 ;;
    *) summary+=("FAIL     $label"); failed=1 ;;
  esac
fi
printf '\n==== staging summary ====\n'
printf '%s\n' "${summary[@]}"
[ "$failed" -ne 0 ] && exit 1
[ "$blocked" -ne 0 ] && exit 2
exit 0
