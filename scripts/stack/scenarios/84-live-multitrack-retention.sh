#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, publishes a multitrack live stream
# An Enhanced RTMP multitrack publish (1080p/720p/360p H264 at their rung
# bitrates) keeps its tracks as the live ladder's 1080/720/360 rungs: only the
# missing 480p rung is transcoded and billed, and HLS lists every rung once.
. "$(dirname "$0")/../lib.sh"
need ffmpeg jq curl python3 || finish
SID='' PUB=''
cleanup() { [ -z "$PUB" ] || kill "$PUB" 2>/dev/null; [ -z "$SID" ] || delete_stream "$SID"; }
trap cleanup EXIT
SINCE=$(date +%s)

# publish_multitrack <rtmp base> <stream key> <seconds>: one FLV video track
# per rendition; FFmpeg 8 writes every video stream after the first as an
# Enhanced RTMP multitrack packet.
publish_multitrack() {
  ffmpeg -hide_banner -loglevel error -re -f lavfi -i 'testsrc2=size=1920x1080:rate=10' \
    -f lavfi -i 'sine=frequency=440:sample_rate=48000' -t "$3" \
    -filter_complex '[0:v]split=3[a][b][c];[b]scale=1280:720[b2];[c]scale=640:360[c2]' \
    -map '[a]' -map '[b2]' -map '[c2]' -map 1:a \
    -c:v libx264 -preset ultrafast -pix_fmt yuv420p -bf 0 -g 20 -x264-params 'nal-hrd=cbr:scenecut=0' \
    -b:v:0 7800k -minrate:v:0 7800k -maxrate:v:0 7800k -bufsize:v:0 15600k \
    -b:v:1 4500k -minrate:v:1 4500k -maxrate:v:1 4500k -bufsize:v:1 9000k \
    -b:v:2 900k -minrate:v:2 900k -maxrate:v:2 900k -bufsize:v:2 1800k \
    -c:a aac -b:a 96k -flvflags no_metadata -f flv "$1/$2" >"$STACK_STATE_DIR/publish-multitrack-$2.log" 2>&1 &
  echo $!
}
# live_tracks: the ingest edge's video tracks of the stream as "<short side>
# <derived 0|1>", read from Mist's own health report, where a process output
# names the track it was produced from. Mist names the stream live+<internal
# name> plus an ingest suffix.
live_tracks() {
  stack_exec edge curl -s -m 10 http://127.0.0.1:4242/api2 --data-urlencode \
    'command={"active_streams":{"longform":true,"fields":["health"]}}' |
    jq -r --arg prefix "live+$INTERNAL" '.active_streams // {} | to_entries[] | select(.key | startswith($prefix))
      | .value.health // {} | to_entries[] | .value
      | select(type=="object" and (.codec // "") != "" and (.codec | ascii_upcase) != "JPEG" and (.width // 0) > 0)
      | "\([.width, .height] | min) \(if (.source // "") != "" then 1 else 0 end)"' 2>/dev/null
}
# rung_of: maps the first field (a short side) to the ladder rung it fits.
rung_of() {
  awk '{ n = split("360 480 720 1080", rungs, " "); out = $1
    for (i = 1; i <= n; i++) { r = rungs[i]; t = r / 20 < 32 ? 32 : r / 20; d = $1 - r; if (d < 0) d = -d; if (d <= t) { out = r; break } }
    print out " " $2 }'
}
# track_ladder: the originals are the 360/720/1080 rungs and 480p is the only
# process output.
track_ladder() {
  local got
  got=$(live_tracks | rung_of | sort -n | tr '\n' ',')
  echo "    video tracks (rung derived): ${got:-<none>}"
  [ "$got" = '360 0,480 1,720 0,1080 0,' ]
}
hls_ladder() {
  local url
  url=$(location_at "$1" "$2")
  [ -n "$url" ] || return 1
  python3 - "$url" <<'PY'
import re,sys,urllib.request
with urllib.request.urlopen(sys.argv[1],timeout=20) as response:
    master=response.read().decode()
sizes=[min(map(int,pair)) for pair in re.findall(r'RESOLUTION=(\d+)x(\d+)',master)]
print('    HLS variant short sides:',sizes)
rungs=sorted(next((r for r in (360,480,720,1080) if abs(s-r)<=max(32,r//20)),s) for s in sizes)
sys.exit(0 if rungs==[360,480,720,1080] else 1)
PY
}
# billed_rungs: the rungs Livepeer billed for the stream ("480,"), from the
# rendition names of its segment samples.
billed_rungs() {
  ch "SELECT renditions_json FROM periscope.processing_segments_final_v
      WHERE tenant_id='$STACK_TENANT_ID' AND position(stream_name,'$INTERNAL')>0 AND process_type='Livepeer'
        AND media_seconds>0 AND billable_at_ms >= $((SINCE * 1000)) FORMAT JSONEachRow" |
    jq -r '.renditions_json | fromjson? // [] | .[]? | .name // "" | capture("^(?<h>[0-9]+)p$").h' | sort -un | tr '\n' ','
}
billed_only_480() {
  local billed
  billed=$(billed_rungs)
  echo "    billed rungs: ${billed:-<none>}"
  [ "$billed" = '480,' ]
}

S=$(create_stream "stack-multitrack-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
INTERNAL=$(internal_name_of "$UUID")
eventually 90 'cell A Livepeer gateway discoverable' gateway_discoverable stack-livepeer-gateway-a || finish
# The publisher outlives every check below; cleanup stops it.
PUB=$(publish_multitrack "$EDGE_A_RTMP" "$KEY" 1200)
admitted() { [ -n "$(admitted_generation "$FOGHORN_A_DB" "$INTERNAL" "$SINCE")" ]; }
eventually 60 'multitrack publish admitted' admitted || { cat "$STACK_STATE_DIR/publish-multitrack-$KEY.log" >&2; finish; }
eventually 180 'originals are the 360/720/1080 rungs, only 480p is transcoded' track_ladder
every_replica 'multitrack live plays' "$PB" media_at
for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
  hls_at() { hls_ladder "$base" "$PB"; }
  eventually 60 "live HLS lists each rung exactly once via $base" hls_at
done
eventually 120 'live processing bills only the 480p rung' billed_only_480
check 'multitrack publisher stayed connected through the checks' kill -0 "$PUB"
finish
