#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, restarts a Foghorn replica of the other cell
# Both Foghorn replicas of cell B restart together, as in a rolling deploy of
# the cell, while cell A has no live stream toward cell B. While they are down
# Quartermaster names cell B with no healthy Foghorn address; cell A keeps B's
# last known addresses and redials them, and Quartermaster records the returned
# replicas healthy within seconds of them serving. A stream published into
# cell A right after the restart is then served through foghorn-b on its first
# cross-cell resolve and keeps being served.
# Catches: cell A removing a peer cell that Quartermaster names without an
# address (logged as "Removed peer whose authority lease expired or was
# revoked"; until a new stream's admission re-adds it, cell A neither
# advertises to nor redials cell B), and Quartermaster re-checking a
# re-registered Foghorn only on its next watch refresh or poll round, up to a
# minute after it serves again. Either left cell B's viewers with 404 "no
# source entry in this cell's registry" or 503 "placement destination cell has
# no federation address" in a stack run.
. "$(dirname "$0")/../lib.sh"

FIRST_RESOLVE_SECONDS=${STACK_FIRST_RESOLVE_SECONDS:-10}
HEALTHY_SECONDS=${STACK_FOGHORN_HEALTHY_SECONDS:-10}
STEADY_SECONDS=${STACK_PEER_RESTART_STEADY_SECONDS:-30}
REPLICAS_B=(foghorn-b foghorn-b-2)
need ffmpeg jq curl python3 || finish
read -r -a A_BASES <<<"$FOGHORN_A_URLS"
read -r -a B_BASES <<<"$FOGHORN_B_URLS"
B=${B_BASES[0]}

SID="" PUB=""
trap 'kill "$PUB" 2>/dev/null; [ -n "$SID" ] && delete_stream "$SID"' EXIT

since=$(utc_now)
log "restart ${REPLICAS_B[*]}"
stack_ctl restart "${REPLICAS_B[@]}" || { fail "restart ${REPLICAS_B[*]}"; finish; }
for base in "${B_BASES[@]}"; do
  answers() { [ "$(play_status "$base" stack-no-such-playback)" != 000 ]; }
  eventually 120 "$base answers /play again" answers || finish
done

# The cell's gRPC Foghorn rows, as ListPeerClusters reads them for peers.
healthy_b() {
  [ "$(pg quartermaster "SELECT count(DISTINCT si.advertise_host)
    FROM quartermaster.service_instances si
    JOIN quartermaster.services svc ON svc.service_id = si.service_id
    WHERE svc.type = 'foghorn' AND si.protocol = 'grpc' AND si.status = 'running'
      AND si.health_status = 'healthy' AND si.advertise_host IN ('foghorn-b', 'foghorn-b-2')")" = 2 ]
}
deadline=$(($(date +%s) + HEALTHY_SECONDS))
until healthy_b || [ "$(date +%s)" -ge "$deadline" ]; do sleep 0.5; done
check "Quartermaster lists both restarted cell B Foghorns healthy within ${HEALTHY_SECONDS}s of them serving" healthy_b

S=$(create_stream "stack-peer-restart-a-to-b-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
PUB=$(publish "$EDGE_A_RTMP" "$(echo "$S" | jq -r '.streamKey')" 320x240 10 180)
eventually 60 "cell A stream has media at its source cell" media_at "${A_BASES[0]}" "$PB" || finish
media=$(date +%s)

# refusal <label>: prints one refused resolve with its status and body.
refusal() {
  printf '  %s %s %s -> %s %s\n' "$(ts)" "$1" "$B" "$(play_status "$B" "$PB")" \
    "$(curl -s -m 5 "$B/play/$PB/hls" | head -c 160)"
}
loc="" refused=0
while [ "$(date +%s)" -lt $((media + FIRST_RESOLVE_SECONDS)) ]; do
  loc=$(location_at "$B" "$PB")
  [ -n "$loc" ] && break
  refused=$((refused + 1))
  [ "$refused" -le 4 ] && refusal "first resolve:"
  sleep 0.5
done
if [ -z "$loc" ]; then
  fail "first cross-cell resolve via $B gave no redirect within ${FIRST_RESOLVE_SECONDS}s of source media ($refused refused)"
else
  pass "first cross-cell resolve via $B redirected after $refused refused attempts"
  got=$(segment_fetch "$loc")
  case "$got" in
    200\ *) check "first cross-cell segment via $B carries TS bytes ($got)" [ "${got#200 }" -gt 0 ] ;;
    *) fail "first cross-cell segment via $B returned $got" ;;
  esac
fi

bad=0 total=0
steady_end=$(($(date +%s) + STEADY_SECONDS))
while [ "$(date +%s)" -lt "$steady_end" ]; do
  code=$(play_status "$B" "$PB")
  total=$((total + 1))
  case "$code" in
    307 | 302) ;;
    *)
      bad=$((bad + 1))
      [ "$bad" -le 4 ] && refusal "steady:"
      ;;
  esac
  sleep 0.5
done
check "$bad of $total resolves via $B refused in the following ${STEADY_SECONDS}s" [ "$bad" = 0 ]

removed=$(log_json "$since" \
  'select(.msg == "Removed peer whose authority lease expired or was revoked") | .peer_cluster' \
  foghorn foghorn-2 | sort -u | tr '\n' ' ')
check "cell A kept its peers while cell B's Foghorns restarted (removed: ${removed:-none})" [ -z "$removed" ]
finish
