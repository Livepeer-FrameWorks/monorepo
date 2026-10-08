#!/usr/bin/env bash
# stack-scenario: default
# Edge A's Helmsman reaches its cell the way a production edge does: over TLS,
# by the cell's dotted control name, which resolves to every Foghorn instance of
# the cell (FOGHORN_CONTROL_ADDR=foghorn.<cluster>.<root>:18029 with
# GRPC_TLS_CA_PATH). Every other scenario runs the edges plaintext on
# single-label service names, which takes neither the TLS dial nor the
# per-address resolution of a dotted name.
#   1. Edge A registers over TLS under the name, and a publisher is admitted
#      through that control stream.
#   2. The instance holding the edge is killed together with its TLS front: the
#      edge registers with the other instance under the same name, and the
#      stream keeps serving in the same ingest generation.
#   3. The connection to the surviving instance holds, and still holds after
#      the killed instance returns.
# Edge A goes back to its plaintext addresses at the end, and must register
# there again.
# Foghorn's external listener serves TLS only from Navigator-issued cluster
# bundles and the stack runs no Navigator, so each cell-A instance is fronted by
# a TLS terminator (foghorn-tls-a-*, scripts/stack/foghorn-control-tls.conf.template)
# serving a stack-CA certificate for the cell wildcard.
# Catches: a Helmsman whose TLS dial of a resolved instance sets an authority
# beside the TLS server name, which gRPC refuses before the dial leaves the
# edge, so no TLS edge ever registers.
. "$(dirname "$0")/../lib.sh"

EDGE_SERVICE=${STACK_EDGE_A_SERVICE:-edge}
EDGE_NODE=${STACK_EDGE_A_NODE:-edge-node-1}
CONTROL_ADDR=${FOGHORN_A_CONTROL_TLS_ADDR:-foghorn.demo-media.stack.frameworks.network:18029}
CONTROL_NAME=${CONTROL_ADDR%:*}
CA_FILE=${STACK_FOGHORN_CA_FILE:-/repo/.stack/slot-${STACK_SLOT:-1}/certs/foghorn-ca.crt}
HOLD_SECONDS=${STACK_CONTROL_TLS_HOLD_SECONDS:-45}
# Helmsman loads .env from its working directory over the process environment
# at every start, so the override applies from the next Helmsman start until
# the file is removed. The bundle runs services with the container's own
# environment (S6_KEEP_ENV), which cannot change without recreating it.
EDGE_ENV=/opt/frameworks/helmsman/.env
EDGE_CA=/run/fw-stack-foghorn-ca.crt
need ffmpeg jq curl docker || finish
if [ "${STACK_TARGET:-stack}" = staging ]; then
  blocked "staging edges already run this path; the scenario reconfigures a stack edge"
  finish
fi
[ -s "$CA_FILE" ] || { blocked "no stack Foghorn CA at $CA_FILE (scripts/stack/up.sh creates it)"; finish; }

declare -A FRONT BASE
for pair in ${FOGHORN_A_CONTROL_TLS_FRONTS:-foghorn=foghorn-tls-a-1 foghorn-2=foghorn-tls-a-2}; do
  FRONT[${pair%%=*}]=${pair#*=}
done
read -r -a bases <<<"$FOGHORN_A_URLS"
BASE[foghorn]=${bases[0]}
BASE[foghorn-2]=${bases[1]}

helmsman_restart() { stack_exec "$EDGE_SERVICE" /command/s6-svc -r /run/service/helmsman; }
switch_to_tls() {
  stack_exec "$EDGE_SERVICE" sh -c "cat >$EDGE_CA && chmod 0644 $EDGE_CA" <"$CA_FILE" || return 1
  printf 'FOGHORN_CONTROL_ADDR=%s\nGRPC_TLS_CA_PATH=%s\n' "$CONTROL_ADDR" "$EDGE_CA" |
    stack_exec "$EDGE_SERVICE" sh -c "cat >$EDGE_ENV && chmod 0644 $EDGE_ENV"
}
restore_plaintext() { stack_exec "$EDGE_SERVICE" rm -f "$EDGE_ENV" "$EDGE_CA"; }

# registrations <since> <service...>: the receive time of every registration
# of edge A the instances logged since then, oldest first.
registrations() {
  local since=$1
  shift
  log_json_at "$since" "select(.msg == \"Helmsman registered\" and .node_id == \"$EDGE_NODE\") | .at" "$@" | sort
}
registered_with() { [ -n "$(registrations "$2" "$1")" ]; } # registered_with <service> <since>
# holder_since <since>: sets HOLDER to the instance edge A registered with last.
holder_since() {
  local svc at latest=""
  HOLDER=""
  for svc in foghorn foghorn-2; do
    at=$(registrations "$1" "$svc" | tail -1)
    if [ -n "$at" ] && [[ "$at" > "$latest" ]]; then latest=$at HOLDER=$svc; fi
  done
  [ -n "$HOLDER" ]
}
# dials <since>: Helmsman's dial lines: "<message> <configured entry> <address dialed>".
dials() {
  log_json "$1" 'select((.msg // "") | startswith("Connecting to Foghorn")) | "\(.msg) \(.foghorn_addr) \(.foghorn_dial)"' "$EDGE_SERVICE"
}
dial_errors() {
  log_json "$1" 'select(.foghorn_addr != null and .level != "info") | "\(.level) \(.msg) dial=\(.foghorn_dial) \(.error // "")"' "$EDGE_SERVICE" | tail -8
}
# tls_dials_only <since>: edge A dialed since then, and only over TLS by the name.
tls_dials_only() {
  local d
  d=$(dials "$1")
  printf '%s\n' "$d" | sed 's/^/    /'
  [ -n "$d" ] && ! printf '%s\n' "$d" | grep -v "^Connecting to Foghorn with TLS $CONTROL_ADDR " >/dev/null
}
# held_since <since> <service>: edge A neither dialed again nor lost its
# connection to the instance since then.
held_since() {
  local d gone
  d=$(dials "$1")
  gone=$(log_json "$1" "select(.msg == \"Helmsman disconnected\" and .node_id == \"$EDGE_NODE\") | .msg" "$2")
  [ -n "$d" ] && printf '    dials: %s\n' "$d"
  [ -n "$gone" ] && printf '    %s logged edge A disconnected\n' "$2"
  [ -z "$d" ] && [ -z "$gone" ]
}
name_resolves_to() { [ "$(stack_replica_count "$CONTROL_NAME")" = "$1" ]; }

PUB="" SID="" KILLED="" SWITCHED=0
cleanup() {
  [ -n "$PUB" ] && kill "$PUB" 2>/dev/null
  [ -n "$KILLED" ] && stack_ctl start "$KILLED" "${FRONT[$KILLED]}"
  [ -n "$SID" ] && delete_stream "$SID"
  if [ "$SWITCHED" = 1 ]; then
    restore_plaintext && helmsman_restart
  fi
}
trap cleanup EXIT

log "1. edge A registers over TLS by $CONTROL_ADDR"
eventually 30 "$CONTROL_NAME resolves to both cell-A instances' TLS fronts" name_resolves_to 2 || finish
SINCE=$(utc_now)
switch_to_tls || { fail "switch edge A's Helmsman to $CONTROL_ADDR with the stack CA"; finish; }
SWITCHED=1
helmsman_restart || { fail "restart edge A's Helmsman"; finish; }
if ! eventually 120 "edge A registers with a cell-A Foghorn over TLS by name" holder_since "$SINCE"; then
  dial_errors "$SINCE"
  finish
fi
echo "    holder: $HOLDER"
check "every dial since the switch went over TLS to $CONTROL_ADDR" tls_dials_only "$SINCE"
OTHER=foghorn
[ "$HOLDER" = foghorn ] && OTHER=foghorn-2

S=$(create_stream "stack-control-tls-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
IN=$(internal_name_of "$(echo "$S" | jq -r '.streamId')")
PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$(echo "$S" | jq -r '.streamKey')" 640x360 15 900) ||
  { PUB=""; fail "publisher admitted through the TLS control stream"; finish; }
pass "publisher admitted through the TLS control stream"
G1=$(open_generation "$FOGHORN_A_DB" "$IN" | head -1)
echo "    generation $G1"
eventually 90 "the stream plays via $HOLDER" media_at "${BASE[$HOLDER]}" "$PB"

log "2. kill $HOLDER and its TLS front; edge A fails over to $OTHER under the name"
KILL_TS=$(utc_now)
stack_ctl kill "$HOLDER" "${FRONT[$HOLDER]}" || { fail "kill $HOLDER and ${FRONT[$HOLDER]}"; finish; }
KILLED=$HOLDER
eventually 30 "$CONTROL_NAME resolves to the surviving front only" name_resolves_to 1
if ! eventually 90 "edge A registers with $OTHER over TLS by name" registered_with "$OTHER" "$KILL_TS"; then
  dial_errors "$KILL_TS"
  finish
fi
REG_TS=$(utc_now)
check "every dial since the kill went over TLS to $CONTROL_ADDR" tls_dials_only "$KILL_TS"
eventually 60 "the stream serves via $OTHER after the failover" media_at "${BASE[$OTHER]}" "$PB"
G2=$(open_generation "$FOGHORN_A_DB" "$IN" | head -1)
check "the stream keeps ingest generation $G1 across the failover (now $G2)" test -n "$G1" -a "$G2" = "$G1"

log "3. the connection to $OTHER holds for ${HOLD_SECONDS}s, then after $HOLDER returns"
sleep "$HOLD_SECONDS"
check "edge A stayed connected to $OTHER for ${HOLD_SECONDS}s" held_since "$REG_TS" "$OTHER"
stack_ctl start "$HOLDER" "${FRONT[$HOLDER]}" || { fail "start $HOLDER and ${FRONT[$HOLDER]}"; finish; }
KILLED=""
answers() { [ "$(play_status "${BASE[$HOLDER]}" "$PB")" != 000 ]; }
eventually 120 "$HOLDER answers /play again" answers
eventually 30 "$CONTROL_NAME resolves to both fronts again" name_resolves_to 2
sleep "$HOLD_SECONDS"
check "edge A stayed connected to $OTHER after $HOLDER returned" held_since "$REG_TS" "$OTHER"
check "edge A registered once since the kill" [ "$(registrations "$KILL_TS" foghorn foghorn-2 | wc -l | tr -d ' ')" = 1 ]
eventually 60 "the stream serves via the returned $HOLDER" media_at "${BASE[$HOLDER]}" "$PB"
G3=$(open_generation "$FOGHORN_A_DB" "$IN" | head -1)
check "the stream is still in ingest generation $G1 (now $G3)" test -n "$G1" -a "$G3" = "$G1"

log "4. edge A back on its plaintext addresses"
kill "$PUB" 2>/dev/null
PUB=""
delete_stream "$SID"
SID=""
RESTORE_TS=$(utc_now)
if ! restore_plaintext || ! helmsman_restart; then fail "restore edge A's control addresses"; fi
SWITCHED=0
plaintext_registered() {
  holder_since "$RESTORE_TS" && dials "$RESTORE_TS" | grep -q '^Connecting to Foghorn without TLS '
}
eventually 120 "edge A registers over its plaintext addresses again" plaintext_registered
finish
