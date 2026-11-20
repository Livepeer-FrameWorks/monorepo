#!/usr/bin/env bash
# A fresh Free account exercises the shared push/pull cap using real encoders.
. "$(dirname "$0")/lib.sh"
need ffmpeg jq curl python3 openssl psql || finish
UPSTREAM=$1
run="pull-cap-$(date +%s)-$RANDOM"
export STACK_STATE_DIR="$STACK_STATE_DIR/$run"
mkdir -p "$STACK_STATE_DIR"
email=$(test_email "$run")
password="Stack-$(openssl rand -hex 8)"
streams=() publishers=() player=''
cleanup() {
  local pid id
  [ -z "$player" ] || kill "$player" 2>/dev/null || true
  for pid in "${publishers[@]}"; do kill "$pid" 2>/dev/null || true; done
  for id in "${streams[@]}"; do delete_stream "$id"; done
}
trap cleanup EXIT
code=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" '{email:$e,password:$p,first_name:"Stack",last_name:"Pull"}+$b' |
  curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/register")
check 'Free cap account registers' [ "$code" = 201 ]
[ "$code" = 201 ] || finish
eventually 60 'Free cap verification email arrives' mail_seen "$email" || finish
code=$(jq -cn --arg t "$(mail_token "$email")" '{token:$t}' |
  curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/verify")
check 'Free cap account verifies' [ "$code" = 200 ]
[ "$code" = 200 ] || finish
code=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" '{email:$e,password:$p}+$b' |
  curl -s -m 20 -c "$STACK_STATE_DIR/cookies" -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login")
check 'Free cap account logs in' [ "$code" = 200 ]
[ "$code" = 200 ] || finish
STACK_API_TOKEN=$(awk '$6=="access_token"{print $7}' "$STACK_STATE_DIR/cookies" | tail -1)
[ -n "$STACK_API_TOKEN" ] || { fail 'Free cap login has no session'; finish; }
STACK_TENANT_ID=$(python3 - "$STACK_API_TOKEN" <<'PY'
import base64, json, sys
payload = sys.argv[1].split('.')[1]
print(json.loads(base64.urlsafe_b64decode(payload+'='*(-len(payload)%4)))['tenant_id'])
PY
)
export STACK_API_TOKEN STACK_TENANT_ID
free_plan() {
  [ "$(pg purser "SELECT bt.tier_name||'|'||e.value::text FROM purser.tenant_subscriptions ts JOIN purser.billing_tiers bt ON bt.id=ts.tier_id JOIN purser.tier_entitlements e ON e.tier_id=bt.id AND e.key='max_concurrent_streams' WHERE ts.tenant_id='$STACK_TENANT_ID' AND ts.status='active'")" = 'free|3' ]
}
eventually 90 'fresh Free subscription has the ordinary three-stream cap' free_plan || finish
create_cap_stream() {
  local mode=$1 response input
  input=$(jq -cn --arg n "$run-$2" --arg mode "$mode" --arg uri "$UPSTREAM" --arg cluster "$CELL_B_CLUSTER" \
    '{i:({name:$n,record:false,liveVideoAbr:"OFF",ingestMode:$mode,sourceLocation:{mode:"RESTRICTED",clusters:[{clusterId:$cluster}]}} + (if $mode=="PULL" then {pullSource:{sourceUri:$uri,enabled:true}} else {} end))}')
  response=$(gql 'mutation($i:CreateStreamInput!){createStream(input:$i){__typename ... on Stream{id streamId playbackId streamKey} ... on ValidationError{message}}}' "$input")
  CREATED=$(jq -c '.data.createStream' <<<"$response")
  local id uuid
  id=$(jq -r '.id // empty' <<<"$CREATED")
  uuid=$(jq -r '.streamId // empty' <<<"$CREATED")
  [ -n "$id" ] || { echo "    cap create: $CREATED"; return 1; }
  streams+=("$id")
  sweep_ledger created "$id" "$uuid"
}
for index in 1 2 3; do
  create_cap_stream PUSH "$index" || { fail 'create cap publisher'; finish; }
  key=$(jq -r '.streamKey' <<<"$CREATED")
  internal=$(internal_name_of_key "$key")
  [ "$index" != 1 ] || first_internal=$internal
  pid=$(publish_until_admitted "$EDGE_B_RTMP" "$key" 320x180 10 900) || { fail "admit cap publisher $index"; finish; }
  publishers+=("$pid")
  pass "real Free publisher $index is admitted"
done
create_cap_stream PULL fourth || { fail 'create cap pull'; finish; }
pull_uuid=$(jq -r '.streamId' <<<"$CREATED")
pull_public=$(jq -r '.playbackId' <<<"$CREATED")
pull_url="http://$EDGE_B_HOST:8082/hls/$pull_public/index.m3u8"
# Trigger the native source dial; offline responses may be HTTP 200 in Mist.
curl -s -m 15 "$pull_url" >/dev/null || true
cap_refused() {
  [ "$(pg commodore "SELECT count(*) FROM commodore.pull_source_events WHERE tenant_id='$STACK_TENANT_ID' AND stream_id='$pull_uuid' AND event_kind='stream_cap'")" -gt 0 ]
}
eventually 60 'fourth pull reports stream_cap instead of dialing its upstream' cap_refused || finish
check 'refused pull emits no connected event' test "$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.domain_event_outbox WHERE tenant_id='$STACK_TENANT_ID' AND aggregate_id='$pull_uuid' AND event_type='stream.connected'")" = 0
check 'refused pull has no active buffer session' test "$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.pull_ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND stream_id='$pull_uuid' AND ended_at IS NULL")" = 0
kill "${publishers[0]}" 2>/dev/null || true
first_ended() { [ -z "$(open_generation "$FOGHORN_B_DB" "$first_internal")" ]; }
eventually 60 'one stopped publisher frees its cell slot' first_ended || finish
pull_opens() { MEDIA=$(hls_session_url "$pull_url") && [ -n "$MEDIA" ]; }
eventually 120 'the same refused pull plays after a slot is freed' pull_opens || finish
hls_poll "$MEDIA" >"$STACK_STATE_DIR/player.log" &
player=$!
pull_playing() { [ "$(hls_poll_state "$STACK_STATE_DIR/player.log" 0)" = ok ]; }
eventually 45 'the admitted cap pull receives real TS media' pull_playing || finish
pull_pair() { [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FILTER(WHERE event_type='stream.connected')||'|'||count(*) FILTER(WHERE event_type='stream.live') FROM foghorn.domain_event_outbox WHERE tenant_id='$STACK_TENANT_ID' AND aggregate_id='$pull_uuid'")" = '1|1' ]; }
eventually 60 'admitted pull emits exactly one connected/live pair' pull_pair || finish
create_cap_stream PUSH fifth || { fail 'create over-cap push'; finish; }
key=$(jq -r '.streamKey' <<<"$CREATED")
internal=$(internal_name_of_key "$key")
since=$(utc_now)
pid=$(publish "$EDGE_B_RTMP" "$key" 320x180 10 30)
publishers+=("$pid")
push_cap_refused() {
  logs_since "$since" "$STACK_EDGE_B_SERVICE" | grep 'INGEST_ERROR_TENANT_STREAM_CAP' >/dev/null
}
eventually 45 'a pull consumes the slot and the next real push gets TENANT_STREAM_CAP' push_cap_refused || finish
check 'over-cap push has no open generation' test -z "$(open_generation "$FOGHORN_B_DB" "$internal")"
finish
