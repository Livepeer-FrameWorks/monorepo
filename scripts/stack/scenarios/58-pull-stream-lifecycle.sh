#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, changes isolated cluster capability and ends its fixture buffer
# tenant: qa
# A direct HTTP TS pull emits one connected/live pair, renews its placement past
# thirty seconds, starts DVR, survives a sidecar restart and ends with idle
# when the upstream ends. A second burst creates a new session.
. "$(dirname "$0")/../lib.sh"
need ffmpeg jq curl docker psql python3 || finish
[ "${STACK_TARGET:-stack}" != staging ] || { blocked 'requires an isolated stack'; finish; }
SOURCE_PID='' PLAYER='' SID='' DVR='' PRIVATE_BEFORE='' RESTORE_PRIVATE=0
rpc_cluster() {
  local method=$1 body=$2
  printf '%s' "$body" | stack_exec commodore sh -c \
    'grpcurl -plaintext -max-time 10 -H "authorization: Bearer $SERVICE_TOKEN" -d @ "$QUARTERMASTER_GRPC_ADDR" "quartermaster.ClusterService/$1"' sh "$method"
}
set_private() { rpc_cluster UpdateCluster "$(jq -cn --arg id "$CELL_B_CLUSTER" --argjson allowed "$1" '{clusterId:$id,allowPrivatePullSources:$allowed}')" | jq -e --argjson allowed "$1" '.cluster.allowPrivatePullSources // false | .==$allowed' >/dev/null; }
cleanup() {
  [ -z "$PLAYER" ] || kill "$PLAYER" 2>/dev/null || true
  [ -z "$DVR" ] || gql 'mutation($id:ID!){stopDVR(dvrHash:$id){__typename}}' "$(jq -cn --arg id "$DVR" '{id:$id}')" >/dev/null
  [ -z "$SID" ] || delete_stream "$SID"
  [ -z "$SOURCE_PID" ] || kill "$SOURCE_PID" 2>/dev/null || true
  [ "$RESTORE_PRIVATE" = 0 ] || set_private "$PRIVATE_BEFORE" || fail 'restore pull-source cluster capability'
}
trap cleanup EXIT
snapshot=$(rpc_cluster GetCluster "$(jq -cn --arg id "$CELL_B_CLUSTER" '{clusterId:$id}')") || { fail 'read source cluster capability'; finish; }
PRIVATE_BEFORE=$(jq -r '.cluster.allowPrivatePullSources // false' <<<"$snapshot")
case "$PRIVATE_BEFORE" in true | false) ;; *) fail 'cluster returned no private-source capability'; finish ;; esac
RESTORE_PRIVATE=1
set_private true || { fail 'enable isolated private source fixture'; finish; }
webhook_endpoint >/dev/null || { fail 'create pull lifecycle webhook'; finish; }
SINCE=$(date +%s)
WORK="$STACK_STATE_DIR/pull-lifecycle-$SINCE"
mkdir -p "$WORK"
render_mp4 "$WORK/loop.mp4" 640x360 15 12 || { fail 'render direct pull source'; finish; }
python3 "$STACK_LIB_DIR/native-source.py" "$WORK/loop.mp4" >"$WORK/source.log" 2>&1 &
SOURCE_PID=$!
UPSTREAM='http://stack-runner:18948/loop.ts'
create_pull() {
  local response
  response=$(gql 'mutation($i:CreateStreamInput!){createStream(input:$i){__typename ... on Stream{id streamId playbackId} ... on ValidationError{message}}}' \
    "$(jq -cn --arg name "stack-pull-lifecycle-$SINCE" --arg source "$UPSTREAM" --arg cluster "$CELL_B_CLUSTER" '{i:{name:$name,record:false,liveVideoAbr:"OFF",ingestMode:"PULL",pullSource:{sourceUri:$source,enabled:true},sourceLocation:{mode:"RESTRICTED",clusters:[{clusterId:$cluster}]}}}')") || return 1
  SID=$(jq -r '.data.createStream.id // empty' <<<"$response")
  [ -n "$SID" ] || { echo "    pull create: $(jq -c '.data.createStream // .errors' <<<"$response")"; return 1; }
  UUID=$(jq -r '.data.createStream.streamId // empty' <<<"$response")
  PB=$(jq -r '.data.createStream.playbackId // empty' <<<"$response")
  sweep_ledger created "$SID" "$UUID"
}
eventually 90 'create a private HTTP TS pull in its permitted cluster' create_pull || finish
INTERNAL=$(internal_name_of "$UUID")
open_pull() { MEDIA=$(hls_session_url "http://$EDGE_B_HOST:8082/hls/$PB/index.m3u8"); [ -n "$MEDIA" ]; }
eventually 120 'pull opens real HLS playback' open_pull || finish
hls_poll "$MEDIA" >"$STACK_STATE_DIR/pull-lifecycle-player.log" &
PLAYER=$!
playing() { [ "$(hls_poll_state "$STACK_STATE_DIR/pull-lifecycle-player.log" 0)" = ok ]; }
eventually 45 'pull receives transport-stream media' playing || finish
session_live() {
  ROW=$(pg "$FOGHORN_B_DB" "SELECT id::text||'|'||buffer_pid::text FROM foghorn.pull_ingest_sessions
    WHERE tenant_id='$STACK_TENANT_ID' AND stream_id='$UUID' AND ended_at IS NULL AND admission_state='active'") || return 1
  [[ "$ROW" =~ ^[a-f0-9-]+\|[0-9]+$ ]]
}
eventually 60 'playable pull has one durable buffer session' session_live || finish
FIRST_ROW=$ROW
ingest_matches() {
  local state=$1 session=$2 response
  response=$(gql 'query($id:ID!){stream(id:$id){ingest{state sessionId resumableUntil}}}' "$(jq -cn --arg id "$SID" '{id:$id}')") || return 1
  json_has "$response" ".errors==null and .data.stream.ingest.state==\"$state\" and .data.stream.ingest.sessionId==\"$session\" and .data.stream.ingest.resumableUntil==null"
}
eventually 30 'pull exposes its live session without push resumption' ingest_matches LIVE "${FIRST_ROW%%|*}"
pair_once() {
  [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FILTER(WHERE event_type='stream.connected')||'|'||count(*) FILTER(WHERE event_type='stream.live') FROM foghorn.domain_event_outbox WHERE tenant_id='$STACK_TENANT_ID' AND aggregate_id='$UUID'")" = '1|1' ]
}
eventually 60 'pull emits exactly one connected and one live event' pair_once || finish
eventually 60 'connected event reaches the deployed webhook consumer' saw_event "$SINCE" stream.connected "tostring | contains(\"$UUID\")" || finish
eventually 60 'live event reaches the deployed webhook consumer' saw_event "$SINCE" stream.live "tostring | contains(\"$UUID\")" || finish
sleep 40
lease_fresh() {
  [ "$(pg commodore "SELECT count(*) FROM commodore.streams WHERE tenant_id='$STACK_TENANT_ID' AND id='$UUID'
    AND active_ingest_cluster_id='$CELL_B_CLUSTER' AND active_ingest_claim_id='pull:$UUID'
    AND active_ingest_cluster_updated_at>NOW()-INTERVAL '20 seconds'")" = 1 ]
}
eventually 30 'pull placement keeps renewing beyond the original lease' lease_fresh || finish
response=$(gql 'mutation($id:ID!){startDVR(streamId:$id){__typename ... on DVRRequest{dvrHash} ... on ValidationError{message}}}' "$(jq -cn --arg id "$SID" '{id:$id}')")
DVR=$(jq -r '.data.startDVR.dvrHash // empty' <<<"$response")
check 'DVR starts on a pull live for more than thirty seconds' [ -n "$DVR" ]
[ -n "$DVR" ] || { echo "    startDVR: $(jq -c '.data.startDVR // .errors' <<<"$response")"; finish; }
stack_exec "$STACK_EDGE_B_SERVICE" /command/s6-svc -r /run/service/helmsman || { fail 'restart pull sidecar'; finish; }
same_session() { session_live && [ "$ROW" = "$FIRST_ROW" ] && pair_once && playing; }
eventually 90 'Helmsman restart preserves the exact buffer and its event pair' same_session || finish
eventually 30 'the public pull session survives a sidecar restart' ingest_matches LIVE "${FIRST_ROW%%|*}"
response=$(gql 'mutation($id:ID!){stopDVR(dvrHash:$id){__typename}}' "$(jq -cn --arg id "$DVR" '{id:$id}')")
check 'DVR stop is acknowledged in the pull serving cell' json_has "$response" '.errors==null and .data.stopDVR.__typename=="DeleteSuccess"'
kill "$PLAYER" 2>/dev/null || true
PLAYER=''
kill "$SOURCE_PID" 2>/dev/null || true
wait "$SOURCE_PID" 2>/dev/null || true
SOURCE_PID=''
session_ended() {
  [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.pull_ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND stream_id='$UUID' AND ended_at IS NULL")" = 0 ]
}
eventually 90 'ended pull buffer closes its durable session' session_ended || finish
eventually 30 'the public pull session becomes idle without a resume window' ingest_matches IDLE "${FIRST_ROW%%|*}"
idle_once() { [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.domain_event_outbox WHERE tenant_id='$STACK_TENANT_ID' AND aggregate_id='$UUID' AND event_type='stream.idle'")" = 1 ]; }
eventually 60 'last direct buffer emits one idle event' idle_once || finish
eventually 60 'idle reaches the deployed webhook consumer' saw_event "$SINCE" stream.idle "tostring | contains(\"$UUID\")" || finish
python3 "$STACK_LIB_DIR/native-source.py" "$WORK/loop.mp4" >>"$WORK/source.log" 2>&1 &
SOURCE_PID=$!
eventually 120 'a second viewer burst starts the same pull again' open_pull || finish
hls_poll "$MEDIA" >"$STACK_STATE_DIR/pull-lifecycle-player-second.log" &
PLAYER=$!
second_playing() { [ "$(hls_poll_state "$STACK_STATE_DIR/pull-lifecycle-player-second.log" 0)" = ok ]; }
eventually 45 'second pull burst receives TS media' second_playing || finish
second_session() {
  session_live && [ "$ROW" != "$FIRST_ROW" ] &&
    [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FILTER(WHERE event_type='stream.connected')||'|'||count(*) FILTER(WHERE event_type='stream.live') FROM foghorn.domain_event_outbox WHERE tenant_id='$STACK_TENANT_ID' AND aggregate_id='$UUID'")" = '2|2' ]
}
eventually 60 'second burst has a new session and exactly one new event pair' second_session || finish
eventually 30 'the second burst exposes its new public session' ingest_matches LIVE "${ROW%%|*}"
kill "$PLAYER" 2>/dev/null || true
PLAYER=''
kill "$SOURCE_PID" 2>/dev/null || true
wait "$SOURCE_PID" 2>/dev/null || true
SOURCE_PID=''
eventually 90 'second buffer closes its session' session_ended || finish
python3 "$STACK_LIB_DIR/native-source.py" "$WORK/loop.mp4" >>"$WORK/source.log" 2>&1 &
SOURCE_PID=$!
check 'Free signup shares its three cell slots between real pushes and pulls' bash "$STACK_LIB_DIR/pull-capacity.sh" "$UPSTREAM"
finish
