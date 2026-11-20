#!/usr/bin/env bash
# stack-scenario: default
# tenant: qa
# staging: safe
# Requires the Mist upload-codec/container release on both edges.
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
  local database=$FOGHORN_A_DB raw
  source_owned "$database" "$1" || database=$FOGHORN_B_DB
  raw=$(pg "$database" "SELECT tracks::text FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1'") || return 1
  echo "$raw" | jq -e --argjson surround "$2" --argjson subtitles "$3" '
    [.[] | {codec:(.codec|ascii_downcase),type:(.track_type // .trackType // .type),channels:(.channels // .audio_channels // 0)}] as $tracks |
    any($tracks[]; .codec=="h264") and
    (if $surround then any($tracks[]; .codec=="aac" and .channels==6) else true end) and
    (if $subtitles then any($tracks[]; .codec=="subtitle") else true end)' >/dev/null
}
full_six() { full_length "$1" "$2" 6 1; }

aac_hls() {
  local location audio
  location=$(location_at "$1" "$2") || return 1
  [ -n "$location" ] || return 1
  audio=$(ffprobe -v error -select_streams a -show_entries stream=codec_name,channels -of json "$location") || return 1
  echo "$audio" | jq -e 'any(.streams[]; .codec_name=="aac" and .channels==6)' >/dev/null
}

work="$STACK_STATE_DIR/upload-formats"
mkdir -p "$work"
printf '1\n00:00:00,500 --> 00:00:02,000\nstack subtitle cue\n' > "$work/cue.srt"
# MPEG-4 Part 2 exercises immediate Livepeer refusal and local replacement.
ffmpeg -v error -y -f lavfi -i 'testsrc2=size=960x540:rate=10:duration=6' -c:v mpeg4 -g 10 -bf 0 "$work/mpeg4.mp4" || { fail 'render MPEG4'; finish; }
ffmpeg -v error -y -i "$work/mpeg4.mp4" -i "$work/cue.srt" -c:v copy -c:s srt "$work/mpeg4-subtitles.mkv" || { fail 'render MPEG4 subtitles'; finish; }
# iPhone-like HEVC with independent 5.1 E-AC3 audio must become browser codecs.
ffmpeg -v error -y -f lavfi -i 'testsrc2=size=960x540:rate=10:duration=6' \
  -f lavfi -i 'anullsrc=channel_layout=5.1:sample_rate=48000' -t 6 \
  -c:v libx265 -preset ultrafast -x265-params 'log-level=error:pools=1:frame-threads=1' -tag:v hvc1 -g 10 -bf 0 \
  -c:a eac3 -b:a 384k "$work/hevc-surround.mp4" || { fail 'render HEVC surround'; finish; }
ffmpeg -v error -y -i "$work/hevc-surround.mp4" -c:v mpeg2video -g 10 -bf 0 -c:a copy -f mpegts "$work/surround.ts" || { fail 'render TS surround'; finish; }
python3 - "$work/surround.ts" "$work/surround.m2ts" <<'PY'
from pathlib import Path
import sys
raw=Path(sys.argv[1]).read_bytes()
assert len(raw)%188==0
Path(sys.argv[2]).write_bytes(b''.join(b'\x47\x10\x00\x10'+raw[offset:offset+188] for offset in range(0,len(raw),188)))
PY

ffmpeg -v error -y -i "$work/hevc-surround.mp4" -i "$work/cue.srt" -map 0:v -map 0:a -map 1:s \
  -c:v copy -c:a copy -c:s srt "$work/surround-subtitles.mkv" || { fail 'render surround subtitle MKV'; finish; }
ffmpeg -v error -y -i "$work/hevc-surround.mp4" -c:v mpeg2video -g 10 -bf 0 -c:a ac3 -f mpegts "$work/ac3.ts" || { fail 'render AC3 TS'; finish; }
python3 - "$work/ac3.ts" "$work/surround.mts" <<'PYMTS'
from pathlib import Path
import sys
raw=Path(sys.argv[1]).read_bytes()
assert len(raw)%188==0
Path(sys.argv[2]).write_bytes(b''.join(b'\0\0\0\0'+raw[offset:offset+188] for offset in range(0,len(raw),188)))
PYMTS

for fixture in mpeg4.mp4 mpeg4-subtitles.mkv hevc-surround.mp4 surround-subtitles.mkv surround.m2ts surround.mts ac3.ts; do
  surround=false subtitles=false mime=video/mp4
  case "$fixture" in
    surround-subtitles.mkv) surround=true subtitles=true mime=video/x-matroska ;;
    *subtitles.mkv) subtitles=true mime=video/x-matroska ;;
    hevc-surround.mp4) surround=true ;;
    *.m2ts | *.mts | ac3.ts) surround=true mime=video/mp2t ;;
  esac
  options=$(jq -cn --arg mime "$mime" '{keepOriginal:false,transcodeLadder:"standard",contentType:$mime}')
  result=$(upload_vod "$work/$fixture" "stack upload format $fixture" "$options")
  id=$(echo "$result" | jq -r '.id // empty')
  hash=$(echo "$result" | jq -r '.artifactHash // empty')
  playback=$(echo "$result" | jq -r '.playbackId // empty')
  [ -n "$id" ] || { fail "upload $fixture: $result"; continue; }
  if eventually 900 "$fixture becomes READY" ready_or_failed "$id"; then
    check "$fixture preserves requested tracks" metadata_matches "$hash" "$surround" "$subtitles"
    every_replica "$fixture plays through both cells" "$playback" media_at
    every_replica "$fixture retains its full duration" "$playback" full_six
    if [ "$fixture" = ac3.ts ]; then
      every_replica 'AC3 upload plays as AAC surround through HLS' "$playback" aac_hls
    fi
  fi
  deleted=$(gql 'mutation($id:ID!){deleteVodAsset(id:$id){__typename ... on DeleteSuccess{success}}}' "$(jq -cn --arg id "$id" '{id:$id}')")
  check "$fixture cleanup accepted" json_has "$deleted" '.data.deleteVodAsset.__typename=="DeleteSuccess" and .data.deleteVodAsset.success'
done
# This query URL redirects to a small public WebM and supports byte ranges.
# Source/attribution: https://commons.wikimedia.org/wiki/File:Flip_Book_-_Messi_Example.webm
# The explicit filename selects the relay's format; the URL path is index.php.
extensionless_url=${STACK_EXTENSIONLESS_VOD_URL:-'https://commons.wikimedia.org/w/index.php?title=Special:Redirect/file/Flip_Book_-_Messi_Example.webm'}
extensionless_filename=${STACK_EXTENSIONLESS_VOD_FILENAME:-extensionless-import.webm}
imported=$(gql 'mutation($input:ImportVodAssetInput!){importVodAsset(input:$input){__typename ... on VodAsset{id artifactHash playbackId} ... on ValidationError{message}}}' "$(jq -cn --arg url "$extensionless_url" --arg filename "$extensionless_filename" '{input:{url:$url,filename:$filename,title:"stack extensionless import",transcodeLadder:"standard"}}')")
import_id=$(echo "$imported" | jq -r '.data.importVodAsset.id // empty')
check 'extensionless import accepts an explicit video filename' test -n "$import_id"
if [ -n "$import_id" ]; then
  import_playback=$(echo "$imported" | jq -r '.data.importVodAsset.playbackId')
  if eventually 900 'extensionless URL import becomes READY' ready_or_failed "$import_id"; then
    every_replica 'extensionless URL import plays through both cells' "$import_playback" media_at
  fi
  deleted=$(gql 'mutation($id:ID!){deleteVodAsset(id:$id){__typename ... on DeleteSuccess{success}}}' "$(jq -cn --arg id "$import_id" '{id:$id}')")
  check 'extensionless import cleanup accepted' json_has "$deleted" '.data.deleteVodAsset.success==true'
fi
before=$(pg commodore "SELECT count(*) FROM commodore.vod_assets WHERE tenant_id='$STACK_TENANT_ID' AND title='stack invalid upload format'")
refused=$(gql 'mutation{createVodUpload(input:{filename:"video.xyz",sizeBytes:1024,contentType:"video/mp4",title:"stack invalid upload format"}){__typename ... on ValidationError{field}}}')
check 'unknown upload extension is refused before creating a row' json_has "$refused" '.data.createVodUpload.__typename=="ValidationError" and .data.createVodUpload.field=="filename"'
after=$(pg commodore "SELECT count(*) FROM commodore.vod_assets WHERE tenant_id='$STACK_TENANT_ID' AND title='stack invalid upload format'")
check 'refused file creates no VOD catalog row' sh -c '[ -n "$1" ] && [ -n "$2" ] && [ "$1" = "$2" ]' sh "$before" "$after"
refused_import=$(gql 'mutation{importVodAsset(input:{url:"https://example.com/video.xyz",title:"stack invalid import format"}){__typename ... on ValidationError{field}}}')
check 'unknown import extension is refused before fetching or creating a row' json_has "$refused_import" '.data.importVodAsset.__typename=="ValidationError"'
check 'refused import creates no VOD catalog row' test "$(pg commodore "SELECT count(*) FROM commodore.vod_assets WHERE tenant_id='$STACK_TENANT_ID' AND title='stack invalid import format'")" = 0

finish
