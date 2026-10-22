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

placement_retries_refused_setup() (
  trap - EXIT
  rm -f "$work/pids" "$work/polls" "$work/session-queries"
  PASS=0 FAIL=0 BLOCKED=0
  export STACK_PLACEMENT_SECONDS=2
  need() { return 0; }
  log() { :; }
  create_stream() { printf '%s\n' '{"id":"stream-1","playbackId":"playback-1","streamKey":"sk_fixture"}'; }
  delete_stream() { :; }
  pg() {
    case "$2" in
    *commodore.streams*) echo "internal-1" ;;
    *foghorn.ingest_sessions*)
      if [ "$(wc -l <"$work/pids" | tr -d ' ')" = 1 ]; then
        kill "$(tail -n 1 "$work/pids")" 2>/dev/null
      else
        echo "generation-2"
      fi
      ;;
    esac
  }
  media_at() { [ "$(wc -l <"$work/pids" | tr -d ' ')" -gt 1 ]; }
  play_status() { if media_at; then echo 307; else echo 404; fi; }
  curl() { printf '%s\n' '{"primary":{"rtmpUrl":"rtmp://edge/live"}} 200'; }
  eventually() {
    local what=$2
    shift 2
    "$@" || fail "$what"
  }
  # Keep the real scenario body and admission helper; stub only its network
  # dependencies and omit the library bootstrap that would replace the stubs.
  sed '\|^\. .*../lib\.sh"$|d' "$here/scenarios/01-placement.sh" >"$work/placement-scenario.sh"
  . "$work/placement-scenario.sh"
) >"$work/placement.log" 2>&1
placement_setup_is_measured() {
  if placement_retries_refused_setup && grep -qs '^PASS .*viewer resolve via' "$work/placement.log"; then
    return 0
  fi
  tail -15 "$work/placement.log" >&2
  return 1
}
expect "placement measures a live session after its first setup publisher is refused" placement_setup_is_measured

# A deployed target keeps what a run leaves behind, so the run deletes its own
# objects at exit: every stream its ledger created, and every stream, VOD and
# clip of the tenant named as a scenario's ("stack-" streams, "stack " assets),
# which also recovers what a killed earlier run left. Nothing else is touched.
(
  export STACK_STATE_DIR="$work/cleanup"
  mkdir -p "$STACK_STATE_DIR"
  printf '%s\n' '{"t":1,"scenario":"04-recording","kind":"created","id":"ledger-stream","stream_uuid":"u1"}' \
    '{"t":2,"scenario":"05-clip","kind":"created","id":"gone-stream","stream_uuid":"u2"}' \
    '{"t":3,"scenario":"05-clip","kind":"deleted","id":"gone-stream"}' >"$STACK_STATE_DIR/sweep-ledger.jsonl"
  gql() {
    case "$1" in
    *streamsConnection*)
      echo '{"data":{"streamsConnection":{"nodes":[{"id":"stale-stream","name":"stack-recording-1"},{"id":"user-stream","name":"my show"}],"pageInfo":{"hasNextPage":false}}}}'
      ;;
    *storageArtifactsConnection*)
      # A storage artifact's id is not the asset's relay id; deletes take VodAsset:/Clip:<hash>.
      echo '{"data":{"storageArtifactsConnection":{"nodes":[{"kind":"VOD","id":"a1","hash":"vodhash","title":"stack cross-cell vod"},{"kind":"CLIP","id":"a2","hash":"cliphash","title":"stack clip a"},{"kind":"VOD","id":"a3","hash":"userhash","title":"stacking chairs"},{"kind":"DVR","id":"a4","hash":"dvrhash","title":"stack-recording-1"}],"hasNextPage":false}}}'
      ;;
    *deleteStream*) echo "deleteStream $(jq -r .id <<<"$2")" >>"$work/cleanup-calls"; echo '{"data":{"deleteStream":{"__typename":"DeleteSuccess"}}}' ;;
    *deleteVodAsset*) echo "deleteVodAsset $(jq -r .id <<<"$2")" >>"$work/cleanup-calls"; echo '{"data":{"deleteVodAsset":{"__typename":"DeleteSuccess"}}}' ;;
    *deleteClip*) echo "deleteClip $(jq -r .id <<<"$2")" >>"$work/cleanup-calls"; echo '{"data":{"deleteClip":{"__typename":"DeleteSuccess"}}}' ;;
    esac
  }
  cleanup_run_objects >"$work/cleanup.log" 2>&1
) || true
deleted_exactly_the_runs_objects() {
  [ "$(sort "$work/cleanup-calls" 2>/dev/null | tr '\n' ' ')" = "deleteClip $(printf 'Clip:cliphash' | base64) deleteStream ledger-stream deleteStream stale-stream deleteVodAsset $(printf 'VodAsset:vodhash' | base64) " ] ||
    { cat "$work/cleanup-calls" "$work/cleanup.log" >&2 2>/dev/null; return 1; }
}
expect "run cleanup deletes the ledger's and stack-named streams, VODs and clips, and nothing else" deleted_exactly_the_runs_objects

if [ "$failures" -gt 0 ]; then
  echo "lib.sh checks: $failures failed"
  exit 1
fi
echo "lib.sh checks: ok"
