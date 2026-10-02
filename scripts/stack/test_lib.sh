#!/usr/bin/env bash
# Unit checks for scripts/stack/lib.sh helpers that run with stubbed
# publishers and databases, so they need no stack.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/fw-stack-lib-test.XXXXXX")
cleanup() {
  local pid
  while read -r pid; do kill "$pid" 2>/dev/null; done <"$work/pids" 2>/dev/null
  rm -rf "$work"
}
trap cleanup EXIT

export STACK_ENDPOINTS_FILE=/dev/null STACK_STATE_DIR="$work/state"
# shellcheck source=scripts/stack/lib.sh
. "$here/lib.sh"
failures=0
expect() { # expect <description> <command...>
  local what=$1
  shift
  if "$@"; then printf 'ok    %s\n' "$what"; else printf 'FAIL  %s\n' "$what"; failures=$((failures + 1)); fi
}

# Stubs. The scenario's clock is the number of admission polls: each poll of
# the ingest sessions is one second of the stubbed publish.
sleep() { :; }
publish() {
  command sleep 300 >/dev/null 2>&1 &
  echo $! >>"$work/pids"
  echo $!
}
pg() {
  local db=$1 sql=$2 n pid
  case "$sql" in
  *commodore.streams*) echo "internal-1" ;;
  *foghorn.ingest_sessions*)
    printf '%s\n' "$db $sql" >>"$work/session-queries"
    n=$(($(cat "$work/polls" 2>/dev/null || echo 0) + 1))
    echo "$n" >"$work/polls"
    pid=$(tail -n 1 "$work/pids")
    # The first publisher is held while Mist retries a slow PUSH_REWRITE and
    # refused after ten seconds; the second one is admitted.
    if [ "$(wc -l <"$work/pids" | tr -d ' ')" = 1 ]; then
      [ "$n" -ge 10 ] && { kill "$pid"; wait "$pid" 2>/dev/null; }
      return 0
    fi
    echo "generation-2"
    ;;
  esac
}

# A publisher that stays connected while its push is still being decided is not
# admitted: the helper waits for the platform's admitted session, and restarts a
# publisher that is refused after its liveness window.
got=$(publish_until_admitted "$EDGE_A_RTMP" sk_test 640x360 15 60 2>/dev/null)
first=$(sed -n 1p "$work/pids")
second=$(sed -n 2p "$work/pids")
returned_second() { [ -n "$second" ] && [ "$got" = "$second" ]; }
expect "a publisher refused after 8 s is restarted and the admitted one is returned (got '$got', publishers: $first $second)" returned_second
asked_cell_a() { grep -qs "^$FOGHORN_A_DB " "$work/session-queries"; }
expect "admission is read from the publishing edge's cell" asked_cell_a
asked_active_since_start() { grep -qs "projection_state='active'" "$work/session-queries" && grep -qs "started_at >= to_timestamp(" "$work/session-queries"; }
expect "only an active session opened by this publish counts" asked_active_since_start

# A publisher that never gets admitted within the timeout is reported as such.
rm -f "$work/pids" "$work/polls" "$work/session-queries"
pg() {
  case "$2" in
  *commodore.streams*) echo "internal-1" ;;
  esac
}
never_admitted() { ! publish_until_admitted "$EDGE_B_RTMP" sk_test 640x360 15 60 2 >/dev/null 2>&1; }
expect "a publisher never admitted is a failure, not a live PID" never_admitted
publishers_stopped() {
  local pid
  while read -r pid; do kill -0 "$pid" 2>/dev/null && return 1; done <"$work/pids"
  return 0
}
expect "unadmitted publishers are stopped" publishers_stopped

if [ "$failures" -gt 0 ]; then
  echo "lib.sh checks: $failures failed"
  exit 1
fi
echo "lib.sh checks: ok"
