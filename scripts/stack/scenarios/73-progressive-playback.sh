#!/usr/bin/env bash
# stack-scenario: default
# staging: safe
# tenant: qa
# Progressive playback must deliver decodable MP4 through placement and the
# final PLAY_REWRITE, for the live stream, its DVR and a stored clip. HLS is
# checked independently so an unavailable source cannot masquerade as MP4 loss.
. "$(dirname "$0")/../lib.sh"

need ffmpeg ffprobe jq curl python3 || finish
SID='' CID='' DVR_HASH='' PUB=''
cleanup() {
  [ -z "$PUB" ] || kill "$PUB" 2>/dev/null || true
  [ -z "$CID" ] || gql 'mutation($id:ID!){deleteClip(id:$id){__typename}}' \
    "$(jq -cn --arg id "$CID" '{id:$id}')" >/dev/null
  [ -z "$DVR_HASH" ] || gql 'mutation($hash:ID!){deleteDVR(dvrHash:$hash){__typename}}' \
    "$(jq -cn --arg hash "$DVR_HASH" '{hash:$hash}')" >/dev/null
  [ -z "$SID" ] || delete_stream "$SID"
}
trap cleanup EXIT

run_source_case() {
  local ingest=$1
  BASE=$2
  log "publish to $ingest; resolve through both cells"
  S=$(create_stream "stack-progressive-$(date +%s)" true)
  SID=$(echo "$S" | jq -r '.id // empty')
  UUID=$(echo "$S" | jq -r '.streamId // empty')
  PB=$(echo "$S" | jq -r '.playbackId // empty')
  KEY=$(echo "$S" | jq -r '.streamKey // empty')
  [ -n "$SID" ] && [ -n "$UUID" ] && [ -n "$PB" ] && [ -n "$KEY" ] || {
    fail "create the progressive playback source"
    finish
  }
  PUB=$(publish_until_admitted "$ingest" "$KEY" 320x180 10 180) || {
    fail "publish the progressive playback source"
    finish
  }
  eventually 60 "live HLS delivers media" media_at "$BASE" "$PB" || finish

  # A finite range avoids leaving a progressive live download connected until
  # the publisher stops. Both HTTP errors and valid-but-wrong formats fail.
  mp4_at() {
    local base=$1 playback=$2 query=${3:-} file format code frames count rc
    file=$(mktemp "$STACK_STATE_DIR/progressive-XXXXXX.mp4") || return 1
    code=$(curl -sS -fL --max-time 25 -o "$file" -w '%{http_code}' \
      "$base/play/$playback.mp4$query" 2>/dev/null)
    rc=$?
    if [ "$rc" != 0 ]; then
      echo "    MP4 HTTP $code, curl exit $rc, $(wc -c <"$file" | tr -d ' ') bytes; evidence: $file"
      ffprobe -v error -show_entries format=format_name,duration -of json "$file" 2>/dev/null || true
      return 1
    fi
    [ "$code" = 200 ] || { rm -f "$file"; return 1; }
    # Inspect the container as well as decoding: HLS served under .mp4 is wrong.
    format=$(ffprobe -v error -show_entries format=format_name -of default=nw=1:nk=1 "$file" 2>/dev/null)
    case ",$format," in
      *,mp4,*) ;;
      *) rm -f "$file"; return 1 ;;
    esac
    frames=$(ffmpeg -hide_banner -loglevel error -xerror -i "$file" -map 0:v:0 -frames:v 3 -f framemd5 - 2>/dev/null)
    rc=$?
    rm -f "$file"
    [ "$rc" = 0 ] || return "$rc"
    count=$(printf '%s\n' "$frames" | awk '!/^#/ && NF {count++} END {print count+0}')
    [ "$count" = 3 ]
  }
  # The live HLS check guarantees media exists; wait for enough history to cut
  # the requested interval, independently of the protocol under test.
  sleep 12
  for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
    check "live MP4 decodes through $base" mp4_at "$base" "$PB" '?startunix=-8&duration=4&dl=1'
  done

  dvr_started() {
    local row
    row=$(pg commodore "SELECT playback_id, dvr_hash FROM commodore.dvr_recordings
      WHERE tenant_id='$STACK_TENANT_ID' AND stream_id='$UUID'
      ORDER BY created_at DESC LIMIT 1")
    IFS='|' read -r DVR_PB DVR_HASH <<<"$row"
    [ -n "$DVR_PB" ] && [ -n "$DVR_HASH" ]
  }
  if eventually 45 "DVR registered for this source" dvr_started; then
    eventually 45 "live DVR HLS delivers media" media_at "$BASE" "$DVR_PB"
    for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
      check "live DVR MP4 decodes through $base" mp4_at "$base" "$DVR_PB" '?startunix=-8&duration=4&dl=1'
    done
  fi

  R=$(gql 'mutation($i:CreateClipInput!){createClip(input:$i){__typename ... on Clip{id playbackId} ... on ValidationError{message}}}' \
    "$(jq -cn --arg id "$SID" --argjson start $(($(date +%s) - 10)) \
      '{i:{streamId:$id,title:"stack progressive clip",mode:"DURATION",startUnix:$start,duration:4}}')")
  CID=$(echo "$R" | jq -r '.data.createClip.id // empty')
  CPB=$(echo "$R" | jq -r '.data.createClip.playbackId // empty')
  if [ -n "$CID" ] && [ -n "$CPB" ]; then
    if eventually 90 "stored clip HLS delivers media" media_at "$BASE" "$CPB"; then
      for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
        check "stored clip MP4 decodes through $base" mp4_at "$base" "$CPB" '?dl=1'
      done
    fi
  else
    fail "create the stored clip for progressive playback"
  fi
}

run_source_case "$EDGE_A_RTMP" "${FOGHORN_A_URLS%% *}"
cleanup
SID='' CID='' DVR_HASH='' PUB=''
run_source_case "$EDGE_B_RTMP" "${FOGHORN_B_URLS%% *}"
finish
