#!/usr/bin/env bash
# stack-scenario: default
# staging: safe
# A VOD processed in its origin cell and read through the other cell's edge is
# deleted; afterwards no edge holds any file of it: not the origin edge's
# canonical media, its .dtsh, or the upload/ relay cache processing read, and
# not the .blocks/.dtsh relay caches of the edge that only read it.
# Catches: the staging 2026-10-21 retention defect (the VOD delete globbed only
# vod/<hash>* on the edge that cataloged the VOD, leaving upload/<hash>.mp4
# .blocks and .dtsh; the reading edge never cataloged the VOD, got no delete,
# and kept its caches until storage pressure evicted them), and an edge offline
# past the fan-out window never being sent the deletes it missed (no relay
# delete cursor).
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 || finish
STORE=${STACK_EDGE_STORAGE_DIR:-/data/storage}
render_mp4 "$STACK_STATE_DIR/vod-delete-caches.mp4" 640x360 15 12 || { fail "render VOD source"; finish; }
V=$(upload_vod "$STACK_STATE_DIR/vod-delete-caches.mp4" "stack deleted vod leaves no edge files")
VID=$(echo "$V" | jq -r '.id // empty')
VHASH=$(echo "$V" | jq -r '.artifactHash // empty')
VPB=$(echo "$V" | jq -r '.playbackId // empty')
[ -n "$VID" ] && [ -n "$VHASH" ] || { fail "upload: $V"; finish; }

origin_owned() { [ -n "$(pg "$1" "SELECT 1 FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH' AND NOT federated_pointer")" ]; }
origin_known() { origin_owned "$FOGHORN_A_DB" || origin_owned "$FOGHORN_B_DB"; }
eventually 60 "the VOD has a row in its origin cell" origin_known || finish
if origin_owned "$FOGHORN_A_DB"; then
  ORIGIN_DB=$FOGHORN_A_DB REMOTE=${FOGHORN_B_URLS%% *} REMOTE_EDGE=$STACK_EDGE_B_SERVICE
else
  ORIGIN_DB=$FOGHORN_B_DB REMOTE=${FOGHORN_A_URLS%% *} REMOTE_EDGE=$STACK_EDGE_A_SERVICE
fi
ready_synced() {
  local s
  s=$(vod_status "$VID")
  echo "    status=$s"
  [ "$s" = FAILED ] && return 2
  [ "$s" = READY ] || return 1
  synced_with_index "$ORIGIN_DB" "$VHASH"
}
eventually 300 "VOD ready and synced with its .dtsh index in its origin cell" ready_synced || finish

# edge_files <service>: every path under the edge's storage named after the VOD.
edge_files() { stack_exec "$1" sh -c "find $STORE -maxdepth 3 \\( -name '$VHASH' -o -name '$VHASH.*' \\) 2>/dev/null | sort"; }
log "read through the other cell's edge ($REMOTE_EDGE)"
eventually 120 "the other cell plays the VOD" media_at "$REMOTE" "$VPB" || finish
remote_cached() {
  local f
  f=$(edge_files "$REMOTE_EDGE")
  echo "    $REMOTE_EDGE: $(echo "$f" | tr '\n' ' ')"
  [ -n "$f" ]
}
eventually 60 "the reading edge ($REMOTE_EDGE) holds relay caches of the VOD" remote_cached || finish
for svc in "$STACK_EDGE_A_SERVICE" "$STACK_EDGE_B_SERVICE"; do
  echo "    before delete, $svc: $(edge_files "$svc" | tr '\n' ' ')"
done

log "delete the VOD"
R=$(gql 'mutation($id:ID!){deleteVodAsset(id:$id){__typename ... on DeleteSuccess{success}}}' "$(jq -cn --arg id "$VID" '{id:$id}')")
check "deleteVodAsset succeeds ($R)" json_has "$R" '.data.deleteVodAsset.success == true'

# A read still open on an edge holds a lease that defers that edge's delete
# until its input closes.
no_edge_files() {
  local svc f left=0
  for svc in "$STACK_EDGE_A_SERVICE" "$STACK_EDGE_B_SERVICE"; do
    f=$(edge_files "$svc")
    [ -z "$f" ] && continue
    echo "    $svc keeps: $(echo "$f" | tr '\n' ' ')"
    left=1
  done
  [ "$left" = 0 ]
}
eventually 420 "no edge keeps any file of the deleted VOD" no_edge_files

# Each cell's edge got its relay delete cursor at registration and the
# fan-out passes keep it current, so an edge offline past the fan-out window
# is sent the deletes it missed when it registers again.
cursor_current() {
  local n
  n=$(pg "$1" "SELECT count(*) FROM foghorn.node_relay_delete_cursor WHERE covered_through > NOW() - INTERVAL '30 minutes'")
  echo "    current cursors: ${n:-none}"
  [ "${n:-0}" -ge 1 ]
}
check "cell A's edge has a current relay delete cursor" cursor_current "$FOGHORN_A_DB"
check "cell B's edge has a current relay delete cursor" cursor_current "$FOGHORN_B_DB"
finish
