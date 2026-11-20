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
  local database=$FOGHORN_A_DB raw
  source_owned "$database" "$1" || database=$FOGHORN_B_DB
  raw=$(pg "$database" "SELECT tracks::text FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1'") || return 1
  echo "$raw" | jq -e  '
    [.[] | {codec:(.codec|ascii_downcase),type:(.track_type // .trackType // .type),channels:(.channels // .audio_channels // 0)}] as $tracks |
    any($tracks[]; .codec=="h264") and
    any($tracks[]; .codec=="aac" and .channels==6)' >/dev/null
}
full_six() { full_length "$1" "$2" 6 1; }

work="$STACK_STATE_DIR/upload-formats"
mkdir -p "$work"
# Container coverage uses the existing local H264/AAC processing contract.
ffmpeg -v error -y -f lavfi -i 'testsrc2=size=960x540:rate=10:duration=6' \
  -f lavfi -i 'anullsrc=channel_layout=5.1:sample_rate=48000' -t 6 \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p -g 10 -bf 0 \
  -c:a aac -b:a 384k "$work/h264-surround.mp4" || { fail 'render H264 surround'; finish; }
ffmpeg -v error -y -i "$work/h264-surround.mp4" -c copy "$work/surround.mkv" || { fail 'render surround MKV'; finish; }
ffmpeg -v error -y -i "$work/h264-surround.mp4" -c copy -f mpegts "$work/surround.ts" || { fail 'render TS surround'; finish; }
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

for fixture in h264-surround.mp4 surround.mkv surround.ts surround.m2ts surround.mts; do
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
  if eventually 900 "$fixture becomes READY" ready_or_failed "$id"; then
    check "$fixture preserves requested tracks" metadata_matches "$hash"
    every_replica "$fixture plays through both cells" "$playback" media_at
    every_replica "$fixture retains its full duration" "$playback" full_six
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
