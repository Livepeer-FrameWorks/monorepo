#!/usr/bin/env bash
# stack-scenario: default
# tenant: qa
# staging: unsafe, changes ladders, stops Livepeer and publishes a stream
. "$(dirname "$0")/../lib.sh"
need ffmpeg jq curl python3 || finish
SID='' PUB='' GATEWAY_STOPPED=''
GATEWAY=${STACK_LIVEPEER_GATEWAY_A:-livepeer-gateway-a}
ORCHESTRATOR=${STACK_LIVEPEER_ORCHESTRATOR_A:-livepeer-orch-a}
ORCHESTRATOR_B=${STACK_LIVEPEER_ORCHESTRATOR_B:-livepeer-orch-b}
GATEWAY_B=${STACK_LIVEPEER_GATEWAY_B:-livepeer-gateway-b}
policy=$(gql 'query{transcodeLadderPolicy{tenantLadder effectiveLadder ladders{name}}}')
prior=$(echo "$policy" | jq -r '.data.transcodeLadderPolicy.tenantLadder // ""')
set_default() { gql 'mutation($ladder:String!){setTranscodeLadderPolicy(input:{ladder:$ladder}){__typename}}' "$(jq -cn --arg ladder "$1" '{ladder:$ladder}')"; }
cleanup() { [ -z "$PUB" ] || kill "$PUB" 2>/dev/null; [ -z "$SID" ] || delete_stream "$SID"; set_default "$prior" >/dev/null; }
trap cleanup EXIT
set_default '' | jq -e '.data.setTranscodeLadderPolicy.__typename=="TranscodeLadderPolicy"' >/dev/null || { fail 'inherit the plan ladder for this test'; finish; }

set_ladder() { gql 'mutation($name:String!){setTranscodeLadderPolicy(input:{ladder:$name}){__typename ... on TranscodeLadderPolicy{effectiveLadder}}}' "$(jq -cn --arg name "$1" '{name:$name}')"; }
cleanup() { [ -z "$PUB" ] || kill "$PUB" 2>/dev/null; [ -z "$SID" ] || delete_stream "$SID"; [ -z "$GATEWAY_STOPPED" ] || stack_ctl start "$GATEWAY"; set_ladder "$prior" >/dev/null; }
trap cleanup EXIT
set_ladder '' | jq -e '.data.setTranscodeLadderPolicy.effectiveLadder=="standard"' >/dev/null || { fail 'restore standard inheritance'; finish; }
ready() {
  local status
  status=$(vod_status "$1")
  [ "$status" = READY ] && return 0
  [ "$status" = FAILED ] && return 2
  return 1
}
recorded_ceiling() {
  pg "$FOGHORN_A_DB" "SELECT tracks::text FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1'" |
    jq -e --argjson height "$2" '[.[] | select((.track_type // .trackType // .type)=="video" and (.codec|ascii_downcase)!="jpeg") | ([.width,.height]|min)] | length>0 and (max-$height|fabs)<=32' >/dev/null
}
media_runtime_state() {
  local service id mode=${1:-all}
  for service in edge edge-b "$ORCHESTRATOR" "$ORCHESTRATOR_B" "$GATEWAY" "$GATEWAY_B"; do
    [ "$mode" != local ] || [ "$service" != "$GATEWAY" ] || continue
    id=$(docker compose -p "$COMPOSE_PROJECT_NAME" ps --all -q "$service" | head -1) || return 1
    [ -n "$id" ] || return 1
    docker inspect --format '{{.Id}} {{.State.OOMKilled}} {{.RestartCount}} {{.State.Status}} {{.State.StartedAt}} {{.HostConfig.Memory}}' "$id" || return 1
  done
}
livepeer_produced() {
  local processes rows quoted
  processes=$(pg "$FOGHORN_A_DB" "SELECT processes_json FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1' ORDER BY created_at DESC LIMIT 1") || return 1
  json_has "$processes" 'any(.[]; .process=="Livepeer")' || return 1
  quoted=$(jq -cn --arg stream "processing+$1" '$stream') || return 1
  rows=$(log_json "$2" "select(.trigger_type==\"LIVEPEER_SEGMENT_COMPLETE\" and .stream_name==$quoted and (.rendition_count|tonumber)>0)" edge edge-b) || return 1
  [ -n "$rows" ]
}
native_worker_budget() {
  local service visible
  for service in "$ORCHESTRATOR" "$ORCHESTRATOR_B"; do
    visible=$(stack_exec "$service" nproc) || return 1
    echo "    $service native CPU visibility: $visible (budget 2)"
    [[ "$visible" =~ ^[0-9]+$ ]] && [ "$visible" -gt 0 ] && [ "$visible" -le 2 ] || return 1
  done
}
local_first_running() {
  [ "$(pg "$FOGHORN_A_DB" "SELECT count(*) FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='${local_hashes[0]}' AND status='processing' AND memory_reserved_bytes>0")" = 1 ]
}
local_job_waited_for_memory() {
  [ "$(pg "$FOGHORN_A_DB" "SELECT EXISTS (
    SELECT 1 FROM foghorn.processing_jobs AS waiting
    JOIN foghorn.processing_jobs AS running ON running.tenant_id=waiting.tenant_id AND running.job_id<>waiting.job_id
    WHERE waiting.tenant_id='$STACK_TENANT_ID'
      AND waiting.artifact_hash='${local_hashes[1]}'
      AND running.artifact_hash='${local_hashes[0]}'
      AND waiting.status='queued' AND waiting.wait_reason='memory' AND waiting.retry_count=0
      AND running.status IN ('dispatched','processing') AND running.memory_reserved_bytes>0)")" = t ]
}
before_media=$(media_runtime_state all) || { fail 'read media service state before Livepeer ladders'; finish; }
check 'native encoder worker pools see at most their two-CPU budget' native_worker_budget
printf '%s\n' "$before_media" >"$STACK_STATE_DIR/media-runtime-livepeer-before.tsv"
if printf '%s\n' "$before_media" | grep -v ' running ' >/dev/null; then fail 'media services are not running before Livepeer ladders'; finish; fi
for entry in '2560x1440 qhd 1440' '3840x2160 uhd 2160' '3840x2160 inherit 1080'; do
  read -r geometry ladder ceiling <<<"$entry"
  file="$STACK_STATE_DIR/ladder-$ladder.mp4"
  render_mp4 "$file" "$geometry" 10 8 || { fail "render $geometry"; continue; }
  options='{"keepOriginal":false}'
  [ "$ladder" = inherit ] || options=$(jq -cn --arg ladder "$ladder" '{transcodeLadder:$ladder,keepOriginal:false}')
  started=$(utc_now)
  result=$(upload_vod "$file" "stack named ladder $ladder" "$options")
  id=$(echo "$result" | jq -r '.id // empty'); hash=$(echo "$result" | jq -r '.artifactHash // empty')
  [ -n "$id" ] || { fail "upload $ladder: $result"; continue; }
  if eventually 900 "$ladder upload completes" ready "$id"; then
    check "$ladder recorded ceiling is $ceiling" recorded_ceiling "$hash" "$ceiling"
    eventually 20 "$ladder actually completed through Livepeer" livepeer_produced "$hash" "$started"
  fi
done

after_media=$(media_runtime_state all) || { fail 'read media service state after Livepeer ladders'; finish; }
printf '%s\n' "$after_media" >"$STACK_STATE_DIR/media-runtime-livepeer-after.tsv"
check 'Livepeer ladders do not OOM or restart edges, orchestrators or gateways' test "$before_media" = "$after_media"
if [ "$before_media" != "$after_media" ]; then
  log "media runtime changed during Livepeer ladders: $before_media -> $after_media"
  finish
fi

# Two full 4K ladders must fit one at a time on the unchanged 3 GiB edge,
# including when no gateway exists to take any of the encoding work.
gateway_drained() {
  [ "$(date +%s)" -ge "$DISCOVERY_DRAIN_AT" ] &&
    [ "$(pg quartermaster "SELECT COALESCE(health_status,'') FROM quartermaster.service_instances WHERE instance_id='stack-livepeer-gateway-a'")" != healthy ]
}
before_edges=$(media_runtime_state local) || { fail 'read edge state before local 4K jobs'; finish; }
stack_ctl stop "$GATEWAY" || { fail 'stop gateway for 4K memory test'; finish; }
GATEWAY_STOPPED=1
DISCOVERY_DRAIN_AT=$(($(date +%s) + 65))
eventually 180 'gateway discovery drained for local 4K jobs' gateway_drained || finish
render_mp4 "$STACK_STATE_DIR/local-4k-queue.mp4" 3840x2160 30 30 || { fail 'render sustained local 4K memory workload'; finish; }
local_jobs=()
local_hashes=()
for job in first second; do
  result=$(upload_vod "$STACK_STATE_DIR/local-4k-queue.mp4" "stack local 4K $job" '{"transcodeLadder":"uhd","keepOriginal":false}')
  id=$(echo "$result" | jq -r '.id // empty')
  hash=$(echo "$result" | jq -r '.artifactHash // empty')
  [ -n "$id" ] && [ -n "$hash" ] || { fail "create local 4K $job: $result"; finish; }
  local_jobs+=("$id")
  local_hashes+=("$hash")
  if [ "$job" = first ]; then
    eventually 120 'first local 4K job reserves memory before the second upload' local_first_running || finish
  fi
done
eventually 300 'one local 4K upload queues behind the running job without spending retries' local_job_waited_for_memory || finish
for job in 0 1; do
  if eventually 900 "local 4K job $job completes" ready "${local_jobs[$job]}"; then
    check "local 4K job $job retains its 2160 rung" recorded_ceiling "${local_hashes[$job]}" 2160
    tracks=$(pg "$FOGHORN_A_DB" "SELECT tracks::text FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='${local_hashes[$job]}'")
    check "local 4K job $job records all six video rungs" json_has "$tracks" '
      [.[] | select((.track_type // .trackType // .type)=="video" and (.codec|ascii_downcase)!="jpeg") | ([.width,.height]|min)] as $heights |
      ($heights|length)==6 and all([360,480,720,1080,1440,2160][]; . as $height | any($heights[]; (.-$height|fabs)<=32))'
    processes=$(pg "$FOGHORN_A_DB" "SELECT processes_json FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='${local_hashes[$job]}' ORDER BY created_at DESC LIMIT 1")
    check "local 4K job $job uses budgeted local encoders" json_has "$processes" 'any(.[]; .process=="AV" and .codec=="H264") and all(.[]; .process!="Livepeer") and all(.[]|select(.process=="AV" or .process=="Thumbs"); .decode_threads>=1 and .encode_threads>=1)'
  fi
done
after_edges=$(media_runtime_state local) || { fail 'read edge state after local 4K jobs'; finish; }
check 'local 4K jobs do not OOM or restart edges or unaffected Livepeer services' test "$before_edges" = "$after_edges"
stack_ctl start "$GATEWAY" || { fail 'restore gateway after 4K memory test'; finish; }
GATEWAY_STOPPED=''
gateway_restored() {
  stack_running "$GATEWAY" && [ "$(date +%s)" -ge "$DISCOVERY_READY_AT" ] &&
    [ "$(pg quartermaster "SELECT COALESCE(health_status,'') FROM quartermaster.service_instances WHERE instance_id='stack-livepeer-gateway-a'")" = healthy ]
}
DISCOVERY_READY_AT=$(($(date +%s) + 6))
eventually 180 'restored gateway is healthy before live broadcast checks' gateway_restored || finish
restored_gateway=$(docker inspect --format '{{.State.OOMKilled}} {{.State.Status}}' "$(container_of "$GATEWAY")")
check 'restored gateway was not OOM-killed' test "$restored_gateway" = 'false running'

before=$(pg commodore "SELECT count(*) FROM commodore.vod_assets WHERE tenant_id='$STACK_TENANT_ID' AND title='stack refused ladder'")
refused=$(gql 'mutation{createVodUpload(input:{filename:"valid.mp4",sizeBytes:1024,title:"stack refused ladder",transcodeLadder:"unknown"}){__typename ... on ValidationError{field}}}')
check 'unknown ladder is refused on the ladder field' json_has "$refused" '.data.createVodUpload.__typename=="ValidationError" and .data.createVodUpload.field=="transcodeLadder"'
after=$(pg commodore "SELECT count(*) FROM commodore.vod_assets WHERE tenant_id='$STACK_TENANT_ID' AND title='stack refused ladder'")
check 'refused upload registers no asset' [ "$before" = "$after" ]

stream=$(create_stream "stack live ladder change" false)
SID=$(echo "$stream" | jq -r '.id'); uuid=$(echo "$stream" | jq -r '.streamId'); key=$(echo "$stream" | jq -r '.streamKey'); internal=$(internal_name_of "$uuid")
session_ceiling() {
  pg "$FOGHORN_A_DB" "SELECT processes_json FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name='$internal' AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1" |
    jq -e --argjson height "$1" '[.[] | select(.process=="Livepeer") | .target_profiles[].height] | length>0 and max==$height' >/dev/null
}
ended() { [ "$(pg "$FOGHORN_A_DB" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name='$internal' AND ended_at IS NULL")" = 0 ]; }
PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$key" 1280x720 10 240) || { fail 'publish standard stream'; finish; }
eventually 60 'first broadcast snapshots standard' session_ceiling 1080
updated=$(gql 'mutation($id:ID!){updateStream(id:$id,input:{transcodeLadder:"uhd"}){__typename}}' "$(jq -cn --arg id "$SID" '{id:$id}')")
check 'stream ladder change accepted' json_has "$updated" '.data.updateStream.__typename=="Stream"'
check 'running broadcast retains its bound ladder' session_ceiling 1080
kill "$PUB" 2>/dev/null; PUB=''
eventually 90 'old broadcast ends' ended
PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$key" 1280x720 10 60) || { fail 'republish changed stream'; finish; }
eventually 90 'next broadcast snapshots uhd' session_ceiling 2160
finish
