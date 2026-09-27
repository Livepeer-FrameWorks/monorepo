#!/usr/bin/env bash
# stack-scenario: manual
# An edge that loses Foghorn while a publisher is live (slow: case B holds the
# edge isolated past the 5-min lost-node window, so manual only).
#   A. Only the control path is cut for 60 s. The ingest session stays open, the
#      edge keeps serving HLS, and after the edge re-registers the session is the
#      same generation with no stream.idle emitted.
#   B. The edge loses its whole uplink for longer than 5 min while its local
#      encoder keeps publishing. The session ends node_lost no earlier than 5 min
#      after the cut (and within a bounded wait), stream.idle is emitted; on
#      return Foghorn drains that exact generation and a new publish is admitted
#      as a new generation.
# Faults are iptables rules in the edge bundle's network namespace (lib.sh
# netfault_*): case A rejects only TCP to and from Foghorn's control port with a
# reset, so both ends see the loss at once while RTMP, HLS and DTSC keep
# flowing; case B drops everything but loopback, a silent uplink loss. In case B
# the encoder runs in the edge's own namespace (an on-site encoder) so the
# publisher survives the isolation and the returning node still holds the
# generation it must be told to stop.
# Catches: A1 (sessions retired ~2m20s after control loss), A2 (ghost sessions
# of an unreachable node never end), A3 (a node back after node_lost resumes the
# ended generation instead of being drained).
. "$(dirname "$0")/../lib.sh"

EDGE_SERVICE=${STACK_EDGE_A_SERVICE:-edge}
EDGE_NODE=${STACK_EDGE_A_NODE:-edge-node-1}
CONTROL_PORT=${STACK_FOGHORN_CONTROL_PORT:-18029}
CUT_SECONDS=${STACK_CONTROL_CUT_SECONDS:-60}
LOST_AFTER=300
FOGHORNS_A=(foghorn foghorn-2)
EDGE_PUB="$COMPOSE_PROJECT_NAME-edge-local-encoder"
need ffmpeg jq curl python3 docker || finish
webhook_endpoint >/dev/null || fail "webhook endpoint at $RECEIVER_HOOK_URL"
netfault_start "$EDGE_SERVICE" || finish

PUBS=() STREAMS=()
cleanup() {
  local p s
  netfault_stop "$EDGE_SERVICE"
  docker rm -f "$EDGE_PUB" >/dev/null 2>&1
  for p in "${PUBS[@]}"; do kill "$p" 2>/dev/null; done
  for s in "${STREAMS[@]}"; do delete_stream "$s"; done
}
trap cleanup EXIT

reconciled_after() { # reconciled_after <since>: Foghorn reconciled a registration of the edge
  logs_since "$1" "${FOGHORNS_A[@]}" | grep 'Reconciled node ingest sessions' | grep -q "$EDGE_NODE"
}
idle_for() { saw_event "$1" stream.idle ".streamId == \"$2\""; }
no_idle_for() { ! idle_for "$@"; }

# ---- A: control path only -----------------------------------------------------
log "A. control path cut for ${CUT_SECONDS}s while live"
S=$(create_stream "stack-ctl-loss-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
STREAMS+=("$SID")
IN=$(internal_name_of "$UUID")
PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$(echo "$S" | jq -r '.streamKey')" 640x360 15 $((CUT_SECONDS + 300))) ||
  { fail "publisher never admitted before the cut"; finish; }
PUBS+=("$PUB")
eventually 90 "stream live" media_at "$FOGHORN_A_URL" "$PB"
G1=$(open_generation "$FOGHORN_A_DB" "$IN" | head -1)
echo "    generation $G1"
[ -n "$G1" ] || { fail "no open generation before the cut"; finish; }
# One viewer session on the publishing edge itself, opened before the cut: a
# viewer redirect may name another cell's edge that relays the stream.
MASTER="http://$EDGE_A_HOST:8082/hls/live+$IN/index.m3u8"
edge_session() { LOC=$(hls_session_url "$MASTER") && [ -n "$LOC" ] && [ -n "$(hls_position "$LOC")" ]; }
LOC=""
eventually 30 "the publishing edge serves an HLS session" edge_session
echo "    viewer session $LOC"
first=$(hls_position "$LOC")

SINCE=$(date +%s)
CUT_TS=$(utc_now)
netfault "$EDGE_SERVICE" sh -c "iptables -A FW_FAULT_OUT -p tcp --dport $CONTROL_PORT -j REJECT --reject-with tcp-reset &&
  iptables -A FW_FAULT_IN -p tcp --sport $CONTROL_PORT -j REJECT --reject-with tcp-reset &&
  { ss -K '( dport = :$CONTROL_PORT )' >/dev/null 2>&1 || true; }" || fail "install the control-port rules"
echo "    rules: $(netfault_rules "$EDGE_SERVICE" | grep -c REJECT) REJECT"
helmsman_lost() { logs_since "$CUT_TS" "$EDGE_SERVICE" | grep -q 'Helmsman control client disconnected'; }
eventually 45 "Helmsman lost its control stream" helmsman_lost

open_ok=0 serve_ok=0 samples=0 last=$first
end=$(($(date +%s) + CUT_SECONDS))
while [ "$(date +%s)" -lt "$end" ]; do
  sleep 10
  samples=$((samples + 1))
  [ "$(open_generation "$FOGHORN_A_DB" "$IN" | head -1)" = "$G1" ] && open_ok=$((open_ok + 1))
  pos=$(hls_position "$LOC")
  if [ -n "$pos" ] && [ -n "$last" ] && [ "$pos" -gt "$last" ]; then serve_ok=$((serve_ok + 1)); fi
  printf '    +%ss hls=%s viewer-resolve=%s\n' "$(($(date +%s) - SINCE))" "${pos:-none}" "$(play_status "$FOGHORN_A_URL" "$PB")"
  last=${pos:-$last}
done
check "session $G1 stayed open through the cut ($open_ok of $samples samples)" [ "$open_ok" = "$samples" ]
check "the edge kept serving the viewer session advancing HLS through the cut ($serve_ok of $samples samples, ${first:-none} -> ${last:-none})" [ "$serve_ok" = "$samples" ]
check "publisher still connected at the end of the cut" kill -0 "$PUB"

RESTORE_TS=$(utc_now)
netfault_clear "$EDGE_SERVICE" || fail "clear the control-port rules"
restored() { reconciled_after "$RESTORE_TS"; }
eventually 90 "the edge re-registered and Foghorn reconciled its ingest sessions" restored
logs_since "$RESTORE_TS" "${FOGHORNS_A[@]}" | grep 'Reconciled node ingest sessions' | grep "$EDGE_NODE" | tail -1 | cut -c1-300
OPEN=$(open_generation "$FOGHORN_A_DB" "$IN" | tr '\n' ' ')
check "the same generation is the only open session after the return (open: $OPEN)" [ "$OPEN" = "$G1 " ]
check "no stream.idle for the stream across the cut" no_idle_for "$SINCE" "$UUID"
eventually 30 "viewers resolve to media again after the return" media_at "$FOGHORN_A_URL" "$PB"
kill "$PUB" 2>/dev/null

# ---- B: uplink loss past the lost-node window -----------------------------------
log "B. whole uplink lost for more than ${LOST_AFTER}s while the local encoder keeps publishing"
S=$(create_stream "stack-uplink-loss-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
STREAMS+=("$SID")
IN=$(internal_name_of "$UUID")
RUNNER_IMAGE=$(docker inspect -f '{{.Config.Image}}' "$(hostname)" 2>/dev/null)
[ -n "$RUNNER_IMAGE" ] || { blocked "cannot find the stack-runner image for the edge-local encoder"; finish; }
encoder_running() { [ "$(docker inspect -f '{{.State.Running}}' "$EDGE_PUB" 2>/dev/null)" = true ]; }
start_edge_encoder() { # setup: restarted while its push is refused, like publish_until_admitted
  local deadline=$(($(date +%s) + 120))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    docker rm -f "$EDGE_PUB" >/dev/null 2>&1
    docker run -d --name "$EDGE_PUB" --net "container:$(container_of "$EDGE_SERVICE")" --entrypoint ffmpeg "$RUNNER_IMAGE" \
      -hide_banner -loglevel error -re -f lavfi -i 'testsrc2=size=640x360:rate=15' -f lavfi -i 'sine=frequency=440:sample_rate=48000' \
      -t 1200 -c:v libx264 -preset veryfast -g 30 -pix_fmt yuv420p -c:a aac -b:a 96k -flvflags no_metadata \
      -f flv "rtmp://127.0.0.1:1935/live/$KEY" >/dev/null || return 1
    sleep 8
    encoder_running && return 0
    sleep 3
  done
  return 1
}
start_edge_encoder || { fail "the edge-local encoder was never admitted"; finish; }
eventually 90 "stream live from the edge-local encoder" media_at "$FOGHORN_A_URL" "$PB"
G2=$(open_generation "$FOGHORN_A_DB" "$IN" | head -1)
echo "    generation $G2"
[ -n "$G2" ] || { fail "no open generation before the uplink loss"; finish; }

SINCE=$(date +%s)
netfault "$EDGE_SERVICE" sh -c 'iptables -A FW_FAULT_IN ! -i lo -j DROP && iptables -A FW_FAULT_OUT ! -o lo -j DROP' ||
  fail "install the uplink-loss rules"
CUT=$(date +%s)
# The lost-node clock starts once Foghorn sees no control connection (keepalive,
# ~40 s) at a reaper pass (every 30 s); the end lands on a later pass.
ended_row() {
  local row
  row=$(ingest_sessions "$FOGHORN_A_DB" "$IN" | awk -F'|' -v g="$G2" '$1 == g')
  printf '    +%ss %s encoder=%s\n' "$(($(date +%s) - CUT))" "$(echo "$row" | cut -d'|' -f2,3)" "$(encoder_running && echo up || echo down)"
  [ -n "$(echo "$row" | cut -d'|' -f3)" ] || return 1
  ENDED_ROW=$row
}
waited() { # polls every 30 s; the answer comes minutes after the cut
  local deadline=$((CUT + LOST_AFTER + 150))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    ended_row && return 0
    sleep 30
  done
  ended_row
}
ENDED_ROW=""
if waited; then
  reason=$(echo "$ENDED_ROW" | cut -d'|' -f3)
  after=$(($(echo "$ENDED_ROW" | cut -d'|' -f5) - CUT))
  check "the generation ended as node_lost (got '$reason')" [ "$reason" = node_lost ]
  check "not before the ${LOST_AFTER}s lost-node window (${after}s after the cut)" [ "$after" -ge $((LOST_AFTER - 2)) ]
  pass "ended within $((LOST_AFTER + 150))s of the cut (${after}s)"
else
  fail "generation $G2 still open $((LOST_AFTER + 150))s after the uplink loss"
fi
check "the edge-local encoder kept publishing through the isolation" encoder_running
eventually 60 "stream.idle emitted for the node_lost session" idle_for "$SINCE" "$UUID"

RESTORE_TS=$(utc_now)
netfault_clear "$EDGE_SERVICE" || fail "clear the uplink-loss rules"
drained() {
  logs_since "$RESTORE_TS" "${FOGHORNS_A[@]}" | grep 'its session ended as node_lost' | grep -q "$G2"
}
eventually 120 "Foghorn told the returning node to stop generation $G2" drained
logs_since "$RESTORE_TS" "${FOGHORNS_A[@]}" | grep 'its session ended as node_lost' | grep "$G2" | tail -1 | cut -c1-300
encoder_stopped() { ! encoder_running; }
eventually 60 "the drain disconnected the edge-local encoder" encoder_stopped
check "the node_lost generation stayed ended (no reopen)" \
  [ "$(ingest_sessions "$FOGHORN_A_DB" "$IN" | awk -F'|' -v g="$G2" '$1 == g {print $3}')" = node_lost ]

log "the encoder reconnects after the return: admitted as a new generation"
new_generation() {
  G3=$(open_generation "$FOGHORN_A_DB" "$IN" | head -1)
  [ -n "$G3" ] && [ "$G3" != "$G2" ]
}
# An encoder whose connection was dropped reconnects at once, then retries
# every few seconds. The first attempt must be admitted; the retries show
# whether and when a refused reconnect would have recovered.
admitted() { # admitted <publisher pid>: connected and holding a new generation after 8 s
  local deadline=$(($(date +%s) + 8))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    kill -0 "$1" 2>/dev/null || return 1
    sleep 1
  done
  new_generation
}
G3="" attempt=0 first_ok=0 RECONNECT_TS=$(utc_now)
while [ "$attempt" -lt 8 ]; do
  attempt=$((attempt + 1))
  PUB=$(publish "$EDGE_A_RTMP" "$KEY" 640x360 15 120)
  PUBS+=("$PUB")
  if admitted "$PUB"; then
    [ "$attempt" = 1 ] && first_ok=1
    break
  fi
  echo "    attempt $attempt refused: $(tail -1 "$STACK_STATE_DIR/publish-$KEY.log" | cut -c1-160)"
  kill "$PUB" 2>/dev/null
  sleep 2
done
if [ "$first_ok" = 1 ]; then
  pass "the encoder's first reconnect after the return was admitted"
else
  fail "the encoder's first reconnect after the return was refused"
  logs_since "$RECONNECT_TS" "${FOGHORNS_A[@]}" | grep -E 'PUSH_REWRITE refused|Failed to process MistServer trigger' | head -2 | cut -c1-400
fi
check "a reconnect was admitted as a new generation (${G3:-none}, attempt $attempt)" new_generation
eventually 90 "the new generation serves media" media_at "$FOGHORN_A_URL" "$PB"
finish
