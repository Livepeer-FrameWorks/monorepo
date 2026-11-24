#!/usr/bin/env bash
# stack-scenario: default
# tenant: qa
# staging: safe
# Requires the combined codec/rotation Mist and matching Helmsman on both edges.
. "$(dirname "$0")/../lib.sh"
need ffmpeg ffprobe jq curl python3 || finish

no_upload_degradation() {
  local quoted rows evidence foghorn_evidence hash
  hash=$(jq -cn --arg hash "$1" '$hash') || return 1
  evidence=$(log_json "$2" "select(.artifact_hash==$hash)" edge edge-b) || return 1
  [ -n "$evidence" ] || return 1
  quoted=$(jq -cn --arg stream "processing+$1" '$stream') || return 1
  foghorn_evidence=$(log_json "$2" "select(.artifact_hash==$hash or .stream==$quoted)" foghorn foghorn-2 foghorn-b foghorn-b-2) || return 1
  [ -n "$foghorn_evidence" ] || return 1
  rows=$(log_json "$2" "select(.stream==$quoted and (.msg==\"Stream transcode degraded to local processing\" or .msg==\"Stream transcode failed; local replacement awaits job admission\" or .msg==\"Stream transcode failed; no local replacement was admitted\"))" foghorn foghorn-2 foghorn-b foghorn-b-2) || return 1
  [ -z "$rows" ]
}
source_routed() {
  local quoted rows
  quoted=$(jq -cn --arg stream "processing+$1" '$stream') || return 1
  rows=$(log_json "$2" "select(.routing==\"source_rotated\" and (.stream_name // .stream // (\"processing+\" + (.artifact_hash // \"\")))==$quoted)" edge edge-b) || return 1
  [ -n "$rows" ]
}
ready_or_failed() {
  local state
  state=$(vod_status "$1")
  [ "$state" = READY ] && return 0
  [ "$state" = FAILED ] && return 2
  return 1
}
portrait_metadata() {
  local database row
  for database in "$FOGHORN_A_DB" "$FOGHORN_B_DB"; do
    row=$(pg "$database" "SELECT tracks::text FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1' AND NOT federated_pointer") || return 1
    [ -n "$row" ] || continue
    echo "$row" | jq -e '[.[] | select((.track_type // .trackType // .type)=="video" and (.codec|ascii_downcase)!="jpeg")] as $videos |
      ($videos|length)>0 and all($videos[]; .width<.height and (.rotation // 0)==0)' >/dev/null
    return
  done
  return 1
}
portrait_hls() {
  local url
  url=$(location_at "$1" "$2")
  [ -n "$url" ] || return 1
  python3 - "$url" <<'PY'
import re,sys,urllib.request
with urllib.request.urlopen(sys.argv[1],timeout=20) as response: master=response.read().decode()
sizes=[tuple(map(int,pair)) for pair in re.findall(r'RESOLUTION=(\d+)x(\d+)',master)]
assert sizes and all(w<h for w,h in sizes),sizes
PY
}
upright_corner() {
  python3 - "$1" "${2:-1}" <<'PYPIXELS'
import subprocess,sys
pixels=subprocess.check_output(['ffmpeg','-v','error','-noautorotate','-i',sys.argv[1],
  '-frames:v','1','-vf','scale=64:64','-f','rawvideo','-pix_fmt','rgb24','-'],timeout=45)
assert len(pixels)==64*64*3,len(pixels)
for index,(x,y) in enumerate(((6,6),(57,6),(57,57),(6,57))):
  red,green,blue=pixels[(y*64+x)*3:(y*64+x)*3+3]
  if index==int(sys.argv[2]):
    assert red>100 and red>blue*1.5,(index,red,green,blue)
  else:
    assert blue>80 and blue>red*1.5,(index,red,green,blue)
PYPIXELS
}
upright_playback() {
  local url
  url=$(location_at "$1" "$2")
  [ -n "$url" ] && upright_corner "$url"
}
portrait_poster() {
  local url dimensions
  url=$(gql 'query($id:ID!){node(id:$id){... on VodAsset{thumbnailAssets{posterUrl}}}}' "$(jq -cn --arg id "$1" '{id:$id}')" | jq -r '.data.node.thumbnailAssets.posterUrl // empty')
  [ -n "$url" ] || return 1
  curl -fsS -m 20 "$url" -o "$STACK_STATE_DIR/rotation-poster.jpg" || return 1
  dimensions=$(ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=p=0:s=x "$STACK_STATE_DIR/rotation-poster.jpg") || return 1
  [ "${dimensions%x*}" -lt "${dimensions#*x}" ]
}
full_eight() { full_length "$1" "$2" 8 1; }

work="$STACK_STATE_DIR/rotation"
mkdir -p "$work"
ffmpeg -v error -y -f lavfi -i 'color=blue:size=854x480:rate=10:duration=8' \
  -vf 'drawbox=x=0:y=0:w=160:h=160:color=red:t=fill' -c:v libx264 -preset ultrafast \
  -pix_fmt yuv420p -g 10 -bf 0 "$work/coded-landscape.mp4" || { fail 'render rotated H264 source'; finish; }
if ffmpeg -hide_banner -h full 2>&1 | grep -F -- '-display_rotation' >/dev/null; then
  ffmpeg -v error -y -display_rotation -90 -i "$work/coded-landscape.mp4" -c copy "$work/portrait-flag.mp4"
else
  ffmpeg -v error -y -i "$work/coded-landscape.mp4" -c copy -metadata:s:v:0 rotate=-90 "$work/portrait-flag.mp4"
fi || { fail 'set rotation matrix'; finish; }
ffprobe -v error -select_streams v:0 -show_entries stream_side_data=rotation -of json "$work/portrait-flag.mp4" |
  jq -e 'any(.streams[].side_data_list[]?; .rotation == -90)' >/dev/null || { fail 'fixture carries a clockwise quarter-turn'; finish; }
render_mp4 "$work/coded-portrait.mp4" 480x854 10 8 || { fail 'render coded portrait source'; finish; }
for fixture in portrait-flag.mp4 coded-portrait.mp4; do
  started=$(utc_now)
  result=$(upload_vod "$work/$fixture" "stack portrait $fixture" '{"keepOriginal":false,"transcodeLadder":"standard"}')
  id=$(echo "$result" | jq -r '.id // empty')
  hash=$(echo "$result" | jq -r '.artifactHash // empty')
  playback=$(echo "$result" | jq -r '.playbackId // empty')
  [ -n "$id" ] || { fail "upload $fixture: $result"; continue; }
  if eventually 900 "$fixture becomes READY" ready_or_failed "$id"; then
    check "$fixture has upright stored tracks" portrait_metadata "$hash"
    every_replica "$fixture advertises portrait HLS variants" "$playback" portrait_hls
    every_replica "$fixture retains its full duration" "$playback" full_eight
    eventually 90 "$fixture has a portrait poster" portrait_poster "$id"
    if [ "$fixture" = portrait-flag.mp4 ]; then
      eventually 15 "$fixture routes normally to local AV" source_routed "$hash" "$started"
      every_replica "$fixture has upright playback pixels" "$playback" upright_playback
      check "$fixture has upright poster pixels" upright_corner "$STACK_STATE_DIR/rotation-poster.jpg"
    fi
    check "$fixture does not count normal routing as degradation" no_upload_degradation "$hash" "$started"
  fi
  deleted=$(gql 'mutation($id:ID!){deleteVodAsset(id:$id){__typename ... on DeleteSuccess{success}}}' "$(jq -cn --arg id "$id" '{id:$id}')")
  check "$fixture cleanup accepted" json_has "$deleted" '.data.deleteVodAsset.__typename=="DeleteSuccess" and .data.deleteVodAsset.success'
done
# A mirror-only source below every standard rung still needs a source-size encode.
ffmpeg -v error -y -f lavfi -i 'color=blue:size=320x180:rate=10:duration=8' \
  -vf 'drawbox=x=0:y=0:w=80:h=80:color=red:t=fill' -c:v libx264 -preset ultrafast \
  -pix_fmt yuv420p -g 10 -bf 0 "$work/mirror-only.mp4" || { fail 'render mirror-only H264'; finish; }
python3 - "$work/mirror-only.mp4" <<'PYMIRROR'
import struct,sys
from pathlib import Path
path=Path(sys.argv[1]); data=bytearray(path.read_bytes())
def boxes(start,end):
 while start+8<=end:
  size,kind=struct.unpack_from('>I4s',data,start)
  assert size>=8 and start+size<=end,(start,size)
  yield start,size,kind
  start+=size
patched=0
for moov,ms,mk in boxes(0,len(data)):
 if mk!=b'moov': continue
 for trak,ts,tk in boxes(moov+8,moov+ms):
  if tk!=b'trak': continue
  for box,size,kind in boxes(trak+8,trak+ts):
   if kind!=b'tkhd': continue
   matrix=box+(48 if data[box+8]==0 else 60)
   struct.pack_into('>9i',data,matrix,-65536,0,0,0,65536,0,320*65536,0,1<<30)
   patched+=1
assert patched==1,patched
path.write_bytes(data)
PYMIRROR
mirror_stored() {
 local database row
 for database in "$FOGHORN_A_DB" "$FOGHORN_B_DB"; do
  row=$(pg "$database" "SELECT tracks::text FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1' AND NOT federated_pointer") || return 1
  [ -n "$row" ] || continue
  echo "$row" | jq -e '[.[] | select((.track_type // .trackType // .type)=="video" and (.codec|ascii_downcase)!="jpeg")] as $videos |
   ($videos|length)>0 and all($videos[]; .width==320 and .height==180 and (.rotation // 0)==0 and (.mirror // false)==false)' >/dev/null
  return
 done
 return 1
}
mirror_playback() {
 local url
 url=$(location_at "$1" "$2")
 [ -n "$url" ] && upright_corner "$url" 1
}
started=$(utc_now)
result=$(upload_vod "$work/mirror-only.mp4" 'stack mirror-only below ladder' '{"keepOriginal":false,"transcodeLadder":"standard"}')
id=$(echo "$result" | jq -r '.id // empty')
hash=$(echo "$result" | jq -r '.artifactHash // empty')
playback=$(echo "$result" | jq -r '.playbackId // empty')
if [ -z "$id" ]; then
 fail "upload mirror-only: $result"
elif eventually 900 'mirror-only upload becomes READY' ready_or_failed "$id"; then
 check 'mirror-only source has an upright source-size stored rendition' mirror_stored "$hash"
 every_replica 'mirror-only source retains full duration' "$playback" full_eight
 every_replica 'mirror-only source has reflected playback pixels' "$playback" mirror_playback
 check 'mirror-only source does not count normal routing as degradation' no_upload_degradation "$hash" "$started"
fi
if [ -n "$id" ]; then
 deleted=$(gql 'mutation($id:ID!){deleteVodAsset(id:$id){__typename ... on DeleteSuccess{success}}}' "$(jq -cn --arg id "$id" '{id:$id}')")
 check 'mirror-only cleanup accepted' json_has "$deleted" '.data.deleteVodAsset.__typename=="DeleteSuccess" and .data.deleteVodAsset.success'
fi


# A push remains live while the replacement ladder is selected and served. The
# publisher uses the MKV URL the ingest front door resolves for the stream key,
# unless STACK_LIVE_MKV_INGEST_URL names an endpoint.
live_sid='' live_pub='' live_ingest_url=''
live_mkv_resolved() {
  local body
  body=$(curl -s -m 10 "$1/ingest/$live_key?protocol=mkv") || return 1
  live_ingest_url=$(jq -r '.primary.mkvUrl // empty' <<<"$body" 2>/dev/null)
  case "$live_ingest_url" in http://*/"$live_key".mkv | https://*/"$live_key".mkv) return 0 ;; *) return 1 ;; esac
}
cleanup_live_rotation() {
  if [ -n "$live_pub" ]; then
    kill -TERM "$live_pub" 2>/dev/null || true
    wait "$live_pub" 2>/dev/null || true
  fi
  [ -z "$live_sid" ] || delete_stream "$live_sid"
}
trap cleanup_live_rotation EXIT
live=$(create_stream "stack-live-rotation-$(date +%s)" false INHERIT)
live_sid=$(jq -r '.id // empty' <<<"$live")
live_key=$(jq -r '.streamKey // empty' <<<"$live")
live_uuid=$(jq -r '.streamId // empty' <<<"$live")
live_playback=$(jq -r '.playbackId // empty' <<<"$live")
if [ -z "$live_sid" ] || [ -z "$live_key" ] || [ -z "$live_uuid" ] || [ -z "$live_playback" ]; then
  fail 'create rotated live MKV stream'
else
  live_internal=$(internal_name_of "$live_uuid")
  if [ -n "${STACK_LIVE_MKV_INGEST_URL:-}" ]; then
    live_ingest_url="${STACK_LIVE_MKV_INGEST_URL%/}/$live_key.mkv"
  else
    for live_front_door in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
      eventually 60 "front door at $live_front_door resolves a live MKV ingest URL" live_mkv_resolved "$live_front_door"
    done
  fi
  live_started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  python3 "$STACK_LIB_DIR/rotate_live_mkv.py" "$work/coded-landscape.mp4" \
    "${live_ingest_url:-missing-mkv-ingest-url}" > "$work/live-publisher.log" 2>&1 &
  live_pub=$!
  live_rotation_routed() {
    local quoted rows runtime
    kill -0 "$live_pub" 2>/dev/null || return 2
    runtime=$(pg "$FOGHORN_A_DB" "SELECT runtime_name FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name='$live_internal' AND ended_at IS NULL AND projection_state='active' ORDER BY started_at DESC LIMIT 1") || return 1
    [ -n "$runtime" ] || return 1
    quoted=$(jq -cn --arg stream "$runtime" '$stream') || return 1
    rows=$(log_json "$live_started" "select(.routing==\"source_rotated\" and (.stream_name // .stream // \"\")==$quoted)" edge edge-b) || return 1
    [ -n "$rows" ]
  }
  if eventually 90 'live MKV routes rotation to local processing' live_rotation_routed; then
    every_replica 'live rotated MKV advertises upright renditions' "$live_playback" portrait_hls
    every_replica 'live rotated MKV serves upright pixels' "$live_playback" upright_playback
  fi
fi

finish
