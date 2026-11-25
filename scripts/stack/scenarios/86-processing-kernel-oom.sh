#!/usr/bin/env bash
# stack-scenario: default
# tenant: qa
# staging: unsafe, bounds edge memory and deliberately causes an owned processor's kernel OOM
. "$(dirname "$0")/../lib.sh"
need ffmpeg jq curl python3 docker || finish
[ "${STACK_TARGET:-stack}" != staging ] || { blocked 'requires isolated edges'; finish; }
limits=() VOD_ID='' GATEWAY_STOPPED=''
GATEWAY=${STACK_LIVEPEER_GATEWAY_A:-livepeer-gateway-a}
restore_limits() {
  local row container memory swap
  for row in "${limits[@]}"; do
    container=$(jq -r '.container' <<<"$row")
    memory=$(jq -r '.memory' <<<"$row")
    swap=$(jq -r '.swap' <<<"$row")
    docker update --memory "$memory" --memory-swap "$swap" "$container" >/dev/null || fail 'restore edge memory limits'
  done
  limits=()
}
cleanup() {
  restore_limits
  [ -z "$GATEWAY_STOPPED" ] || stack_ctl start "$GATEWAY" >/dev/null || true
  [ -z "$VOD_ID" ] || gql 'mutation($id:ID!){deleteVodAsset(id:$id){__typename}}' "$(jq -cn --arg id "$VOD_ID" '{id:$id}')" >/dev/null
}
trap cleanup EXIT
for service in "$STACK_EDGE_A_SERVICE" "$STACK_EDGE_B_SERVICE"; do
  container=$(container_of "$service")
  [ -n "$container" ] || { fail 'resolve isolated edge'; finish; }
  row=$(docker inspect --format '{{json .HostConfig}}' "$container" | jq -c --arg c "$container" '{container:$c,memory:.Memory,swap:.MemorySwap}') || finish
  limits+=("$row")
  docker update --memory 1g --memory-swap 1g "$container" >/dev/null || { fail 'bound edge memory for OOM fixture'; finish; }
done
bounded_budgets_reported() {
  local pair service database node budget
  for pair in "$STACK_EDGE_A_SERVICE|$FOGHORN_A_DB" "$STACK_EDGE_B_SERVICE|$FOGHORN_B_DB"; do
    service=${pair%|*} database=${pair#*|}
    node=$(stack_exec "$service" printenv NODE_ID) || return 1
    budget=$(pg "$database" "SELECT (lifecycle->>'processing_memory_budget_bytes')::bigint FROM foghorn.node_lifecycle WHERE node_id='$node'") || return 1
    [ -n "$budget" ] && [ "$budget" -le 1073741824 ] || return 1
  done
}
eventually 90 'edges report their bounded processing budgets' bounded_budgets_reported || finish
stack_ctl stop "$GATEWAY" || { fail 'stop gateway to exercise native AV'; finish; }
GATEWAY_STOPPED=1
DRAIN_AT=$(($(date +%s)+65))
gateway_absent() {
  [ "$(date +%s)" -ge "$DRAIN_AT" ] &&
    [ "$(pg quartermaster "SELECT COALESCE(health_status,'') FROM quartermaster.service_instances WHERE instance_id='stack-livepeer-gateway-a'")" != healthy ]
}
eventually 180 'native fallback sees the unavailable gateway' gateway_absent || finish
render_mp4 "$STACK_STATE_DIR/oom-job.mp4" 1280x720 30 180 || { fail 'render finite OOM job input'; finish; }
result=$(upload_vod "$STACK_STATE_DIR/oom-job.mp4" 'stack actual kernel OOM recovery' '{"keepOriginal":false,"transcodeLadder":"standard"}') || finish
VOD_ID=$(jq -r '.id // empty' <<<"$result")
HASH=$(jq -r '.artifactHash // empty' <<<"$result")
PLAYBACK=$(jq -r '.playbackId // empty' <<<"$result")
[ -n "$VOD_ID" ] && [ -n "$HASH" ] && [ -n "$PLAYBACK" ] || { fail 'create actual OOM processing job'; finish; }
running_job() {
  local row database node
  for database in "$FOGHORN_A_DB" "$FOGHORN_B_DB"; do
    row=$(pg "$database" "SELECT jsonb_build_object('node',processing_node_id,'reserved',memory_reserved_bytes,'status',status) FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$HASH' ORDER BY created_at DESC LIMIT 1") || return 1
    [ -n "$row" ] || continue
    jq -e '.status=="processing" and .reserved>0 and (.node|length)>0' <<<"$row" >/dev/null || return 1
    node=$(jq -r '.node' <<<"$row")
    for service in "$STACK_EDGE_A_SERVICE" "$STACK_EDGE_B_SERVICE"; do
      if [ "$(stack_exec "$service" printenv NODE_ID)" = "$node" ]; then
        EDGE_CONTAINER=$(container_of "$service") || return 1
        [ -n "$EDGE_CONTAINER" ] || return 1
        EDGE_SERVICE=$service
        JOB_DB=$database ADMITTED=$(jq -r '.reserved' <<<"$row")
        return 0
      fi
    done
  done
  return 1
}
eventually 90 'actual VOD job receives native processing admission' running_job || finish
if ! python3 "$STACK_LIB_DIR/processing_oom_pressure.py" "$EDGE_CONTAINER" "processing+$HASH" "$STACK_STATE_DIR/kernel-oom-proof.json"; then
  fail 'kernel kills an owned native AV processor while its services survive'
  finish
fi
pass 'kernel killed the real native processor; controller and sidecar survived'
sweep_allow host "container $EDGE_SERVICE" '^OOM-killed$' \
  'owned native processor kernel kill recorded in kernel-oom-proof.json; controller and sidecar births unchanged'
# The host is shared by the default and QA tenant sweeps.
if [[ "$STACK_STATE_DIR" = */qa-tenant ]]; then
  (STACK_STATE_DIR=${STACK_STATE_DIR%/qa-tenant};
   sweep_allow host "container $EDGE_SERVICE" '^OOM-killed$' \
     'owned native processor kernel kill recorded in QA kernel-oom-proof.json; controller and sidecar births unchanged')
fi
restore_limits
memory_retry_recorded() {
  local row
  row=$(pg "$JOB_DB" "SELECT jsonb_build_object('floor',memory_floor_bytes,'retries',retry_count,'status',status) FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$HASH' ORDER BY created_at DESC LIMIT 1") || return 1
  [ -n "$row" ] || return 1
  jq -e '.status!="failed" and .retries==0' <<<"$row" >/dev/null || { log "$row"; return 2; }
  jq -e --argjson admitted "$ADMITTED" '.floor>$admitted' <<<"$row" >/dev/null
}
eventually 60 'OOM requeues the actual job with a larger floor and zero spent retries' memory_retry_recorded || finish
ready_or_failed() { local status; status=$(vod_status "$VOD_ID"); [ "$status" = READY ] && return 0; [ "$status" = FAILED ] && return 2; return 1; }
eventually 900 'actual OOM job retries to READY' ready_or_failed || finish
recovered_decodes() {
  local url
  url=$(location_at "$1" "$PLAYBACK") || return 1
  [ -n "$url" ] || return 1
  python3 "$STACK_LIB_DIR/decode_asset.py" "$url" 180 30
}
for replica in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
  eventually 60 "OOM-recovered recording decodes all requested frames via $replica" recovered_decodes "$replica"
done
check 'successful OOM recovery spends no retries' test "$(pg "$JOB_DB" "SELECT retry_count FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$HASH' ORDER BY created_at DESC LIMIT 1")" = 0
check 'edge has only the single deliberate kernel kill' test \
  "$(docker exec "$EDGE_CONTAINER" awk '$1=="oom_kill"{print $2}' /sys/fs/cgroup/memory.events)" = \
  "$(jq -r '.final_oom_kills' "$STACK_STATE_DIR/kernel-oom-proof.json")"
finish
