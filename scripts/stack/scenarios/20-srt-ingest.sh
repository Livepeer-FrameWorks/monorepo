#!/usr/bin/env bash
# stack-scenario: default
# SRT ingest end to end. Mist advertises its TSSRT listener without the port
# when it is the scheme default, so the edge must report the configured port:
# the front door offers an SRT URL with a port, a direct SRT publish to the edge
# is admitted, and the stream plays through every Foghorn replica.
# Catches: D1 (SRT refused on every edge: NO_INGEST_NODES at the front door and
# "temporarily unavailable; retry" on a direct publish).
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl || finish

S=$(create_stream "stack-srt-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }

srt_offered() {
  local body
  body=$(curl -s -m 10 "$1/ingest/$KEY?protocol=srt")
  SRT_URL=$(echo "$body" | jq -r '.primary.srtUrl // empty' 2>/dev/null)
  case "$SRT_URL" in srt://*:[0-9]*\?streamid=*) return 0 ;; *) return 1 ;; esac
}
for base in $FOGHORN_A_URLS; do
  eventually 60 "front door at $base offers an SRT URL with a port" srt_offered "$base"
done
log "front door SRT URL: ${SRT_URL:-none}"

log "publish SRT directly to the edge"
ffmpeg -hide_banner -loglevel error -re \
  -f lavfi -i 'testsrc2=size=640x360:rate=15' -f lavfi -i 'sine=frequency=440:sample_rate=48000' \
  -t 120 -c:v libx264 -preset veryfast -g 30 -pix_fmt yuv420p -c:a aac -b:a 96k \
  -f mpegts "srt://$EDGE_A_HOST:8889?streamid=$KEY" >"$STACK_STATE_DIR/publish-srt-$KEY.log" 2>&1 &
PUB=$!
trap 'kill "$PUB" 2>/dev/null; delete_stream "$SID"' EXIT

for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
  eventually 90 "SRT-published stream plays via $base" media_at "$base" "$PB"
done
check "SRT publisher stayed connected" kill -0 "$PUB"
if ! kill -0 "$PUB" 2>/dev/null; then
  sed 's/^/  ffmpeg: /' "$STACK_STATE_DIR/publish-srt-$KEY.log" | tail -5
fi
finish
