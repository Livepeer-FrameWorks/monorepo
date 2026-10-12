#!/usr/bin/env bash
# stack-scenario: default
# A cell-B edge keeps playing a cell-A VOD after the presigned GETs it cached
# for it have expired. The S3 proxy answers every presigned GET of that VOD
# signed before the arming second the way S3 answers an expired signature.
# Mist opens a source with a HEAD to the edge's relay: the relay must re-resolve
# through Foghorn and answer 200, never the store's 403.
# Catches: R5F-1 (the relay replayed the origin's 15-minute presigned URL from
# its resolve cache for ~48 minutes, so every cross-cell VOD/clip/DVR showed an
# empty master from minute 15 to minute 48 after the edge's first resolve).
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl || finish
[ -n "${STACK_THUMBNAIL_FAULT_URL:-}" ] && [ -n "${SERVICE_TOKEN:-}" ] || {
  blocked 'the S3 fault proxy and its control credential are required'
  finish
}
fault() {
  curl -fsS -m 10 -H "Authorization: Bearer $SERVICE_TOKEN" \
    -H 'Content-Type: application/json' "$@" "$STACK_THUMBNAIL_FAULT_URL"
}
fault -d '{"expire_artifact":""}' >/dev/null || { blocked 'S3 fault proxy is unreachable'; finish; }
stack_exec "$STACK_EDGE_B_SERVICE" true || finish

render_mp4 "$STACK_STATE_DIR/vod-presign-expiry.mp4" 640x360 15 12 || { fail "render VOD source"; finish; }
V=$(upload_vod "$STACK_STATE_DIR/vod-presign-expiry.mp4" "stack presign expiry")
VID=$(echo "$V" | jq -r '.id // empty')
VHASH=$(echo "$V" | jq -r '.artifactHash // empty')
VPB=$(echo "$V" | jq -r '.playbackId // empty')
[ -n "$VID" ] && [ -n "$VHASH" ] || { fail "upload: $V"; finish; }
trap 'fault -d '\''{"expire_artifact":""}'\'' >/dev/null 2>&1' EXIT

ready_synced() {
  local s sync
  s=$(vod_status "$VID")
  sync=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(sync_status,'') FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH'")
  echo "    status=$s sync=${sync:-?}"
  [ "$s" = FAILED ] && return 2
  [ "$s" = READY ] && [ "$sync" = synced ]
}
eventually 300 "VOD ready and synced in cell A" ready_synced

B=${FOGHORN_B_URLS%% *}
eventually 60 "first cross-cell play through cell B caches the edge's resolution" media_at "$B" "$VPB"
FORMAT=$(pg "$FOGHORN_B_DB" "SELECT COALESCE(format,'') FROM foghorn.artifacts WHERE artifact_hash='$VHASH'")
[ -n "$FORMAT" ] || { fail "cell B adopted no artifact row with a format for $VHASH"; finish; }
RELAY="http://127.0.0.1:18007/internal/artifact/vod/$VHASH.${FORMAT#.}"

# X-Amz-Date has one-second resolution: the URLs cached above must be signed
# in an earlier second than the arming instant.
sleep 1
check "presigned GETs of $VHASH signed so far now answer expired" fault -d "$(jq -cn --arg h "$VHASH" '{expire_artifact:$h}')"

code=$(stack_exec "$STACK_EDGE_B_SERVICE" curl -s -m 15 -o /dev/null -w '%{http_code}' -I -A MistServer/stack "$RELAY")
if [ "$code" = 200 ]; then
  pass "Mist's source HEAD on edge-b after expiry -> 200"
else
  fail "Mist's source HEAD on edge-b after expiry -> $code (want 200 from a re-resolved URL)"
fi
EXP=$(fault | jq -c '.expiry')
check "the relay first used the expired URL (proxy refused it)" json_has "$EXP" '.refused >= 1'
check "the relay then read through a URL signed after the expiry" json_has "$EXP" '.passed >= 1'
check "a viewer through cell B still gets media" media_at "$B" "$VPB"
finish
