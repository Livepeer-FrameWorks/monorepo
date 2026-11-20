#!/usr/bin/env bash
# stack-scenario: default
# tenant: qa
# staging: unsafe, stops the Livepeer gateway and orchestrator
# Off-ladder and portrait uploads must retain the full requested ladder on
# Livepeer, pure local AV, and Livepeer's local replacement path.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 || finish
GATEWAY=${STACK_LIVEPEER_GATEWAY_A:-livepeer-gateway-a}
ORCHESTRATOR=${STACK_LIVEPEER_ORCHESTRATOR_A:-livepeer-orch-a}
trap 'stack_ctl start "$ORCHESTRATOR" "$GATEWAY" >/dev/null 2>&1' EXIT

gateway_state() {
  pg quartermaster "SELECT COALESCE(health_status,'') FROM quartermaster.service_instances WHERE instance_id='stack-livepeer-gateway-a'"
}
gateway_absent() {
  [ "$(gateway_state)" != healthy ] && [ "$(date +%s)" -ge "$DISCOVERY_DRAIN_AT" ]
}
gateway_present() {
  [ "$(gateway_state)" = healthy ] && [ "$(date +%s)" -ge "$DISCOVERY_READY_AT" ]
}
orchestrator_present() {
  stack_running "$ORCHESTRATOR" &&
    curl -ksS --connect-timeout 2 --max-time 5 "https://$ORCHESTRATOR:8935/" -o /dev/null
}
ready_or_failed() {
  local status
  status=$(vod_status "$1")
  echo "    status: $status"
  [ "$status" = READY ] && return 0
  [ "$status" = FAILED ] && return 2
  return 1
}
job_processes() {
  pg "$FOGHORN_A_DB" "SELECT COALESCE(processes_json,'') FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1' ORDER BY created_at DESC LIMIT 1"
}
recorded_geometry() {
  local tracks
  tracks=$(pg "$FOGHORN_A_DB" "SELECT tracks::text FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1'") || return 1
  echo "$tracks" | jq -e --argjson portrait "$2" '
    [.[] | select((.track_type // .trackType // .type) == "video" and (.codec | ascii_downcase) != "jpeg") | [.width,.height]] as $sizes |
    ($sizes | length) == 4 and
    all([360,480,720,1080][]; . as $expected |
      any($sizes[]; (min - $expected | fabs) <= ([32,($expected/20)] | max))) and
    (if $portrait then all($sizes[]; .[0] < .[1]) else true end)' >/dev/null
}
hls_geometry() {
  local url
  url=$(location_at "$2" "$3")
  [ -n "$url" ] || return 1
  python3 - "$url" "$1" <<'PY'
import re,sys,urllib.request
with urllib.request.urlopen(sys.argv[1],timeout=20) as response:
    master=response.read().decode()
sizes=[tuple(map(int,pair)) for pair in re.findall(r'RESOLUTION=(\d+)x(\d+)',master)]
if len(sizes)!=4: raise SystemExit(f"expected four HLS variants, got {sizes}")
for rung in (360,480,720,1080):
    if not any(abs(min(size)-rung)<=max(32,rung//20) for size in sizes):
        raise SystemExit(f"missing HLS rung {rung}: {sizes}")
if sys.argv[2]=="true" and not all(w<h for w,h in sizes):
    raise SystemExit(f"sideways HLS variants: {sizes}")
PY
}

full_twelve() { full_length "$1" "$2" 12; }

for geometry in 1920x1200 2560x1440 1080x1920; do
  render_mp4 "$STACK_STATE_DIR/geometry-$geometry.mp4" "$geometry" 10 12 || { fail "render $geometry"; finish; }
done
for path in livepeer local fallback; do
  case "$path" in
    livepeer)
      if ! eventually 30 "Livepeer orchestrator accepts connections" orchestrator_present; then
        docker compose -p "$COMPOSE_PROJECT_NAME" ps --all "$ORCHESTRATOR" >&2
        docker inspect --format '{{json .State}}' "$(docker compose -p "$COMPOSE_PROJECT_NAME" ps --all -q "$ORCHESTRATOR")" >&2
        finish
      fi
      DISCOVERY_READY_AT=$(($(date +%s) + 6))
      eventually 120 "Livepeer gateway available" gateway_present || finish
      ;;
    local)
      stack_ctl stop "$GATEWAY" || { fail "stop gateway"; finish; }
      # Positive discovery is cached for 60 seconds; health and cache must both drain.
      DISCOVERY_DRAIN_AT=$(($(date +%s) + 65))
      eventually 180 "gateway undiscoverable before pure-local uploads" gateway_absent || finish
      ;;
    fallback)
      stack_ctl start "$GATEWAY" || { fail "start gateway"; finish; }
      DISCOVERY_READY_AT=$(($(date +%s) + 6))
      eventually 120 "gateway registered for Livepeer-to-local uploads" gateway_present || finish
      stack_ctl stop "$ORCHESTRATOR" || { fail "stop orchestrator"; finish; }
      ;;
  esac
  for geometry in 1920x1200 2560x1440 1080x1920; do
    log "$path: $geometry upload"
    result=$(upload_vod "$STACK_STATE_DIR/geometry-$geometry.mp4" "stack $path $geometry")
    id=$(echo "$result" | jq -r '.id // empty')
    hash=$(echo "$result" | jq -r '.artifactHash // empty')
    playback=$(echo "$result" | jq -r '.playbackId // empty')
    if [ -z "$id" ]; then fail "upload $path/$geometry: $result"; continue; fi
    if eventually 600 "$path/$geometry reaches READY" ready_or_failed "$id"; then
      portrait=false
      [ "$geometry" = 1080x1920 ] && portrait=true
      check "$path/$geometry has all recorded rungs" recorded_geometry "$hash" "$portrait"
      every_replica "$path/$geometry has all HLS rungs" "$playback" hls_geometry "$portrait"
      every_replica "$path/$geometry spans the full source" "$playback" full_twelve
      if [ "$path" = livepeer ]; then
        check "Livepeer policy retained" json_has "$(job_processes "$hash")" 'any(.[]; .process == "Livepeer")'
      else
        check "$path policy persisted as local AV" json_has "$(job_processes "$hash")" 'any(.[]; .process == "AV") and all(.[]; .process != "Livepeer")'
      fi
    fi
  done
done
finish
