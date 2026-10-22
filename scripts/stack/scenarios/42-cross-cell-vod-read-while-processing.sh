#!/usr/bin/env bash
# stack-scenario: default
# staging: safe
# The other cell plays a VOD while its origin cell is still processing it, and
# the same edge keeps playing it once processing has replaced the uploaded
# source with its output in another container and storage has deleted the
# upload, while the edge still holds what it cached for the first read.
# Catches: the staging edge-us defect after v0.3.13 (the US Foghorn adopted the
# EU VOD with the uploaded source's format=mp4 while EU was processing it; the
# adoption kept that format once EU's output was mkv and STREAM_SOURCE never
# asked EU again, so every later .mp4 relay read through edge-us failed while
# EU played it). Also catches the edge relay answering the processed .mkv from
# the resolution it cached for the upload's .mp4 (same hash, other container),
# and replaying that resolution's deleted upload URL as 404 until its TTL ran
# out instead of resolving again.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 || finish
render_mp4 "$STACK_STATE_DIR/vod-xcell-processing.mp4" 640x360 15 30 || { fail "render VOD source"; finish; }
V=$(upload_vod "$STACK_STATE_DIR/vod-xcell-processing.mp4" "stack cross-cell read while processing")
VID=$(echo "$V" | jq -r '.id // empty')
VHASH=$(echo "$V" | jq -r '.artifactHash // empty')
VPB=$(echo "$V" | jq -r '.playbackId // empty')
[ -n "$VID" ] && [ -n "$VHASH" ] || { fail "upload: $V"; finish; }

origin_owned() { # origin_owned <db>: the cell's own (not adopted) row exists
  [ -n "$(pg "$1" "SELECT 1 FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH' AND NOT federated_pointer")" ]
}
origin_known() { origin_owned "$FOGHORN_A_DB" || origin_owned "$FOGHORN_B_DB"; }
eventually 60 "the VOD has a row in its origin cell" origin_known
if origin_owned "$FOGHORN_A_DB"; then
  ORIGIN_DB=$FOGHORN_A_DB REMOTE_DB=$FOGHORN_B_DB REMOTE=${FOGHORN_B_URLS%% *} REMOTE_EDGE=$STACK_EDGE_B_SERVICE
else
  ORIGIN_DB=$FOGHORN_B_DB REMOTE_DB=$FOGHORN_A_DB REMOTE=${FOGHORN_A_URLS%% *} REMOTE_EDGE=$STACK_EDGE_A_SERVICE
fi
origin_format() { pg "$ORIGIN_DB" "SELECT COALESCE(format,'') FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH'"; }
pointer_format() { pg "$REMOTE_DB" "SELECT COALESCE(format,'') FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH' AND federated_pointer"; }

log "read through the other cell ($REMOTE) while the origin processes"
read_while_processing() {
  local s
  s=$(vod_status "$VID")
  echo "    status=$s origin format=$(origin_format) pointer format=$(pointer_format)"
  case "$s" in
  PROCESSING) ;;
  READY | FAILED) return 2 ;;
  *) return 1 ;;
  esac
  media_at "$REMOTE" "$VPB"
}
eventually 240 "the other cell plays the VOD while its origin is processing it" read_while_processing
LAST_READ=$(utc_now)
LAST_READ_EPOCH=$(date +%s)
SOURCE_FORMAT=$(pointer_format)
SOURCE_URL=$(pg "$ORIGIN_DB" "SELECT COALESCE(s3_url,'') FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH'")
echo "    origin source object while processing: ${SOURCE_URL:-none}"
INTERNAL=$(pg "$REMOTE_DB" "SELECT COALESCE(internal_name,'') FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH'")
echo "    adopted while processing: format=${SOURCE_FORMAT:-none}"

ready_synced() {
  local s
  s=$(vod_status "$VID")
  echo "    status=$s"
  [ "$s" = FAILED ] && return 2
  [ "$s" = READY ] || return 1
  synced_with_index "$ORIGIN_DB" "$VHASH"
}
eventually 420 "VOD ready and synced with its .dtsh index in its origin cell" ready_synced
FINAL_FORMAT=$(origin_format)
echo "    origin format after processing: $FINAL_FORMAT (source format: ${SOURCE_FORMAT:-none})"
[ -n "$SOURCE_FORMAT" ] && [ "$SOURCE_FORMAT" != "$FINAL_FORMAT" ] ||
  echo "    processing kept the source container; the format change this scenario guards is not exercised"

# Only a new input on the other cell's edge asks its Foghorn for the source
# again; the input that served the read during processing stays up while it
# has viewers.
input_idle() { log_has "$LAST_READ" "Stream vod\\+$INTERNAL became inactive" "$REMOTE_EDGE"; }
eventually 240 "the other cell's edge ($REMOTE_EDGE) closed the input of the read during processing" input_idle

# Processing enqueues the superseded upload for deletion; the cleanup queue
# drains every minute. The edge keeps its relay cache from the first read.
SOURCE_OBJECT=${SOURCE_URL#s3://}
if [ "$SOURCE_OBJECT" != "$SOURCE_URL" ] && [ "$SOURCE_URL" != "$(pg "$ORIGIN_DB" "SELECT COALESCE(s3_url,'') FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH'")" ]; then
  upload_deleted() { python3 "$STACK_LIB_DIR/s3-prefix-empty.py" "${SOURCE_OBJECT%%/*}" "${SOURCE_OBJECT#*/}"; }
  eventually 300 "storage deleted the superseded upload $SOURCE_URL" upload_deleted
else
  echo "    the origin still serves the source object; the deleted-upload read this scenario guards is not exercised"
fi
echo "    cached on $REMOTE_EDGE: $(stack_exec "$REMOTE_EDGE" sh -c "ls ${STACK_EDGE_STORAGE_DIR:-/data/storage}/vod/ | grep $VHASH" | tr '\n' ' ')"
echo "    $(($(date +%s) - LAST_READ_EPOCH))s since the read during processing (cross-cell relay resolutions are cached for about 11 minutes)"

log "the same edge ($REMOTE_EDGE) plays the processed VOD"
# The first viewer must play: a relay that answers from the resolution it
# cached for the upload fails this read even when a later retry recovers.
check "the same edge plays the processed VOD at the first attempt" media_at "$REMOTE" "$VPB"
remote_plays_processed() {
  echo "    pointer format=$(pointer_format)"
  media_at "$REMOTE" "$VPB" && [ "$(pointer_format)" = "$FINAL_FORMAT" ]
}
eventually 120 "the other cell plays the processed VOD from the origin's current $FINAL_FORMAT" remote_plays_processed
every_replica "the processed VOD plays through every replica" "$VPB" media_at
finish
