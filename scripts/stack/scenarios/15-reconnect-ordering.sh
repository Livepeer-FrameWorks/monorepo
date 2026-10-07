#!/usr/bin/env bash
# stack-scenario: default
# An encoder reconnects on the same edge while its previous connection's
# PUSH_INPUT_CLOSE is still undelivered in Helmsman's trigger WAL. Helmsman must
# hold the new PUSH_REWRITE until Foghorn has acknowledged that runtime's close,
# so the new publisher is admitted on its first attempt as a new generation and
# Foghorn never refuses it as a duplicate.
# Foghorn acknowledges a close within milliseconds of receiving it, so the cell's
# Foghorn instances are paused (docker pause) before the publisher disconnects:
# the control stream stays open, the close is written to the WAL and sent, and
# it stays unacknowledged. The encoder reconnects once the WAL holds the close.
# Foghorn is resumed as soon as Helmsman logs that the PUSH_REWRITE waits for the
# runtime's end triggers, inside the 4 s PUSH_REWRITE budget. Each step waits on
# the event that enables it, so every run exercises the ordering. A PUSH_REWRITE
# that finishes without that wait while the close is provably unacknowledged is
# the regression.
# Catches: A4 (PUSH_REWRITE bypassed the WAL and reached Foghorn before the old
# connection's close: DUPLICATE_INGEST until the WAL drained).
. "$(dirname "$0")/../lib.sh"

EDGE_SERVICE=${STACK_EDGE_A_SERVICE:-edge}
FOGHORNS_A=(foghorn foghorn-2)
need ffmpeg jq curl || finish
stack_service_exists "$EDGE_SERVICE" || { fail "find the $EDGE_SERVICE target"; finish; }

PUBS=() STREAMS=()
FOGHORN_PAUSED=0
cleanup() {
  local p s
  [ "$FOGHORN_PAUSED" = 1 ] && stack_ctl unpause "${FOGHORNS_A[@]}"
  for p in "${PUBS[@]}"; do kill "$p" 2>/dev/null; done
  for s in "${STREAMS[@]}"; do delete_stream "$s"; done
}
trap cleanup EXIT

edge_metric() { # edge_metric <name>: Helmsman's gauge value inside the edge
  stack_exec "$EDGE_SERVICE" curl -s -m 2 http://localhost:18007/metrics 2>/dev/null | awk -v n="$1" '$1 == n {print $2; exit}'
}
control_connected() { [ "$(edge_metric helmsman_control_stream_connected)" = 1 ]; }
# pending_closes: the PUSH_INPUT_CLOSE entries Helmsman's WAL holds unacknowledged,
# from its loopback management listener.
pending_closes() {
  local wal
  wal=$(stack_exec "$EDGE_SERVICE" curl -s -m 2 http://localhost:18017/triggers/wal 2>/dev/null) || return 1
  jq -er '[.entries[]? | select(.trigger_type == "PUSH_INPUT_CLOSE")] | length' <<<"$wal"
}
no_pending_close() { [ "$(pending_closes)" = 0 ]; }
close_pending() { local n; n=$(pending_closes) && [ "$n" -ge 1 ]; }
# rewrite_waiting <since>: Helmsman logged that the target's PUSH_REWRITE waits
# for the runtime's undelivered end triggers.
rewrite_waiting() {
  local lines
  lines=$(logs_since "$1" "$EDGE_SERVICE") || return 1
  grep 'PUSH_REWRITE waits for the runtime' <<<"$lines" | grep -F "$T_IN" >/dev/null
}
# rewrite_answered <since>: Helmsman finished a PUSH_REWRITE request from Mist.
rewrite_answered() { log_has "$1" '"path":"/webhooks/mist/push_rewrite"' "$EDGE_SERVICE"; }

S=$(create_stream "stack-reorder-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
[ -n "$SID" ] || { fail "create the target stream: $S"; finish; }
STREAMS+=("$SID")
T_IN=$(internal_name_of "$(echo "$S" | jq -r '.streamId')")
T_PB=$(echo "$S" | jq -r '.playbackId')
T_KEY=$(echo "$S" | jq -r '.streamKey')
eventually 30 "Helmsman control stream connected" control_connected || finish

TARGET=$(publish_until_admitted "$EDGE_A_RTMP" "$T_KEY" 640x360 15 3600) || { fail "target publisher never admitted"; finish; }
PUBS+=("$TARGET")
eventually 90 "target stream live" media_at "$FOGHORN_A_URL" "$T_PB" || finish
G_OLD=$(open_generation "$FOGHORN_A_DB" "$T_IN" | head -1)
[ -n "$G_OLD" ] || { fail "target has no open generation"; finish; }
# Any close still pending now belongs to an earlier publisher and would make the
# WAL check below ambiguous.
eventually 60 "Helmsman's WAL holds no undelivered PUSH_INPUT_CLOSE" no_pending_close || finish

stack_ctl pause "${FOGHORNS_A[@]}" || { fail "pause Foghorn to hold its acknowledgements"; finish; }
FOGHORN_PAUSED=1
kill "$TARGET" 2>/dev/null
eventually 30 "the target's PUSH_INPUT_CLOSE is queued unacknowledged in Helmsman's WAL" close_pending || finish
check "Helmsman's control stream stayed connected while Foghorn is paused" control_connected

START_TS=$(utc_now)
TARGET=$(publish "$EDGE_A_RTMP" "$T_KEY" 640x360 15 3600)
PUBS+=("$TARGET")
# The PUSH_REWRITE either waits (logged at once) or, if it bypassed the WAL,
# reaches the paused Foghorn and is answered when its budget runs out.
deadline=$(($(date +%s) + 30))
waited=0
until [ "$(date +%s)" -ge "$deadline" ]; do
  if rewrite_waiting "$START_TS"; then waited=1; break; fi
  rewrite_answered "$START_TS" && break
  kill -0 "$TARGET" 2>/dev/null || break
  sleep 0.1
done
stack_ctl unpause "${FOGHORNS_A[@]}" || { fail "resume Foghorn"; finish; }
FOGHORN_PAUSED=0
check "Helmsman held the reconnect's PUSH_REWRITE while its previous close was unacknowledged" [ "$waited" = 1 ]

new_generation() {
  G_NEW=$(open_generation "$FOGHORN_A_DB" "$T_IN" | head -1)
  [ -n "$G_NEW" ] && [ "$G_NEW" != "$G_OLD" ]
}
G_NEW=""
eventually 30 "the reconnected publisher holds a new generation" new_generation
EDGE_LOG=$(logs_since "$START_TS" "$EDGE_SERVICE")
FOGHORN_LOG=$(logs_since "$START_TS" "${FOGHORNS_A[@]}")
dup=$(echo "$EDGE_LOG" | grep 'PUSH_REWRITE aborted by Foghorn' | grep -c DUPLICATE_INGEST)
refused=$(echo "$EDGE_LOG" | grep -c 'Refusing PUSH_REWRITE')
{ echo "$EDGE_LOG" | grep -E 'Refusing PUSH_REWRITE|PUSH_REWRITE aborted'
  echo "$FOGHORN_LOG" | grep -E 'PUSH_REWRITE refused|already ingesting|denying'; } | head -4 | cut -c1-400
check "the reconnect was not refused as a duplicate ingest ($dup)" [ "$dup" = 0 ]
check "Helmsman refused no PUSH_REWRITE for undelivered end triggers ($refused)" [ "$refused" = 0 ]
forwarded=$(echo "$EDGE_LOG" | grep "end triggers delivered; forwarding PUSH_REWRITE" | grep -F "$T_IN" | head -1)
[ -n "$forwarded" ] && echo "    ${forwarded:0:300}"
check "Helmsman forwarded the PUSH_REWRITE once the close was acknowledged" [ -n "$forwarded" ]
if kill -0 "$TARGET" 2>/dev/null; then
  pass "admitted on the first attempt (publisher still connected)"
  eventually 60 "the reconnected publisher serves media" media_at "$FOGHORN_A_URL" "$T_PB"
else
  fail "the reconnected publisher exited: $(tail -1 "$STACK_STATE_DIR/publish-$T_KEY.log")"
fi
finish
