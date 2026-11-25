#!/usr/bin/env bash
# stack-scenario: default
# tenant: qa
# staging: safe
# Requires container ingestion and complete recordings on both edges.
. "$(dirname "$0")/../lib.sh"
need ffmpeg ffprobe jq curl python3 || finish

ready_or_failed() {
  local state
  state=$(vod_status "$1")
  [ "$state" = READY ] && return 0
  [ "$state" = FAILED ] && return 2
  return 1
}
source_owned() {
  local database=$1 hash=$2
  [ -n "$(pg "$database" "SELECT 1 FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash' AND NOT federated_pointer")" ]
}
metadata_matches() {
  local database=$FOGHORN_A_DB raw subtitles=${2:-false}
  source_owned "$database" "$1" || database=$FOGHORN_B_DB
  raw=$(pg "$database" "SELECT tracks::text FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1'") || return 1
  echo "$raw" | jq -e --argjson subtitles "$subtitles" '
    [.[] | {codec:(.codec|ascii_downcase),type:(.track_type // .trackType // .type),channels:(.channels // .audio_channels // 0)}] as $tracks |
    any($tracks[]; .codec=="h264") and
    any($tracks[]; .codec=="aac" and .channels==6) and
    (if $subtitles then any($tracks[]; .codec=="subtitle") else true end)' >/dev/null
}
full_six() { full_length "$1" "$2" 6 1; }

hls_audio_matches() {
  local url
  url=$(location_at "$1" "$2")
  [ -n "$url" ] || return 1
  ffprobe -v error -select_streams a -show_entries stream=codec_name,channels -of json "$url" |
    jq -e '.streams | length > 0 and all(.[]; .codec_name=="aac" and .channels==6)' >/dev/null
}
subtitles_play() {
  local url
  url=$(location_at "$2" "$3")
  [ -n "$url" ] || return 1
  python3 - "$url" "$1" <<'PYVTT'
from decimal import Decimal
import json, subprocess, sys, urllib.parse, urllib.request
url = urllib.parse.urlsplit(sys.argv[1])
assert url.path.startswith('/hls/'), url.path
stream = url.path[len('/hls/'):].split('/')[0]
assert stream, url.path
vtt = urllib.parse.urlunsplit((url.scheme, url.netloc, '/' + stream + '.vtt', url.query, ''))
with urllib.request.urlopen(vtt, timeout=30) as response:
    assert response.headers.get_content_type() == 'text/vtt', response.headers
    body = response.read().decode()
assert body.startswith('WEBVTT') and 'stack subtitle cue' in body, body
# Remuxing the MP4 into Matroska shifts every track for AAC encoder priming.
# Compare the stored cue with the uploaded container's clock, not the SRT's.
probe = json.loads(subprocess.check_output(['ffprobe', '-v', 'error', '-select_streams', 's',
    '-show_entries', 'packet=pts_time,duration_time,size', '-of', 'json', sys.argv[2]], timeout=10))
cue = next(packet for packet in probe['packets'] if int(packet['size']) > 2)
start = int(Decimal(cue['pts_time']) * 1000)
end = start + int(Decimal(cue['duration_time']) * 1000)
def stamp(ms):
    return f'{ms // 3600000:02}:{ms // 60000 % 60:02}:{ms // 1000 % 60:02}.{ms % 1000:03}'
assert stamp(start) + ' --> ' + stamp(end) in body, body
PYVTT
}


work="$STACK_STATE_DIR/upload-formats"
mkdir -p "$work"
# Cover retained tx3g/UTF8 subtitles and AC3-to-AAC surround as well as container framing.
printf '1\n00:00:00,500 --> 00:00:02,000\nstack subtitle cue\n' > "$work/cue.srt"
ffmpeg -v error -y -f lavfi -i 'testsrc2=size=960x540:rate=10:duration=6' \
  -f lavfi -i 'anullsrc=channel_layout=5.1:sample_rate=48000' -i "$work/cue.srt" -t 6 \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p -g 10 -bf 0 \
  -map 0:v -map 1:a -map 2:s -c:a aac -b:a 384k -c:s mov_text "$work/h264-surround.mp4" || { fail 'render H264 surround'; finish; }
ffmpeg -v error -y -i "$work/h264-surround.mp4" -c copy -c:s srt "$work/surround.mkv" || { fail 'render surround MKV'; finish; }
ffmpeg -v error -y -i "$work/h264-surround.mp4" -map 0:v -map 0:a -c copy -f mpegts "$work/surround.ts" || { fail 'render TS surround'; finish; }
ffmpeg -v error -y -i "$work/h264-surround.mp4" -map 0:v -map 0:a -c:v copy -c:a ac3 -b:a 384k -f mpegts "$work/surround-ac3.ts" || { fail 'render AC3 TS surround'; finish; }
python3 - "$work/surround.ts" "$work/surround.m2ts" <<'PY'
from pathlib import Path
import sys
raw=Path(sys.argv[1]).read_bytes()
assert len(raw)%188==0
Path(sys.argv[2]).write_bytes(b''.join(b'\x47\x10\x00\x10'+raw[offset:offset+188] for offset in range(0,len(raw),188)))
PY

python3 - "$work/surround.ts" "$work/surround.mts" <<'PYMTS'
from pathlib import Path
import sys
raw=Path(sys.argv[1]).read_bytes()
assert len(raw)%188==0
Path(sys.argv[2]).write_bytes(b''.join(b'\0\0\0\0'+raw[offset:offset+188] for offset in range(0,len(raw),188)))
PYMTS

for fixture in h264-surround.mp4 surround.mkv surround.ts surround.m2ts surround.mts surround-ac3.ts; do
  mime=video/mp4
  case "$fixture" in
    surround.mkv) mime=video/x-matroska ;;
    *.ts | *.m2ts | *.mts) mime=video/mp2t ;;
  esac
  options=$(jq -cn --arg mime "$mime" '{keepOriginal:false,transcodeLadder:"standard",contentType:$mime}')
  result=$(upload_vod "$work/$fixture" "stack upload format $fixture" "$options")
  id=$(echo "$result" | jq -r '.id // empty')
  hash=$(echo "$result" | jq -r '.artifactHash // empty')
  playback=$(echo "$result" | jq -r '.playbackId // empty')
  [ -n "$id" ] || { fail "upload $fixture: $result"; continue; }
  subtitles=false
  case "$fixture" in h264-surround.mp4 | surround.mkv) subtitles=true ;; esac
  if eventually 900 "$fixture becomes READY" ready_or_failed "$id"; then
    check "$fixture preserves requested tracks" metadata_matches "$hash" "$subtitles"
    every_replica "$fixture plays through both cells" "$playback" media_at
    every_replica "$fixture retains its full duration" "$playback" full_six
    if [ "$subtitles" = true ]; then
      every_replica "$fixture serves stored subtitles through WebVTT" "$playback" subtitles_play "$work/$fixture"
    fi
    if [ "$fixture" = surround-ac3.ts ]; then
      every_replica 'AC3 becomes six-channel AAC in HLS' "$playback" hls_audio_matches
    fi
  fi
  deleted=$(gql 'mutation($id:ID!){deleteVodAsset(id:$id){__typename ... on DeleteSuccess{success}}}' "$(jq -cn --arg id "$id" '{id:$id}')")
  check "$fixture cleanup accepted" json_has "$deleted" '.data.deleteVodAsset.__typename=="DeleteSuccess" and .data.deleteVodAsset.success'
done
before=$(pg commodore "SELECT count(*) FROM commodore.vod_assets WHERE tenant_id='$STACK_TENANT_ID' AND title='stack invalid upload format'")
refused=$(gql 'mutation{createVodUpload(input:{filename:"video.xyz",sizeBytes:1024,contentType:"video/mp4",title:"stack invalid upload format"}){__typename ... on ValidationError{field}}}')
check 'unknown upload extension is refused before creating a row' json_has "$refused" '.data.createVodUpload.__typename=="ValidationError" and .data.createVodUpload.field=="filename"'
after=$(pg commodore "SELECT count(*) FROM commodore.vod_assets WHERE tenant_id='$STACK_TENANT_ID' AND title='stack invalid upload format'")
check 'refused file creates no VOD catalog row' sh -c '[ -n "$1" ] && [ -n "$2" ] && [ "$1" = "$2" ]' sh "$before" "$after"
refused_import=$(gql 'mutation{importVodAsset(input:{url:"https://example.com/video.xyz",title:"stack invalid import format"}){__typename ... on ValidationError{field}}}')
check 'unknown import extension is refused before fetching or creating a row' json_has "$refused_import" '.data.importVodAsset.__typename=="ValidationError"'
check 'refused import creates no VOD catalog row' test "$(pg commodore "SELECT count(*) FROM commodore.vod_assets WHERE tenant_id='$STACK_TENANT_ID' AND title='stack invalid import format'")" = 0

finish
