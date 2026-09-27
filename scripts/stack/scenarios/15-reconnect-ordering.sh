#!/usr/bin/env bash
# stack-scenario: default
# An encoder reconnects on the same edge while its previous connection's
# PUSH_INPUT_CLOSE is still queued in Helmsman's trigger WAL. The control path
# is cut, the old publisher (and a handful of other publishers, whose end
# triggers queue ahead of it) disconnect during the cut, control is restored,
# and the encoder reconnects the moment Helmsman's control stream is back, while
# the WAL is still draining. Helmsman must deliver that runtime's close before
# forwarding the new PUSH_REWRITE, so the new publisher is admitted on its first
# attempt as a new generation and Foghorn never refuses it as a duplicate.
# The cut rejects TCP to and from Foghorn's control port inside the edge's
# network namespace (lib.sh netfault_*); RTMP to the edge keeps flowing.
# Helmsman's forwarder delivers a queued close within milliseconds of the
# reconnect, so Decklog is paused across the restore: Foghorn receives the
# queued closes and ends their ingest sessions but cannot acknowledge them, and
# the target's close is still undelivered when the encoder reconnects. Decklog
# resumes as soon as Helmsman logs that the PUSH_REWRITE waits for it, and at
# most 1.5 s after the encoder starts, so the close can still be acknowledged
# within the 4 s PUSH_REWRITE budget. Exercised only when Helmsman logs that
# wait; a cycle without it proves nothing and is repeated, up to
# STACK_REORDER_CYCLES.
# Catches: A4 (PUSH_REWRITE bypassed the WAL and reached Foghorn before the old
# connection's close: DUPLICATE_INGEST until the WAL drained).
. "$(dirname "$0")/../lib.sh"

EDGE_SERVICE=${STACK_EDGE_A_SERVICE:-edge}
CONTROL_PORT=${STACK_FOGHORN_CONTROL_PORT:-18029}
BACKLOG=${STACK_REORDER_BACKLOG_STREAMS:-6}
CYCLES=${STACK_REORDER_CYCLES:-3}
FOGHORNS_A=(foghorn foghorn-2)
need ffmpeg jq curl python3 docker || finish
netfault_start "$EDGE_SERVICE" || finish
EDGE_CONTAINER=$(container_of "$EDGE_SERVICE")
[ -n "$EDGE_CONTAINER" ] || { fail "find the $EDGE_SERVICE container"; finish; }

PUBS=() STREAMS=()
DECKLOG_PAUSED=0
cleanup() {
  local p s
  [ "$DECKLOG_PAUSED" = 1 ] && stack_ctl unpause decklog
  netfault_stop "$EDGE_SERVICE"
  for p in "${PUBS[@]}"; do kill "$p" 2>/dev/null; done
  for s in "${STREAMS[@]}"; do delete_stream "$s"; done
}
trap cleanup EXIT

new_stream() { # new_stream <name>: sets NS_IN, NS_PB, NS_KEY
  local s id
  s=$(create_stream "$1" false)
  id=$(echo "$s" | jq -r '.id // empty')
  [ -n "$id" ] || { echo "createStream: $s" >&2; return 1; }
  STREAMS+=("$id")
  NS_IN=$(internal_name_of "$(echo "$s" | jq -r '.streamId')")
  NS_PB=$(echo "$s" | jq -r '.playbackId')
  NS_KEY=$(echo "$s" | jq -r '.streamKey')
}
edge_metric() { # edge_metric <name>: Helmsman's gauge value inside the edge
  stack_exec "$EDGE_SERVICE" curl -s -m 2 http://localhost:18007/metrics 2>/dev/null | awk -v n="$1" '$1 == n {print $2; exit}'
}
control_connected() { [ "$(edge_metric helmsman_control_stream_connected)" = 1 ]; }
control_down() { ! control_connected; }
# rewrite_waiting <since>: Helmsman logged that the target's PUSH_REWRITE waits
# for the runtime's undelivered end triggers. Read from the container directly:
# the check runs inside the PUSH_REWRITE budget.
rewrite_waiting() {
  docker logs --since "$1" "$EDGE_CONTAINER" 2>&1 | grep 'PUSH_REWRITE waits for the runtime' | grep -qF "$T_IN"
}

new_stream "stack-reorder-$(date +%s)" || { fail "create the target stream"; finish; }
T_IN=$NS_IN T_PB=$NS_PB T_KEY=$NS_KEY
BL_KEYS=() BL_INS=()
for i in $(seq 1 "$BACKLOG"); do
  new_stream "stack-reorder-backlog-$i-$(date +%s)" || { fail "create backlog stream $i"; finish; }
  BL_KEYS+=("$NS_KEY")
  BL_INS+=("$NS_IN")
done
eventually 30 "Helmsman control stream connected" control_connected

TARGET=$(publish_until_admitted "$EDGE_A_RTMP" "$T_KEY" 640x360 15 3600) || { fail "target publisher never admitted"; finish; }
PUBS+=("$TARGET")
eventually 90 "target stream live" media_at "$FOGHORN_A_URL" "$T_PB"

exercised=0 admitted=0
for cycle in $(seq 1 "$CYCLES"); do
  log "cycle $cycle: $BACKLOG backlog publishers + the target, control cut, reconnect on restore"
  failed_before=$FAIL
  if [ -z "$(open_generation "$FOGHORN_A_DB" "$T_IN")" ]; then
    TARGET=$(publish_until_admitted "$EDGE_A_RTMP" "$T_KEY" 640x360 15 3600) || { fail "target publisher never admitted"; break; }
    PUBS+=("$TARGET")
  fi
  BL_PIDS=()
  for key in "${BL_KEYS[@]}"; do
    p=$(publish_until_admitted "$EDGE_A_RTMP" "$key" 320x240 10 3600) || { fail "a backlog publisher was never admitted"; break 2; }
    PUBS+=("$p")
    BL_PIDS+=("$p")
  done
  backlog_open() {
    local in n=0
    for in in "${BL_INS[@]}"; do [ -n "$(open_generation "$FOGHORN_A_DB" "$in")" ] && n=$((n + 1)); done
    [ "$n" = "$BACKLOG" ]
  }
  eventually 90 "backlog publishers admitted" backlog_open
  G_OLD=$(open_generation "$FOGHORN_A_DB" "$T_IN" | head -1)
  [ -n "$G_OLD" ] || { fail "target has no open generation before the cut"; break; }

  netfault "$EDGE_SERVICE" sh -c "iptables -A FW_FAULT_OUT -p tcp --dport $CONTROL_PORT -j REJECT --reject-with tcp-reset &&
    iptables -A FW_FAULT_IN -p tcp --sport $CONTROL_PORT -j REJECT --reject-with tcp-reset &&
    { ss -K '( dport = :$CONTROL_PORT )' >/dev/null 2>&1 || true; }" || { fail "install the control-port rules"; break; }
  eventually 30 "Helmsman control stream down" control_down
  for p in "${BL_PIDS[@]}"; do kill "$p" 2>/dev/null; done
  sleep 2
  kill "$TARGET" 2>/dev/null
  # Mist reports the closed connections (PUSH_INPUT_CLOSE) to Helmsman, which
  # can only queue them.
  sleep 4
  echo "    WAL pending before restore: $(edge_metric helmsman_trigger_wal_pending)"

  stack_ctl pause decklog || { fail "pause decklog to hold Foghorn's acknowledgements"; break; }
  DECKLOG_PAUSED=1
  RESTORE_TS=$(utc_now)
  netfault_clear "$EDGE_SERVICE" || { fail "clear the control-port rules"; break; }
  deadline=$(($(date +%s) + 90))
  until control_connected || [ "$(date +%s)" -ge "$deadline" ]; do sleep 0.1; done
  pending=$(edge_metric helmsman_trigger_wal_pending)
  TARGET=$(publish "$EDGE_A_RTMP" "$T_KEY" 640x360 15 3600)
  PUBS+=("$TARGET")
  echo "    reconnected publisher started with ${pending:-?} WAL entries pending"
  release_at=$(($(date +%s%N) + 1500000000))
  until rewrite_waiting "$RESTORE_TS" || [ "$(date +%s%N)" -ge "$release_at" ]; do sleep 0.1; done
  stack_ctl unpause decklog || { fail "resume decklog"; break; }
  DECKLOG_PAUSED=0

  new_generation() {
    G_NEW=$(open_generation "$FOGHORN_A_DB" "$T_IN" | head -1)
    [ -n "$G_NEW" ] && [ "$G_NEW" != "$G_OLD" ]
  }
  G_NEW=""
  eventually 30 "cycle $cycle: the reconnected publisher holds a new generation" new_generation
  sleep 5
  if kill -0 "$TARGET" 2>/dev/null; then
    pass "cycle $cycle: admitted on the first attempt (publisher still connected)"
    admitted=1
  else
    fail "cycle $cycle: the reconnected publisher exited: $(tail -1 "$STACK_STATE_DIR/publish-$T_KEY.log")"
    admitted=0
  fi
  # Only the target publishes after the restore, so every PUSH_REWRITE outcome
  # in this window is the reconnect's.
  EDGE_LOG=$(logs_since "$RESTORE_TS" "$EDGE_SERVICE")
  FOGHORN_LOG=$(logs_since "$RESTORE_TS" "${FOGHORNS_A[@]}")
  dup=$(echo "$EDGE_LOG" | grep 'PUSH_REWRITE aborted by Foghorn' | grep -c DUPLICATE_INGEST)
  refused=$(echo "$EDGE_LOG" | grep -c 'Refusing PUSH_REWRITE')
  { echo "$EDGE_LOG" | grep -E 'Refusing PUSH_REWRITE|PUSH_REWRITE aborted'
    echo "$FOGHORN_LOG" | grep -E 'PUSH_REWRITE refused|already ingesting|denying'; } | head -4 | cut -c1-400
  check "cycle $cycle: the reconnect was not refused as a duplicate ingest ($dup)" [ "$dup" = 0 ]
  check "cycle $cycle: Helmsman refused no PUSH_REWRITE for undelivered end triggers ($refused)" [ "$refused" = 0 ]
  waited=$(echo "$EDGE_LOG" | grep 'PUSH_REWRITE waits for the runtime' | grep -F "$T_IN" | head -1)
  if [ -n "$waited" ]; then
    echo "    ${waited:0:300}"
    echo "$EDGE_LOG" | grep "end triggers delivered; forwarding PUSH_REWRITE" | grep -F "$T_IN" | head -1 | cut -c1-300
    exercised=1
    break
  fi
  echo "    Helmsman never held the target's PUSH_REWRITE for its close"
  # A refused reconnect is the finding; later cycles would only repeat it.
  [ "$FAIL" -gt "$failed_before" ] && break
done
if [ "$exercised" = 1 ]; then
  pass "the reconnect raced the queued close (Helmsman held the PUSH_REWRITE for it)"
elif [ "$FAIL" = 0 ]; then
  blocked "no cycle held the reconnect's PUSH_REWRITE for the queued close; the ordering was not exercised"
fi
[ "$admitted" = 1 ] && eventually 60 "the reconnected publisher serves media" media_at "$FOGHORN_A_URL" "$T_PB"
finish
