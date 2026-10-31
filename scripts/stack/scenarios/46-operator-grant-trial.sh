#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, creates a disposable tenant and changes its billing and account access
# A trial expires back to empty PAYG; user suspension is then tested under a
# fresh admitted grant so billing denial cannot masquerade as user revocation.
. "$(dirname "$0")/../lib.sh"

# Scenario 37 gives the event-driven session revocation path two seconds from
# the mutation response; use the same bound for this existing-session check.
SESSION_REVOCATION_BOUND_S=2
# Scenario observation windows: mail and stop delivery get one minute.
# Media delivery allows Commodore's mediaAuthorityDeliveryTimeout (35s).
# Expiry includes the two-minute fixture grant, Purser's one-minute sweep,
# then the media window. Gateway defaultTenantCacheTiming permits a 1m TTL
# with 20% jitter plus a 30s stale window; billing refusal allows that and
# a request. Re-admission also allows mediaAuthorityRefreshTimeout (90s).
MAIL_BOUND_S=60
MEDIA_BOUND_S=60
EXPIRY_BOUND_S=240
BILLING_REFUSAL_BOUND_S=120
INGEST_STOP_BOUND_S=60
READMISSION_BOUND_S=180
# Longer than admission, API probes and teardown observation combined.
REVOCATION_PUBLISH_LIFETIME_S=600
need jq curl python3 psql ffmpeg openssl || finish
[ -x "$STACK_REPO/bin/cli" ] || {
  blocked 'operator CLI missing; make build-bin-cli'
  finish
}
# shellcheck source=scripts/stack/cli-context.sh
. "$STACK_LIB_DIR/cli-context.sh"
tenant="" email="" id="" pid=""
tenant_closed=0
# The stack's validated bootstrap fixture owns this reusable system operator.
operator_email=operator@stack.test
operator_password=stack-operator-password
operator_jar="$STACK_STATE_DIR/operator46.cookies"
trial_until() { python3 -c 'import datetime,sys; print((datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(minutes=int(sys.argv[1]))).strftime("%Y-%m-%dT%H:%M:%SZ"))' "$1"; }
CLI="$STACK_REPO/bin/cli"
operator_login() {
  local code
  code=$(jq -cn --arg e "$operator_email" --arg p "$operator_password" --argjson b "$(bot_fields)" '{email:$e,password:$p} + $b' |
    curl -s -m 20 -c "$operator_jar" -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login") || return 1
  [ "$code" = 200 ] || {
    printf 'operator login failed: HTTP %s\n' "$code" >&2
    return 1
  }
  operator_jwt=$(awk '$6=="access_token"{print $7}' "$operator_jar" | tail -1)
  [ -n "$operator_jwt" ]
}
cli_json() {
  operator_login || return 1
  FW_USER_TOKEN="$operator_jwt" "$CLI" "$@" --output json
}
cleanup() {
  [ "${cleanup_done:-0}" = 0 ] || return 0
  cleanup_done=1
  local failed=0 current
  [ -z "$pid" ] || kill "$pid" 2>/dev/null
  if [ -z "$tenant" ] && [ -n "$email" ]; then
    tenant=$(pg commodore "SELECT tenant_id::text FROM commodore.users WHERE lower(email::text)=lower('$email') LIMIT 1") || {
      fail 'cleanup tenant lookup failed'
      return 1
    }
  fi
  [ -n "$tenant" ] || return 0
  # Media deletion can use the restored user's token on the successful path.
  if [ -n "$id" ] && [ "$tenant_closed" = 0 ]; then
    cli_json admin users activate --email "$email" --reason 'stack cleanup access' >/dev/null || {
      fail 'cleanup could not restore disposable user access'
      failed=1
    }
    if [ "$(user_login)" = 200 ]; then
      STACK_API_TOKEN=$(awk '$6=="access_token"{print $7}' "$cookies" | tail -1)
      export STACK_API_TOKEN
    else
      fail 'cleanup could not obtain a fresh disposable user session'
      failed=1
    fi
    delete_stream "$id" || {
      fail 'cleanup stream deletion failed'
      failed=1
    }
  fi
  current=$(cli_json admin billing grant show --tenant-id "$tenant") || {
    fail 'cleanup could not read grant'
    failed=1
  }
  if json_has "${current:-}" '.grant != null'; then
    cli_json admin billing grant revoke --tenant-id "$tenant" --reason 'stack cleanup' >/dev/null || {
      fail 'cleanup grant revoke failed'
      failed=1
    }
  fi
  if [ "$tenant_closed" = 0 ]; then
    trial_name=$(cli_json admin tenants get "$tenant" | jq -r '.tenant.name // empty')
    if [ -z "$trial_name" ]; then
      fail 'cleanup tenant name lookup failed'
      return 1
    fi
    cli_json admin tenants deactivate "$tenant" --yes --tenant-name "$trial_name" --reason 'stack disposable trial complete' >/dev/null || {
      fail 'cleanup tenant closure failed'
      failed=1
    }
  fi
  return "$failed"
}
# Include cleanup failures in the scenario summary as well as its exit status.
finish_trial() {
  cleanup || fail 'cleanup did not complete'
  finish
}
on_exit() {
  local result=$?
  cleanup || result=1
  exit "$result"
}
trap on_exit EXIT
operator_login || {
  blocked 'operator fixture login failed'
  finish_trial
}
email=$(test_email "trial-$(date +%s)-$RANDOM")
password="Stack-$(openssl rand -hex 8)"
registration=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" \
  '{email:$e,password:$p,first_name:"Stack",last_name:"Trial"} + $b' |
  curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/register")
check 'trial signup accepted' [ "$registration" = 201 ]
[ "$registration" = 201 ] || finish_trial
eventually "$MAIL_BOUND_S" 'trial verification email delivered' mail_seen "$email" || finish_trial
verification=$(mail_token "$email")
verified=$(jq -cn --arg t "$verification" '{token:$t}' |
  curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/verify")
check 'trial verified' [ "$verified" = 200 ]
cookies="$STACK_STATE_DIR/trial-46.cookies"
user_login() {
  jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" '{email:$e,password:$p} + $b' |
    curl -s -m 20 -c "$cookies" -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login"
}
check 'trial user can log in' [ "$(user_login)" = 200 ]
jwt=$(awk '$6=="access_token"{print $7}' "$cookies" | tail -1)
[ -n "$jwt" ] || {
  fail 'trial login returned no JWT'
  finish_trial
}
tenant=$(
  python3 - "$jwt" <<'PY'
import base64,json,sys
payload=sys.argv[1].split('.')[1]
print(json.loads(base64.urlsafe_b64decode(payload+'='*(-len(payload)%4)))['tenant_id'])
PY
)
trial_name=$(cli_json admin tenants get "$tenant" | jq -r '.tenant.name // empty')
[ -n "$trial_name" ] || {
  fail "trial tenant name lookup failed"
  finish_trial
}
cli_json admin billing set-tier --tenant-id "$tenant" --tier payg --reason 'stack trial baseline' >/dev/null || {
  fail 'set-tier failed'
  finish_trial
}
# A pre-call crash leaves a durable receipt without a provider object. Recover
# that state through the operator API; no provider credentials or money are used.
recovery_id=$(pg purser "WITH receipt AS (
 INSERT INTO purser.payment_provider_intents(tenant_id,provider,purpose,status,submission_state,currency,idempotency_key,expires_at)
 VALUES('$tenant','stripe','tenant_subscription_checkout','provider_call_failed','prepared','EUR',gen_random_uuid()::text,NOW()+INTERVAL '23 hours') RETURNING id,tenant_id,expires_at
), queued AS (
 INSERT INTO purser.provider_webhook_inbox(provider,event_key,headers,raw_payload,next_attempt_at)
 SELECT 'checkout_recovery','checkout-expiry:'||id::text,jsonb_build_object('tenant_id',tenant_id::text),
 convert_to(jsonb_build_object('checkout_reservation_id',id::text,'tenant_id',tenant_id::text)::text,'UTF8'),expires_at FROM receipt
 RETURNING event_key)
 SELECT id::text FROM receipt WHERE EXISTS(SELECT 1 FROM queued)") || {
  fail 'checkout recovery fixture creation failed'
  finish_trial
}
recovery_list=$(cli_json admin billing recovery list --tenant-id "$tenant") || {
  fail 'recovery list failed'
  finish_trial
}
check 'recovery list exposes the tenant reservation' json_has "$recovery_list" ".items | any(.reservation_id == \"$recovery_id\")"
recovery=$(cli_json admin billing recovery checkout --tenant-id "$tenant" --reservation-id "$recovery_id" --reason 'stack unbound checkout recovery' --yes) || {
  fail 'confirmed checkout recovery failed'
  finish_trial
}
check 'confirmed checkout recovery reports expiry and audit ID' json_has "$recovery" '.outcome == "expired" and (.audit_id | length) > 0'
recovery_history=$(cli_json admin billing recovery list --tenant-id "$tenant" --history) || {
  fail 'checkout recovery history failed'
  finish_trial
}
check 'recovery history retains operator reason and outcome' json_has "$recovery_history" '.entries | any(.reason == "stack unbound checkout recovery" and .outcome == "expired" and (.actor_id | length) > 0)'
until=$(trial_until 2)
grant=$(cli_json admin billing grant set --tenant-id "$tenant" --tier enterprise --base-fee 0 --waive-usage --until "$until" --reason 'stack temporary trial') || {
  fail 'grant set failed'
  finish_trial
}
grant_start=$(printf '%s' "$grant" | jq -r '.grant.granted_at')
check 'trial remembers PAYG fallback' json_has "$grant" '.grant.fallback_tier_name == "payg"'
listed=$(cli_json admin billing grant list --expiring 7d) || {
  fail 'grant list failed'
  finish_trial
}
check 'grant listing includes trial' json_has "$listed" ".grants | any(.tenant_id == \"$tenant\")"
api=$(gql 'mutation($i:CreateDeveloperTokenInput!){createDeveloperToken(input:$i){__typename ... on DeveloperToken{tokenValue}}}' \
  '{"i":{"name":"stack trial","permissions":"streams:read,streams:write,developer:read,developer:write"}}' "$jwt" | jq -r '.data.createDeveloperToken.tokenValue // empty')
[ -n "$api" ] || {
  fail 'trial API token creation failed'
  finish_trial
}
export STACK_TENANT_ID="$tenant" STACK_API_TOKEN="$api"
stream=$(create_stream "stack-trial-46-$(date +%s)" false)
id=$(echo "$stream" | jq -r '.id // empty')
key=$(echo "$stream" | jq -r '.streamKey // empty')
pb=$(echo "$stream" | jq -r '.playbackId // empty')
[ -n "$id" ] && [ -n "$key" ] || {
  fail "trial stream creation: $stream"
  finish_trial
}
internal=$(internal_name_of_key "$key")
[ -n "$internal" ] || {
  fail 'trial stream has no internal name'
  finish_trial
}
pid=$(publish_until_admitted "$EDGE_A_RTMP" "$key" 320x240 10 240 60) || {
  fail 'trial ingest refused'
  finish_trial
}
eventually "$MEDIA_BOUND_S" 'trial playback serves real media' media_at "${FOGHORN_A_URLS%% *}" "$pb"
fallback_applied() {
  [ "$(pg purser "SELECT bt.tier_name FROM purser.tenant_subscriptions ts JOIN purser.billing_tiers bt ON bt.id=ts.tier_id WHERE ts.tenant_id='$tenant'")" = payg ]
}
eventually "$EXPIRY_BOUND_S" 'expired trial falls back to PAYG' fallback_applied
check 'trial closing invoice is zero' [ "$(pg purser "SELECT count(*) FROM purser.billing_invoices WHERE tenant_id='$tenant' AND document_kind='invoice' AND amount=0 AND period_start='$grant_start'::timestamptz AND period_end='$until'::timestamptz")" = 1 ]
playback_denied() {
  local response code body
  response=$(curl -s -m 10 -w '\n%{http_code}' "${FOGHORN_A_URLS%% *}/play/$pb/hls") || return 1
  code=${response##*$'\n'}
  body=${response%$'\n'*}
  if [ "$code" = 402 ] || [ "$code" = 403 ]; then return 0; fi
  # An inactive signed media object deliberately hides its public view key.
  # Require that exact response while the original stream still exists;
  # a missing route or an offline publisher is not a billing refusal.
  if [ "$code" = 404 ] && json_has "$body" '.code == "VIEW_KEY_NOT_FOUND"'; then
    [ "$(pg commodore "SELECT COUNT(*) FROM commodore.streams WHERE tenant_id='$tenant' AND internal_name='$internal' AND deleted_at IS NULL")" = 1 ]
    return
  fi
  printf 'expired trial playback: HTTP %s (%s)\n' "$code" "$(printf '%s' "$body" | jq -r '.code // "unclassified"' 2>/dev/null)" >&2
  return 1
}
eventually "$BILLING_REFUSAL_BOUND_S" 'expired trial playback is refused for billing' playback_denied
kill "$pid" 2>/dev/null
pid=
no_admission() { [ -z "$(open_generation "$FOGHORN_A_DB" "$internal")" ]; }
# Quiescence after killing the fixture is setup for the next refusal probe.
quiescence_deadline=$(($(monotonic_ms) + INGEST_STOP_BOUND_S * 1000))
while ! no_admission; do
  if [ "$(monotonic_ms)" -ge "$quiescence_deadline" ]; then
    fail 'fixture publisher did not settle before the new-ingest probe'
    finish_trial
  fi
  sleep 1
done
check 'empty PAYG refuses a new ingest session' publish_refused "$FOGHORN_A_DB" "$internal" "$EDGE_A_RTMP" "$key"
cli_json admin billing grant set --tenant-id "$tenant" --tier enterprise --base-fee 0 --waive-usage --until "$(trial_until 15)" --reason 'stack user revocation fixture' >/dev/null || {
  fail 'stack user revocation fixture command failed'
  finish_trial
}
credential_admitted() {
  local response
  response=$(gql 'query{streamsConnection(page:{first:1}){nodes{id}}}' '{}' "$1") || return 1
  json_has "$response" '.errors == null and (.data.streamsConnection.nodes | type) == "array"'
}
credential_refused() {
  local response
  response=$(gql 'query{streamsConnection(page:{first:1}){nodes{id}}}' '{}' "$1") || return 1
  json_has "$response" '.error == "authentication failed" or .error == "Invalid token" or (.data == null and any(.errors[]?; .extensions.code == "UNAUTHENTICATED"))'
}
api_admitted() { credential_admitted "$api"; }
eventually "$READMISSION_BOUND_S" 'fresh grant restores rated API access' api_admitted
publisher_deadline_ms=$(($(monotonic_ms) + REVOCATION_PUBLISH_LIFETIME_S * 1000))
pid=$(publish_until_admitted "$EDGE_A_RTMP" "$key" 320x240 10 "$REVOCATION_PUBLISH_LIFETIME_S" 120) || {
  fail 'fresh grant did not restore ingest'
  finish_trial
}
eventually "$MEDIA_BOUND_S" 'fresh grant restores media playback' media_at "${FOGHORN_A_URLS%% *}" "$pb"
check 'API token accepted immediately before deactivation' credential_admitted "$api"
check 'session accepted immediately before deactivation' credential_admitted "$jwt"
cli_json admin users deactivate --email "$email" --reason 'stack user suspension' >/dev/null || {
  fail 'stack user suspension command failed'
  finish_trial
}
revocation_started_ms=$(monotonic_ms)
within_bound_since "$revocation_started_ms" "$SESSION_REVOCATION_BOUND_S" 'disabled user existing session is refused' credential_refused "$jwt"
code=$(user_login)
check 'disabled user cannot log in' bash -c '[[ "$1" = 401 || "$1" = 403 ]]' _ "$code"
check 'disabled user API token is refused' credential_refused "$api"
eventually "$INGEST_STOP_BOUND_S" 'deactivated owner active push ends' publisher_stopped_before_deadline "$publisher_deadline_ms" no_admission
kill "$pid" 2>/dev/null
pid=
check 'disabled user stream key creates no ingest session' publish_refused "$FOGHORN_A_DB" "$internal" "$EDGE_A_RTMP" "$key"
cli_json admin users activate --email "$email" --reason 'stack user restored' >/dev/null || {
  fail 'stack user restored command failed'
  finish_trial
}
check 'activated user can start a fresh session' [ "$(user_login)" = 200 ]
jwt=$(awk '$6=="access_token"{print $7}' "$cookies" | tail -1)
eventually "$BILLING_REFUSAL_BOUND_S" 'activation restores the original API token under the admitted grant' api_admitted
publisher_deadline_ms=$(($(monotonic_ms) + REVOCATION_PUBLISH_LIFETIME_S * 1000))
pid=$(publish_until_admitted "$EDGE_A_RTMP" "$key" 320x240 10 "$REVOCATION_PUBLISH_LIFETIME_S" 90) || {
  fail 'activation did not restore ingest'
  finish_trial
}
pass 'activation restores ingest under the admitted grant'
check 'restored session accepted immediately before closure' credential_admitted "$jwt"
check 'restored API token accepted immediately before closure' credential_admitted "$api"
cli_json admin tenants deactivate "$tenant" --yes --tenant-name "$trial_name" --reason 'stack active tenant closure' >/dev/null || {
  fail 'active tenant closure failed'
  finish_trial
}
tenant_closed=1
revocation_started_ms=$(monotonic_ms)
within_bound_since "$revocation_started_ms" "$SESSION_REVOCATION_BOUND_S" 'closed tenant existing session is refused' credential_refused "$jwt"
eventually "$INGEST_STOP_BOUND_S" 'closed tenant active push ends' publisher_stopped_before_deadline "$publisher_deadline_ms" no_admission
code=$(user_login)
check 'closed tenant user cannot log in' bash -c '[[ "$1" = 401 || "$1" = 403 ]]' _ "$code"
check 'closed tenant API token is refused' credential_refused "$api"
check 'closed tenant key refuses a new publisher' publish_refused "$FOGHORN_A_DB" "$internal" "$EDGE_A_RTMP" "$key"
billing_cutoff_recorded() {
  [ "$(pg purser "SELECT COUNT(*) FROM purser.tenant_billing_closures WHERE tenant_id='$tenant'")" = 1 ]
}
billing_finalized() {
  [ "$(pg purser "SELECT COUNT(*) FROM purser.tenant_billing_closures c JOIN purser.tenant_subscriptions s ON s.tenant_id=c.tenant_id WHERE c.tenant_id='$tenant' AND c.finalized_at IS NOT NULL AND s.status='cancelled' AND s.next_billing_date IS NULL AND EXISTS(SELECT 1 FROM purser.billing_invoices i WHERE i.tenant_id=c.tenant_id AND i.period_end=c.closed_at AND i.status<>'draft')")" = 1 ]
}
eventually "$MAIL_BOUND_S" 'tenant closure reaches the durable billing consumer' billing_cutoff_recorded
eventually "$EXPIRY_BOUND_S" 'closed tenant billing finalizes at its recorded cutoff' billing_finalized
finish_trial
