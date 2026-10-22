#!/usr/bin/env bash
# stack-scenario: default
# staging: safe
# A VOD processed in its origin cell plays through every Foghorn replica of
# both cells, first attempt, once it is ready and synced. It is synced together
# with its .dtsh index, so the other cell's edge reads the stored index instead
# of rebuilding it from the media.
# Catches: N8 (only the PeerManager leader knew peer channels: non-leaders
# refused every cross-cell origin with "not authorized"), cross-cell VOD relay,
# rc8 (the index reached storage on the node's next artifact report, up to a
# minute after the media, and a cold read in that window rebuilt it).
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl || finish
LOG_SINCE=$(utc_now)
render_mp4 "$STACK_STATE_DIR/vod-xcell.mp4" 640x360 15 12 || { fail "render VOD source"; finish; }
V=$(upload_vod "$STACK_STATE_DIR/vod-xcell.mp4" "stack cross-cell vod")
VID=$(echo "$V" | jq -r '.id // empty')
VHASH=$(echo "$V" | jq -r '.artifactHash // empty')
VPB=$(echo "$V" | jq -r '.playbackId // empty')
[ -n "$VID" ] || { fail "upload: $V"; finish; }

origin_db() {
  local db
  for db in "$FOGHORN_A_DB" "$FOGHORN_B_DB"; do
    if [ -n "$(pg "$db" "SELECT 1 FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH'")" ]; then
      echo "$db"
      return
    fi
  done
}
ready_synced() {
  local s db
  s=$(vod_status "$VID")
  echo "    status=$s"
  [ "$s" = FAILED ] && return 2
  [ "$s" = READY ] || return 1
  db=$(origin_db)
  [ -n "$db" ] || return 1
  synced_with_index "$db" "$VHASH"
}
eventually 300 "VOD ready and synced with its .dtsh index in its origin cell" ready_synced

origin_a=$(pg "$FOGHORN_A_DB" "SELECT 1 FROM foghorn.artifacts WHERE artifact_hash='$VHASH' AND COALESCE(origin_cluster_id,'') IN ('', '${FOGHORN_A_CLUSTER:-}')")
echo "    origin in cell A: ${origin_a:-no}"
REMOTE_EDGE=$STACK_EDGE_B_SERVICE
[ -n "$origin_a" ] || REMOTE_EDGE=$STACK_EDGE_A_SERVICE

log "first-attempt resolve and media through every replica"
for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
  code=$(play_status "$base" "$VPB")
  if [ "$code" = 307 ] || [ "$code" = 302 ]; then
    pass "resolve via $base -> $code"
  else
    fail "resolve via $base -> $code $(curl -s -m 10 "$base/play/$VPB/hls" | head -c 200)"
  fi
  check "real TS segment via $base" media_at "$base" "$VPB"
done

signals=$(dtsh_generation_signals "$LOG_SINCE" "$VHASH" "$REMOTE_EDGE")
check "the other cell's edge ($REMOTE_EDGE) read the stored .dtsh of the freshly synced VOD (generation signals: $signals)" [ "$signals" = 0 ]
finish
