#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, restarts both destination Foghorn replicas and Helmsman
# One cross-cell HLS viewer remains one audience member during local clip work,
# replica handoffs and a Helmsman restart. Native Mist, every cell replica and
# Periscope must agree; the origin's relay and processing reads are excluded.
# tenant: qa
. "$(dirname "$0")/../lib.sh"
need ffmpeg jq curl python3 docker psql || finish
[ "${STACK_TARGET:-stack}" != staging ] || { blocked 'requires an isolated stack'; finish; }
WORK="$STACK_STATE_DIR/audience-$(date +%s)"
mkdir -p "$WORK"
PUB='' VIEWER='' SID='' CID='' STOPPED=''
HELMSMAN_STOPPED=0
cleanup() {
  [ "$HELMSMAN_STOPPED" = 0 ] || stack_exec "$STACK_EDGE_B_SERVICE" /command/s6-svc -u /run/service/helmsman || fail 'restore serving Helmsman'
  [ -z "$STOPPED" ] || stack_ctl start "$STOPPED" || fail 'restore destination Foghorn'
  [ -z "$VIEWER" ] || kill "$VIEWER" 2>/dev/null
  [ -z "$PUB" ] || kill "$PUB" 2>/dev/null
  [ -z "$CID" ] || gql 'mutation($id:ID!){deleteClip(id:$id){__typename}}' "$(jq -cn --arg id "$CID" '{id:$id}')" >/dev/null
  [ -z "$SID" ] || delete_stream "$SID"
}
trap cleanup EXIT
S=$(create_stream "stack-audience-$(date +%s)" false OFF)
SID=$(jq -r '.id // empty' <<<"$S")
UUID=$(jq -r '.streamId // empty' <<<"$S")
KEY=$(jq -r '.streamKey // empty' <<<"$S")
[ -n "$SID" ] && [ -n "$KEY" ] || { fail 'create audience stream'; finish; }
IN=$(internal_name_of "$UUID")
PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$KEY" 1280x720 30 1800) || { fail 'admit origin publisher'; finish; }
NODE_A=$(stack_exec "$STACK_EDGE_A_SERVICE" printenv NODE_ID) || { fail 'read origin node identity'; finish; }
NODE_B=$(stack_exec "$STACK_EDGE_B_SERVICE" printenv NODE_ID) || { fail 'read serving node identity'; finish; }
[ -n "$NODE_A" ] && [ -n "$NODE_B" ] || { fail 'edge node identities are empty'; finish; }
REGISTRY="${FOGHORN_B_INTERNAL_URLS%% *}/debug/stream-registry?q=$IN&include=sources"
source_visible() {
  local response cell
  response=$(curl -sf -m 5 -H "Authorization: Bearer $SERVICE_TOKEN" "$REGISTRY") || return 1
  cell=$(jq -r '.local_cluster_id // empty' <<<"$response")
  [ -n "$cell" ] || return 1
  response=$(stack_exec foghorn-redis-b valkey-cli --raw get "{$cell}:registry:source:$IN") || return 1
  jq -es --arg cell "$cell" --arg tenant "$STACK_TENANT_ID" 'length == 1 and (.[0] | .TenantID == $tenant and ([.Locations | to_entries[] | select(.key != $cell and .value.IsLiveNow) | .value.EdgeCandidates[]? | select(.IsOrigin and .Playable and .DTSCURL != "")] | length > 0))' <<<"$response" >/dev/null
}
eventually 90 'serving cell sees a playable remote origin without opening a viewer' source_visible || finish
open_viewer() {
  MEDIA=$(hls_session_url "http://$EDGE_B_HOST:8082/hls/live+$IN/index.m3u8")
  [ -n "$MEDIA" ]
}
eventually 90 'one viewer opens directly on the serving edge' open_viewer || finish
hls_poll "$MEDIA" >"$WORK/viewer.log" &
VIEWER=$!
viewer_healthy() { [ "$(hls_poll_state "$WORK/viewer.log" 0)" = ok ]; }
eventually 30 'viewer receives actual TS segments' viewer_healthy || finish

# Credentials are supplied on stdin, and no failed RPC is interpreted as zero.
rpc_health() {
  local service=$1 node=$2
  printf '%s\n' "$SERVICE_TOKEN" | stack_exec commodore sh -c '
    read -r token
    grpcurl -plaintext -max-time 10 -H "authorization: Bearer $token" -d "$2" "$1:18019" foghorn.NodeControlService/GetNodeHealth
  ' sh "$service" "$(jq -cn --arg node "$node" '{nodeId:$node}')"
}
health_matches() {
  local response
  response=$(rpc_health "$1" "$2") || return 1
  if ! jq -e --argjson expected "$3" '.isHealthy and ((.activeViewers // 0)==$expected) and ((.lastHeartbeat | sub("\\.[0-9]+Z$";"Z") | fromdateiso8601) > (now-30))' <<<"$response" >/dev/null; then
    echo "    $1 health mismatch: $(jq -c '{nodeId,isHealthy,activeViewers,lastHeartbeat}' <<<"$response")"
    return 1
  fi
}
native_matches() {
  local response
  response=$(python3 "$STACK_LIB_DIR/node_audience.py" "$(container_of "$1")") || return 1
  if ! jq -e --arg name "$IN" --argjson expected "$2" '.audience==$expected and ((.streams[$name] // 0)==$expected)' <<<"$response" >/dev/null; then
    echo "    $1 native mismatch: $response"
    return 1
  fi
}
counts_match() {
  local service
  native_matches "$STACK_EDGE_A_SERVICE" 0 && native_matches "$STACK_EDGE_B_SERVICE" 1 || return 1
  for service in foghorn foghorn-2; do health_matches "$service" "$NODE_A" 0 || return 1; done
  for service in foghorn-b foghorn-b-2; do
    [ "$service" = "$STOPPED" ] || health_matches "$service" "$NODE_B" 1 || return 1
  done
}
network_matches() {
  local response
  response=$(gql 'query{networkStatus{clusters{clusterId currentViewers egressMbps}}}') || return 1
  if ! jq -e --arg origin "$CELL_A_CLUSTER" --arg serving "$CELL_B_CLUSTER" '.errors==null and ([.data.networkStatus.clusters[] | select(.clusterId==$origin)] | length==1 and .[0].currentViewers==0) and ([.data.networkStatus.clusters[] | select(.clusterId==$serving)] | length==1 and .[0].currentViewers==1 and .[0].egressMbps>0)' <<<"$response" >/dev/null; then
    echo "    attribution mismatch: $(jq -c '{errors,clusters:.data.networkStatus.clusters}' <<<"$response")"
    return 1
  fi
}
eventually 90 'native Mist and every Foghorn agree on origin zero / serving one' counts_match || finish
eventually 90 'Periscope attributes the viewer and positive egress to its serving cluster' network_matches || finish
STRICT_SINCE=$(date +%s)

# A clip opens a processing read on the origin; it must never create an audience.
request_clip() {
  local now response
  now=$(date +%s)
  response=$(gql 'mutation($i:CreateClipInput!){createClip(input:$i){__typename ... on Clip{id} ... on ValidationError{message}}}' "$(jq -cn --arg s "$SID" --argjson start $((now-20)) '{i:{streamId:$s,title:"audience processing proof",mode:"DURATION",startUnix:$start,duration:10}}')") || return 1
  CID=$(jq -r '.data.createClip.id // empty' <<<"$response")
  [ -n "$CID" ] && return 0
  grep -q 'no source with at least one whole second' <<<"$response" && return 1
  echo "    createClip: $(jq -c '.data.createClip // .errors' <<<"$response")"
  return 2
}
eventually 90 'clip processing starts alongside the one viewer' request_clip || finish
for sample in 1 2 3 4; do
  check "sample $sample: processing and relay reads are absent from audience" counts_match
  check "sample $sample: serving-cluster attribution remains one" network_matches
  sleep 10
done
clip_ready() {
  local response status
  response=$(gql 'query($id:ID!){node(id:$id){... on Clip{status}}}' "$(jq -cn --arg id "$CID" '{id:$id}')") || return 1
  status=$(jq -r '.data.node.status // empty' <<<"$response")
  case "$status" in ready | READY | done | DONE) return 0 ;; failed | FAILED) return 2 ;; esac
  return 1
}
eventually 180 'clip finishes without an artifact playback viewer' clip_ready || finish

owner() {
  local response cell base
  for base in $FOGHORN_B_INTERNAL_URLS; do
    response=$(curl -sf -m 5 -H "Authorization: Bearer $SERVICE_TOKEN" "$base/debug/stream-registry?q=$IN&include=sources") || continue
    cell=$(jq -r '.local_cluster_id // empty' <<<"$response")
    [ -n "$cell" ] || continue
    stack_exec foghorn-redis-b valkey-cli --raw get "{$cell}:conn_owner:$NODE_B"
    return $?
  done
  return 1
}
# Each handoff stops the current owner, proves a higher fence on its peer, then
# restarts it. The two rounds exercise both destination replicas.
for round in 1 2; do
  BEFORE=$(owner) || { fail 'read current control owner'; finish; }
  IFS='|' read -r OLD_ID ADDRESS OLD_FENCE <<<"$BEFORE"
  SERVICE=${ADDRESS%:*}
  case "$SERVICE" in foghorn-b | foghorn-b-2) ;; *) fail 'control owner is outside this fixture'; finish ;; esac
  [[ "$OLD_FENCE" =~ ^[0-9]+$ ]] || { fail 'control owner has no numeric fence'; finish; }
  STOPPED=$SERVICE
  stack_ctl stop "$STOPPED" || { fail 'stop current destination owner'; finish; }
  moved() {
    local value id address fence
    value=$(owner) || return 1
    IFS='|' read -r id address fence <<<"$value"
    [[ "$fence" =~ ^[0-9]+$ ]] && [ -n "$id" ] && [ "$id" != "$OLD_ID" ] && [ "$fence" -gt "$OLD_FENCE" ]
  }
  eventually 90 "handoff $round acquires a higher control fence" moved || finish
  eventually 60 "handoff $round keeps native and surviving replica counts equal" counts_match || finish
  # Periscope projects the fresh node census through the analytics pipeline.
  eventually 60 "handoff $round keeps serving-cluster attribution" network_matches || finish
  stack_ctl start "$STOPPED" || { fail 'restart destination owner'; finish; }
  STOPPED=''
  eventually 90 "restart $round rehydrates one viewer on both replicas" counts_match || finish
done
RESTART_AT=$(monotonic_ms)
OUTAGE_FROM=$(date +%s)
HELMSMAN_STOPPED=1
stack_exec "$STACK_EDGE_B_SERVICE" /command/s6-svc -d /run/service/helmsman || { fail 'stop serving Helmsman'; finish; }
viewer_during_restart() {
  [ "$(hls_poll_state "$WORK/viewer.log" "$STRICT_SINCE")" = ok ] &&
    awk -v since="$OUTAGE_FROM" '$1>since && $2=="ok" { n++ } END { exit (n<3) }' "$WORK/viewer.log"
}
within_bound_since "$RESTART_AT" 30 'the admitted viewer keeps reading TS while Helmsman is stopped' viewer_during_restart || finish
stack_exec "$STACK_EDGE_B_SERVICE" /command/s6-svc -u /run/service/helmsman || { fail 'start serving Helmsman'; finish; }
HELMSMAN_STOPPED=0
restarted_counts() {
  local response
  response=$(rpc_health foghorn-b "$NODE_B") || return 1
  jq -e --argjson since "$OUTAGE_FROM" '(.lastHeartbeat | sub("\\.[0-9]+Z$";"Z") | fromdateiso8601)>$since' <<<"$response" >/dev/null && counts_match
}
within_bound_since "$RESTART_AT" 90 'Helmsman restart sends a fresh heartbeat and preserves the native audience census' restarted_counts || finish
HEALTHY_AT=$(date +%s)
viewer_media_recovers() {
  awk -v since="$HEALTHY_AT" '$1>since { last=$2 } END { exit (last!="ok") }' "$WORK/viewer.log"
}
within_bound_since "$RESTART_AT" 90 'the same viewer receives TS bytes after the fresh control report' viewer_media_recovers || finish
OUTAGE_END=$(date +%s)
viewer_continues() {
  [ "$(hls_restart_continuity_state "$WORK/viewer.log" "$STRICT_SINCE" "$OUTAGE_FROM" "$OUTAGE_END")" = ok ]
}
within_bound_since "$RESTART_AT" 90 'the same HLS viewer keeps reading TS with zero failures across the restart' viewer_continues || finish
check 'final Periscope serving-cluster attribution remains one' network_matches
finish
