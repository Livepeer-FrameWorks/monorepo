#!/usr/bin/env bash
# stack-scenario: default
# A VOD processed in its origin cell plays through every Foghorn replica of
# both cells, first attempt, once it is ready and synced.
# Catches: N8 (only the PeerManager leader knew peer channels: non-leaders
# refused every cross-cell origin with "not authorized"), cross-cell VOD relay.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl || finish
render_mp4 "$STACK_STATE_DIR/vod-xcell.mp4" 640x360 15 12 || { fail "render VOD source"; finish; }
V=$(upload_vod "$STACK_STATE_DIR/vod-xcell.mp4" "stack cross-cell vod")
VID=$(echo "$V" | jq -r '.id // empty')
VHASH=$(echo "$V" | jq -r '.artifactHash // empty')
VPB=$(echo "$V" | jq -r '.playbackId // empty')
[ -n "$VID" ] || { fail "upload: $V"; finish; }

ready_synced() {
  local s sync
  s=$(vod_status "$VID")
  sync=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(sync_status,'') FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH'")
  [ -z "$sync" ] && sync=$(pg "$FOGHORN_B_DB" "SELECT COALESCE(sync_status,'') FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH'")
  echo "    status=$s sync=${sync:-?}"
  [ "$s" = FAILED ] && return 2
  [ "$s" = READY ] && [ "$sync" = synced ]
}
eventually 300 "VOD ready and synced in its origin cell" ready_synced

origin_a=$(pg "$FOGHORN_A_DB" "SELECT 1 FROM foghorn.artifacts WHERE artifact_hash='$VHASH' AND COALESCE(origin_cluster_id,'') IN ('', '${FOGHORN_A_CLUSTER:-}')")
echo "    origin in cell A: ${origin_a:-no}"

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
finish
