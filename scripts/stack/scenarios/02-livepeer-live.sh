#!/usr/bin/env bash
# stack-scenario: default
# Live ABR through the cell's offchain Livepeer gateway, then the CPU fallback
# when the gateway dies. A 854x480@15 source keeps transcoding cheap: the 360p
# rung is real work, the 480p rung is equal-area (kept), 720p/1080p are inhibited.
# Catches: N2 (every Livepeer job rejected on the manifest id), F3 (inhibitor
# iterator/JPEG match), N5 (fallback upscales), WS3 fallback never switching.
. "$(dirname "$0")/../lib.sh"

GATEWAY=${STACK_LIVEPEER_GATEWAY_A:-livepeer-gateway-a}
SOURCE_H=480
need ffmpeg jq curl || finish

S=$(create_stream "stack-livepeer-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 854x480 15 420)
trap 'kill "$PUB" 2>/dev/null; stack_ctl start "$GATEWAY" >/dev/null 2>&1; delete_stream "$SID"' EXIT
INTERNAL=$(internal_name_of "$UUID")

# The exact ladder: source + 480p (equal area) + 360p; nothing taller than the source.
ladder() {
  local h
  h=$(video_heights "$UUID" | tr '\n' ' ')
  echo "    video heights: ${h:-<none>}"
  [ "$h" = "360 480 480 " ]
}
degraded_at() {
  pg "$FOGHORN_A_DB" "SELECT COALESCE(transcode_degraded_at::text,'') FROM foghorn.ingest_sessions
    WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name='$INTERNAL' AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1"
}

log "Livepeer ladder"
eventually 90 "cell A Livepeer gateway discoverable" gateway_discoverable stack-livepeer-gateway-a
eventually 120 "Livepeer ladder is exactly 360/480 + 480 source" ladder
check "ingest session not degraded while the gateway is up" [ -z "$(degraded_at)" ]
tallest=$(video_heights "$UUID" | tail -1)
check "no rendition taller than the ${SOURCE_H}p source (tallest ${tallest:-none})" [ "${tallest:-0}" -le "$SOURCE_H" ]
every_replica "Livepeer renditions play" "$PB" media_at

log "gateway dies -> CPU fallback"
if stack_ctl stop "$GATEWAY"; then
  is_degraded() { [ -n "$(degraded_at)" ]; }
  eventually 90 "Foghorn records the transcode degradation" is_degraded
  # One segment window after the switch the CPU renditions carry the same ladder.
  eventually 60 "CPU fallback produces the same 360/480 ladder" ladder
  tallest=$(video_heights "$UUID" | tail -1)
  check "fallback never upscales (tallest ${tallest:-none} <= ${SOURCE_H})" [ "${tallest:-0}" -le "$SOURCE_H" ]
  every_replica "fallback renditions play" "$PB" media_at
  stack_ctl start "$GATEWAY" || true
fi
finish
