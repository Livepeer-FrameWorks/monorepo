#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, creates a tenant and debits its prepaid balance through the operator CLI
# A prepaid tenant spends its balance while it streams. At EUR 0 the running
# publisher and its viewer keep going, a new viewer is refused and a stopped
# publisher cannot publish again. Below -EUR 10 the tenant is suspended and
# its running sessions stop.
# Measures: the zero crossing blocks only new sessions; the -EUR 10 threshold
# ends the sessions that were running.
# Catches: a spent balance that ends running sessions, or that still admits new
# viewers or a re-publish; a suspension that leaves sessions running.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 psql openssl || finish
[ -x "$STACK_REPO/bin/cli" ] || { blocked 'operator CLI binary missing; make build-bin-cli'; finish; }
# shellcheck source=scripts/stack/cli-context.sh
. "$STACK_LIB_DIR/cli-context.sh"
CLI="$STACK_REPO/bin/cli"
run="spent50-$(date +%s)-$RANDOM"
export STACK_STATE_DIR="$STACK_STATE_DIR/$run"
mkdir -p "$STACK_STATE_DIR"
cli_json() {
  local output
  if output=$("$CLI" "$@" 2>"$STACK_STATE_DIR/cli.stderr"); then
    printf '%s' "$output"
  else
    cat "$STACK_STATE_DIR/cli.stderr" >&2
    return 1
  fi
}

KEPT_PUB="" STOPPED_PUB="" KEPT_POLL="" KEPT_SID="" STOPPED_SID=""
cleanup() {
  kill "$KEPT_PUB" "$STOPPED_PUB" "$KEPT_POLL" 2>/dev/null
  [ -z "$KEPT_SID" ] || delete_stream "$KEPT_SID"
  [ -z "$STOPPED_SID" ] || delete_stream "$STOPPED_SID"
  # The tenant is disposable with this stack's volumes, like scenario 27's.
  return 0
}
trap cleanup EXIT

log 'sign up, verify and move the tenant to prepaid with EUR 10'
email=$(test_email "$run")
password="Stack-$(openssl rand -hex 8)"
cookies="$STACK_STATE_DIR/cookies"
code=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" \
  '{email:$e,password:$p,first_name:"Stack",last_name:"Spent"} + $b' |
  curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/register")
check 'tenant registration accepted' [ "$code" = 201 ]
[ "$code" = 201 ] || finish
eventually 60 'verification email delivered' mail_seen "$email" || finish
code=$(jq -cn --arg t "$(mail_token "$email")" '{token:$t}' |
  curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/verify")
check 'verification accepted' [ "$code" = 200 ]
code=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" '{email:$e,password:$p} + $b' |
  curl -s -m 20 -c "$cookies" -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login")
check 'tenant login accepted' [ "$code" = 200 ]
STACK_API_TOKEN=$(awk '$6=="access_token"{print $7}' "$cookies" | tail -1)
[ -n "$STACK_API_TOKEN" ] || { fail 'login returned no session'; finish; }
STACK_TENANT_ID=$(python3 - "$STACK_API_TOKEN" <<'PY'
import base64, json, sys
p = sys.argv[1].split('.')[1]
print(json.loads(base64.urlsafe_b64decode(p + '=' * (-len(p) % 4))).get('tenant_id', ''))
PY
)
[ -n "$STACK_TENANT_ID" ] || { fail 'session JWT has no tenant_id'; finish; }
export STACK_API_TOKEN STACK_TENANT_ID
tenant=$STACK_TENANT_ID
refresh() { STACK_API_TOKEN=$(refresh_session_token "$cookies") && export STACK_API_TOKEN; }

cli_json admin billing set-tier --tenant-id "$tenant" --tier payg --billing-model prepaid \
  --reason 'stack spent prepaid test' --output json >/dev/null || { fail 'operator assigned prepaid tier'; finish; }
pass 'operator assigned prepaid tier'
cli_json admin billing credit --tenant-id "$tenant" --amount 10.00 --reason 'stack spent prepaid test credit' \
  --output json >/dev/null || { fail 'operator credited EUR 10'; finish; }
balance() { pg purser "SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id='$tenant' AND currency='EUR'"; }
check 'prepaid balance is EUR 10' [ "$(balance)" = 1000 ]

log 'two funded streams publish; one has a viewer'
refresh || { fail 'session refresh'; finish; }
S=$(create_stream "stack-$run-kept" false)
KEPT_SID=$(echo "$S" | jq -r '.id // empty')
KEPT_PB=$(echo "$S" | jq -r '.playbackId // empty')
KEPT_KEY=$(echo "$S" | jq -r '.streamKey // empty')
KEPT_IN=$(internal_name_of "$(echo "$S" | jq -r '.streamId // empty')")
S=$(create_stream "stack-$run-stopped" false)
STOPPED_SID=$(echo "$S" | jq -r '.id // empty')
STOPPED_PB=$(echo "$S" | jq -r '.playbackId // empty')
STOPPED_KEY=$(echo "$S" | jq -r '.streamKey // empty')
STOPPED_IN=$(internal_name_of "$(echo "$S" | jq -r '.streamId // empty')")
[ -n "$KEPT_IN" ] && [ -n "$STOPPED_IN" ] || { fail "createStream: $S"; finish; }
KEPT_PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$KEPT_KEY" 640x360 15 1800) || { fail 'funded publisher admitted'; finish; }
pass 'funded publisher admitted'
STOPPED_PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$STOPPED_KEY" 640x360 15 1800) ||
  { fail 'second funded publisher admitted'; finish; }
pass 'second funded publisher admitted'
viewer_opened() {
  local loc
  loc=$(location_at "$FOGHORN_A_URL" "$KEPT_PB")
  [ -n "$loc" ] || return 1
  KEPT_VARIANT=$(hls_session_url "$loc")
  [ -n "$KEPT_VARIANT" ]
}
eventually 120 'funded viewer opens a session' viewer_opened || finish
(hls_poll "$KEPT_VARIANT" >"$STACK_STATE_DIR/kept-viewer.log") &
KEPT_POLL=$!
T0=$(date +%s)
sleep 8
check 'funded viewer plays media' [ "$(hls_poll_state "$STACK_STATE_DIR/kept-viewer.log" "$T0")" = ok ]
check 'a second viewer is admitted while funded' media_at "$FOGHORN_A_URL" "$STOPPED_PB"

log 'debit to EUR 0: running sessions continue, new ones are refused'
cli_json admin billing credit --tenant-id "$tenant" --amount=-10.00 --debit --reason 'stack spent prepaid test debit' \
  --output json >/dev/null || { fail 'operator debited EUR 10'; finish; }
spent() { [ "$(balance)" -le 0 ]; }
check 'prepaid balance is spent' spent
new_viewer_refused() { ! media_at "$FOGHORN_A_URL" "$STOPPED_PB"; }
eventually 180 'a new viewer is refused at EUR 0' new_viewer_refused
T1=$(date +%s)
sleep 20
check 'the running publisher is still admitted at EUR 0' [ -n "$(open_generation "$FOGHORN_A_DB" "$KEPT_IN")" ]
check 'the running publisher process is still sending' kill -0 "$KEPT_PUB"
check 'the running viewer keeps playing at EUR 0' [ "$(hls_poll_state "$STACK_STATE_DIR/kept-viewer.log" "$T1")" = ok ]

kill "$STOPPED_PUB" 2>/dev/null
STOPPED_PUB=""
stopped_ended() { [ -z "$(open_generation "$FOGHORN_A_DB" "$STOPPED_IN")" ]; }
eventually 60 'the stopped publisher session ends' stopped_ended
check 'a re-publish is refused at EUR 0' publish_refused "$FOGHORN_A_DB" "$STOPPED_IN" "$EDGE_A_RTMP" "$STOPPED_KEY"

log 'debit below -EUR 10: the tenant is suspended and its sessions stop'
cli_json admin billing credit --tenant-id "$tenant" --amount=-10.01 --debit --reason 'stack spent prepaid test suspension' \
  --output json >/dev/null || { fail 'operator debited past -EUR 10'; finish; }
suspended() { [ "$(pg purser "SELECT count(*) FROM purser.tenant_subscriptions WHERE tenant_id='$tenant' AND status='suspended'")" -ge 1 ]; }
eventually 60 'the tenant is suspended below -EUR 10' suspended
kept_ended() { [ -z "$(open_generation "$FOGHORN_A_DB" "$KEPT_IN")" ]; }
eventually 120 'the running publisher session stops below -EUR 10' kept_ended
viewer_stopped() { [ "$(hls_poll_state "$STACK_STATE_DIR/kept-viewer.log" "$(($(date +%s) - 6))")" = fail ]; }
eventually 120 'the running viewer stops below -EUR 10' viewer_stopped

finish
