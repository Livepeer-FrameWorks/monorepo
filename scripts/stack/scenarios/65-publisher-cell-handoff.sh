#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, checks isolated cell session state and publishes to both edges
# Publish A, play through B, then publish B and play through A, and reverse.
# Each connection is attempted once. Distinct decoded colours reject a stale
# relay buffer even when its HLS endpoint still answers successfully.
. "$(dirname "$0")/../lib.sh"

need ffmpeg python3 jq curl timeout || finish
PUB="" SID=""
cleanup() {
  [ -z "$PUB" ] || kill "$PUB" 2>/dev/null || true
  [ -z "$SID" ] || delete_stream "$SID"
}
trap cleanup EXIT

start_colour() {
  local protocol=$1 edge=$2 colour=$3 target
  if [ "$protocol" = srt ]; then
    target="srt://$edge:8889?streamid=$KEY"
    ffmpeg -hide_banner -loglevel error -re -f lavfi -i "color=c=$colour:s=640x360:r=10" \
      -f lavfi -i 'sine=frequency=440:sample_rate=48000' -t 180 -c:v libx264 \
      -preset ultrafast -threads 1 -g 20 -c:a aac -f mpegts "$target" \
      >"$STACK_STATE_DIR/handoff-$protocol-$colour.log" 2>&1 &
  else
    target="rtmp://$edge:1935/live/$KEY"
    ffmpeg -hide_banner -loglevel error -re -f lavfi -i "color=c=$colour:s=640x360:r=10" \
      -f lavfi -i 'sine=frequency=440:sample_rate=48000' -t 180 -c:v libx264 \
      -preset ultrafast -threads 1 -g 20 -c:a aac -flvflags no_metadata -f flv "$target" \
      >"$STACK_STATE_DIR/handoff-$protocol-$colour.log" 2>&1 &
  fi
  PUB=$!
}

current_owner() {
  kill -0 "$PUB" 2>/dev/null || return 2
  OWNER=$(pg "$DB" "SELECT id::text || '|' || publisher_lease_epoch::text
    FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID'
      AND stream_internal_name='$INTERNAL' AND ended_at IS NULL AND projection_state='active'") || return 1
  [[ "$OWNER" =~ ^[a-f0-9-]+\|[0-9]+$ ]] || return 1
  [ "${OWNER##*|}" -gt "$LAST_EPOCH" ]
}

decoded_colour() {
  local base=$1 colour=$2 location frame
  location=$(location_at "$base" "$PB") || return 1
  [ -n "$location" ] || return 1
  frame="$STACK_STATE_DIR/handoff-colour.rgb"
  timeout 15 ffmpeg -hide_banner -loglevel error -i "$location" -frames:v 3 -an \
    -vf 'scale=1:1,format=rgb24' -f rawvideo -y "$frame" \
    >"$STACK_STATE_DIR/handoff-decode.log" 2>&1 || return 1
  python3 - "$frame" "$colour" <<'PY'
import pathlib, sys
pixels = pathlib.Path(sys.argv[1]).read_bytes()
channel = {'red': 0, 'lime': 1, 'blue': 2}[sys.argv[2]]
valid = len(pixels) == 9 and all(
    pixel[channel] > 120 and all(value < 90 for i, value in enumerate(pixel) if i != channel)
    for pixel in (pixels[i:i+3] for i in range(0, len(pixels), 3)))
sys.exit(0 if valid else 1)
PY
}

publisher_closed() {
  [ "$(pg "$DB" "SELECT count(*) FROM foghorn.ingest_sessions
    WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name='$INTERNAL' AND ended_at IS NULL")" = 0 ] &&
  [ "$(pg commodore "SELECT count(*) FROM commodore.streams WHERE tenant_id='$STACK_TENANT_ID'
    AND id='$UUID' AND active_ingest_claim_id IS NOT NULL")" = 0 ]
}

for protocol in rtmp srt; do
  stream=$(create_stream "stack-handoff-$protocol-$(date +%s)" false)
  SID=$(jq -r '.id // empty' <<<"$stream")
  UUID=$(jq -r '.streamId // empty' <<<"$stream")
  KEY=$(jq -r '.streamKey // empty' <<<"$stream")
  PB=$(jq -r '.playbackId // empty' <<<"$stream")
  [ -n "$SID" ] && [ -n "$UUID" ] && [ -n "$KEY" ] && [ -n "$PB" ] || { fail 'create handoff fixture'; finish; }
  INTERNAL=$(internal_name_of "$UUID")
  LAST_EPOCH=0
  for phase in first reverse return; do
    case "$phase" in
      first) edge=$EDGE_A_HOST; DB=$FOGHORN_A_DB; colour=red ;;
      reverse) edge=$EDGE_B_HOST; DB=$FOGHORN_B_DB; colour=blue ;;
      return) edge=$EDGE_A_HOST; DB=$FOGHORN_A_DB; colour=lime ;;
    esac
    start_colour "$protocol" "$edge" "$colour"
    eventually 45 "$protocol $phase publisher admitted on its single connection" current_owner || finish
    LAST_EPOCH=${OWNER##*|}
    for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
      eventually 60 "$protocol $phase decodes new $colour media through $base" decoded_colour "$base" "$colour" || finish
    done
    check "$protocol $phase publisher still holds its original connection" kill -0 "$PUB"
    kill "$PUB" 2>/dev/null || true
    PUB=""
    eventually 60 "$protocol $phase exact session and global lease close" publisher_closed || finish
  done
  delete_stream "$SID"
  SID=""
done
finish
