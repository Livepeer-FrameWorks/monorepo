#!/usr/bin/env bash
# stack-scenario: default
# A Helmsman restart does not turn its viewers into a burst of Foghorn calls.
# Mist keeps the viewer sessions; the restarted Helmsman rebuilds its session
# table from Mist's session list and fetches one playback grant per stream,
# after which the viewers' requests are answered on the edge again.
# Measures: grant fetches per stream after the restart; PLAY_REWRITE
# forwarded to Foghorn while several viewers of two streams keep playing.
# Catches: per-viewer PLAY_REWRITE herds after a sidecar restart.
. "$(dirname "$0")/../lib.sh"

VIEWERS=${STACK_RESTART_VIEWERS:-3}
need ffmpeg jq curl python3 docker || finish

PUBS=() SIDS=() INS=() PBS=() POLLERS=()
cleanup() {
  local p s
  for p in "${POLLERS[@]}" "${PUBS[@]}"; do kill "$p" 2>/dev/null; done
  for s in "${SIDS[@]}"; do delete_stream "$s"; done
}
trap cleanup EXIT
for n in 1 2; do
  S=$(create_stream "stack-restart-grants-$n-$(date +%s)" false)
  SID=$(echo "$S" | jq -r '.id // empty')
  [ -n "$SID" ] || { fail "createStream: $S"; finish; }
  SIDS+=("$SID")
  INS+=("$(internal_name_of "$(echo "$S" | jq -r '.streamId')")")
  PBS+=("$(echo "$S" | jq -r '.playbackId')")
  PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$(echo "$S" | jq -r '.streamKey')" 640x360 15 600) ||
    { fail "publisher $n never admitted"; finish; }
  PUBS+=("$PUB")
done
for pb in "${PBS[@]}"; do eventually 90 "stream $pb live" media_at "$FOGHORN_A_URL" "$pb" || finish; done

# Viewers play from the publishing edge itself, whose Helmsman is restarted.
SESSIONS=()
for i in 0 1; do
  for _ in $(seq "$VIEWERS"); do
    loc=$(hls_session_url "http://$EDGE_A_HOST:8082/hls/live+${INS[$i]}/index.m3u8")
    [ -n "$loc" ] || { fail "open a viewer session on stream $((i + 1))"; finish; }
    SESSIONS+=("$loc")
  done
done
# The viewers keep polling throughout, as players do, so their Mist sessions
# stay live across the Helmsman restart.
for i in "${!SESSIONS[@]}"; do
  (hls_poll "${SESSIONS[$i]}" >"$STACK_STATE_DIR/restart-viewer-$i.log") &
  POLLERS+=($!)
done
all_play() { # all_play <since unix time>
  local i
  for i in "${!SESSIONS[@]}"; do [ "$(hls_poll_state "$STACK_STATE_DIR/restart-viewer-$i.log" "$1")" = ok ] || return 1; done
}
T0=$(date +%s)
sleep 8
if all_play "$T0"; then pass "${#SESSIONS[@]} viewer sessions play from the edge"; else
  fail "${#SESSIONS[@]} viewer sessions play from the edge"
  tail -n 2 "$STACK_STATE_DIR"/restart-viewer-*.log | sed 's/^/    poll /'
  finish
fi

log "restart Helmsman on $STACK_EDGE_A_SERVICE"
RESTART_TS=$(utc_now)
stack_exec "$STACK_EDGE_A_SERVICE" /command/s6-svc -r /run/service/helmsman || { fail "restart Helmsman"; finish; }
rebuilt() { log_has "$RESTART_TS" 'Rebuilt playback sessions from Mist' "$STACK_EDGE_A_SERVICE"; }
eventually 60 "the restarted Helmsman rebuilt its sessions from Mist" rebuilt || finish
fetched() {
  local in
  for in in "${INS[@]}"; do
    [ -n "$(log_json "$RESTART_TS" "select(.msg == \"Playback grant applied\" and .internal_name == \"live+$in\") | .time" "$STACK_EDGE_A_SERVICE")" ] || return 1
  done
}
eventually 30 "a grant arrived for each stream" fetched

SERVE_TS=$(utc_now) SERVE_S=$(date +%s)
sleep 12
check "every viewer session keeps playing after the rebuild" all_play "$SERVE_S"
# Each grant the restarted Helmsman fetched is applied once; with no new
# admissions nothing else sends one.
for in in "${INS[@]}"; do
  applied=$(log_json "$RESTART_TS" "select(.msg == \"Playback grant applied\" and .internal_name == \"live+$in\") | .time" "$STACK_EDGE_A_SERVICE" | grep -c .)
  one_grant() { [ "$applied" = 1 ]; }
  check "one grant fetch for stream $in after the restart (got $applied)" one_grant
done
forwarded=$(log_json "$SERVE_TS" 'select(.msg == "PLAY_REWRITE resolved by Foghorn") | .time' "$STACK_EDGE_A_SERVICE" | grep -c .)
echo "    PLAY_REWRITE forwarded to Foghorn after the rebuild: $forwarded for ${#SESSIONS[@]} viewers polling for 12 s"
no_burst() { [ "$forwarded" -eq 0 ]; }
check "no per-viewer PLAY_REWRITE burst after the restart (got $forwarded)" no_burst
finish
