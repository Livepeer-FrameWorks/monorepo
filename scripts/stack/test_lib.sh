#!/usr/bin/env bash
# Unit checks for scripts/stack/lib.sh helpers that run with stubbed
# publishers and databases, so they need no stack.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/fw-stack-lib-test.XXXXXX")
stop_publishers() {
  local pid
  [ -f "$work/pids" ] || return 0
  while read -r pid; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done <"$work/pids"
}
cleanup() {
  stop_publishers
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

revocation_log_failure_is_red() (
  FAIL=0 PASS=0
  SINCE=now REFUSED_AT=later
  foghorn_calls() { printf '0\n'; return 2; }
  sed -n '/^  .*calls=$(foghorn_calls/,/^  gone()/p' "$here/scenarios/18-playback-grant-revocation.sh" |
    sed '$d' >"$work/revocation-log-check.sh"
  [ -s "$work/revocation-log-check.sh" ] || return 1
  . "$work/revocation-log-check.sh"
  [ "$FAIL" = 1 ] && [ "$PASS" = 0 ]
)
expect "an unreadable revocation log never passes as zero Foghorn calls" revocation_log_failure_is_red

absence_requires_readable_logs() (
  LOG_SINCE=now EDGE_A_HOST=edge
  logs_since() { return 1; }
  sed -n '/^no_self_sidecar_fetch()/p' "$here/scenarios/04-recording.sh" >"$work/recording-log-check.sh"
  . "$work/recording-log-check.sh"
  ! no_self_sidecar_fetch
)
expect "a failed journal read cannot prove an event absent" absence_requires_readable_logs
absence_checks_log_content() (
  logs_since() { printf 'ordinary event\n'; }
  log_absent now 'must not happen' edge || return 1
  logs_since() { printf 'must not happen\n'; }
  ! log_absent now 'must not happen' edge || return 1
  ! log_absent now '[' edge 2>/dev/null
)
expect "log absence accepts readable logs only when the event is absent" absence_checks_log_content

ended_requires_readable_session() (
  cell=cell-a internal=internal
  sed -n '/^  ended()/p' "$here/scenarios/26-fresh-ingests.sh" >"$work/ended-check.sh"
  . "$work/ended-check.sh"
  open_generation() { return 1; }
  ! ended || return 1
  open_generation() { printf 'generation\n'; }
  ! ended || return 1
  open_generation() { :; }
  ended
)
expect "a failed session read cannot prove a fresh publisher ended" ended_requires_readable_session

# Stubs. The scenario's clock is the number of admission polls: each poll of
# the ingest sessions is one second of the stubbed publish.
sleep() { :; }
publish() {
  printf 'Sending publish command\n' >"$STACK_STATE_DIR/publish-${2:-key}.log"
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
stop_publishers
rm -f "$work/pids" "$work/polls" "$work/session-queries"
previous_case_publisher_stopped() {
  local _attempt
  for _attempt in {1..100}; do
    kill -0 "$second" 2>/dev/null || return 0
    command sleep 0.01
  done
  kill "$second" 2>/dev/null || true
  return 1
}
expect 'resetting a fixture releases its admitted publisher and inherited admission lock' previous_case_publisher_stopped
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
  stop_publishers
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

refusal_history_probe() (
  local admitted=$1 after_poll=${2:-8}
  echo 0 >"$work/refusal-polls"
  pg() {
    local n=$(($(cat "$work/refusal-polls") + 1))
    echo "$n" >"$work/refusal-polls"
    # Once admitted, its history remains after the 60-second publisher ends.
    if [ "$admitted" = yes ] && [ "$n" -ge "$after_poll" ]; then echo 1; else echo 0; fi
  }
  publish_refused "$FOGHORN_A_DB" internal-1 "$EDGE_A_RTMP" key
)
refusal_rejects_admitted() { ! refusal_history_probe yes; }
expect 'historically admitted publisher is never called refused' refusal_rejects_admitted
refusal_rejects_late_admission() { ! refusal_history_probe yes 31; }
expect 'refusal probe observes admissions at the end of the 35-second admission budget' refusal_rejects_late_admission
expect 'refusal probe accepts a negotiated publisher that creates no admission history' refusal_history_probe no
refusal_rejects_unreachable_edge() (
  publish() { command sleep 300 >/dev/null 2>&1 & echo $! >>"$work/pids"; echo $!; printf 'Connection refused\n' >"$STACK_STATE_DIR/publish-key.log"; }
  ! refusal_history_probe no
)
expect 'refusal probe rejects an attempt that never negotiated with the edge' refusal_rejects_unreachable_edge

operator_calls_refresh_auth() (
  # Read by the scenario function loaded through eval below.
  # shellcheck disable=SC2034
  local logins=0 operator_jwt="" CLI="$work/operator-cli"
  printf '#!/bin/sh\nprintf "%%s\\n" "$FW_USER_TOKEN"\n' >"$CLI"
  chmod +x "$CLI"
  # The evaluated cli_json reads operator_jwt.
  # shellcheck disable=SC2034
  operator_login() { logins=$((logins+1)); operator_jwt="fresh-$logins"; }
  eval "$(sed -n '/^cli_json() {$/,/^}$/p' "$here/scenarios/46-operator-grant-trial.sh")"
  cli_json admin >"$work/operator-call-1"
  cli_json admin >"$work/operator-call-2"
  [ "$(cat "$work/operator-call-1")" = fresh-1 ] && [ "$(cat "$work/operator-call-2")" = fresh-2 ]
)
expect 'every operator CLI invocation refreshes its JWT' operator_calls_refresh_auth
cleanup_failure_is_red() (
  cleanup() { return 1; }
  eval "$(sed -n '/^on_exit() {$/,/^}$/p' "$here/scenarios/46-operator-grant-trial.sh")"
  trap on_exit EXIT
  exit 0
)
if cleanup_failure_is_red; then printf 'FAIL cleanup failure exited green\n'; failures=$((failures+1)); else printf 'ok cleanup failure is red\n'; fi

# Use scenario 46's actual predicate: a route 404 must never count as revocation,
# while the signed inactive-object response intentionally hides the view key.
trial_playback_probe() (
  local probe_code=$1 probe_body=$2
  # Read by playback_denied loaded through eval below.
  # shellcheck disable=SC2034
  tenant=tenant-fixture id=stream-fixture pb=playback-fixture internal=internal-fixture
  curl() { printf '%s\n%s' "$probe_body" "$probe_code"; }
  pg() { echo 1; }
  eval "$(sed -n '/^playback_denied() {$/,/^}$/p' "$here/scenarios/46-operator-grant-trial.sh")"
  playback_denied
)
trial_rejects_route_404() { ! trial_playback_probe 404 '404 page not found'; }
trial_rejects_offline() { ! trial_playback_probe 404 '{"code":"STREAM_OFFLINE"}'; }
trial_accepts_signed_denial() { trial_playback_probe 404 '{"code":"VIEW_KEY_NOT_FOUND"}'; }
expect 'trial playback refusal rejects an unknown-route 404' trial_rejects_route_404
expect 'trial playback refusal rejects an offline publisher' trial_rejects_offline
expect 'trial playback refusal accepts a signed inactive-object response for an existing stream' trial_accepts_signed_denial

revocation_clock_precedes_auxiliary_calls() {
  python3 - "$here/scenarios/46-operator-grant-trial.sh" <<'CLOCK'
import sys
source=open(sys.argv[1]).read()
mutation=source.index('cli_json admin users deactivate')
capture=source.find('revocation_started_ms=$(monotonic_ms)',mutation)
login=source.index('code=$(user_login)',mutation)
assert mutation < capture < login, 'revocation clock must be captured before auxiliary login/token checks'
CLOCK
}
expect 'session deadline begins immediately after the revocation response' revocation_clock_precedes_auxiliary_calls
late_success() { command sleep 1.2; return 0; }
late_success_is_refused() { ! (within_bound_since "$(monotonic_ms)" 1 'late success' late_success) >"$work/late-success.log"; }
expect 'bounded wait refuses a late successful response' late_success_is_refused
expired_start_is_refused() (
  started=$(monotonic_ms) || return 1
  command sleep 1.1
  ! within_bound_since "$started" 1 'already late at helper entry' true >"$work/late-start.log"
)
expect 'deadline includes work before the polling helper starts' expired_start_is_refused
expect 'bounded wait accepts an immediate success' within_bound_since "$(monotonic_ms)" 1 'immediate success' true

revocation_publishers_outlive_observation() {
  python3 - "$here/scenarios/46-operator-grant-trial.sh" <<'PUBLISH'
import re,sys
source=open(sys.argv[1]).read()
constants={key:int(value) for key,value in re.findall(r'^(\w+)=(\d+)$',source,re.M)}
required=sum(constants[key] for key in ['MEDIA_BOUND_S','INGEST_STOP_BOUND_S','READMISSION_BOUND_S'])
calls=re.findall(r'publish_until_admitted[^\n]+?320x240 10 ([^ ]+) ',source[source.index('api_admitted()'):])
assert len(calls)==2, 'must exercise both live revocations'
for value in calls:
    duration=constants[value.strip('"${}') ] if '$' in value else int(value)
    assert duration>required, f'publisher ends naturally inside revocation observation: {duration}s <= {required}s'
PUBLISH
}
expect 'revocation publishers cannot finish naturally during the assertion window' revocation_publishers_outlive_observation

expired_publisher_does_not_prove_termination() {
  publisher_stopped_before_deadline 0 true
  [ "$?" = 2 ]
}
expect 'a naturally expired publisher cannot prove termination' expired_publisher_does_not_prove_termination

managed_access_cleanup_restores_the_snapshot() (
  trap - EXIT
  export FIXTURES=0 BILLING_FIXTURE=0 ACCESS_FIXTURES=1 SOURCE_PID=""
  export TENANT=fixture-tenant CELL_A_CLUSTER=cell-a CELL_B_CLUSTER=cell-b WORK="$work/access-cleanup"
  mkdir -p "$WORK"
  printf 'original-source-and-expiry\n' >"$WORK/access-cell-a.before"
  : >"$WORK/access-cell-b.before"
  printf 'changed-by-fixture\n' >"$WORK/current-cell-a"
  printf 'new-fixture-row\n' >"$WORK/current-cell-b"
  restore_cluster_access_fixture() {
    local cluster=$2 snapshot=$3
    if [ -s "$snapshot" ]; then cp "$snapshot" "$WORK/current-$cluster"; else rm "$WORK/current-$cluster"; fi
  }
  eval "$(sed -n '/^cleanup() {$/,/^}$/p' "$here/scenarios/48-direct-replica-reopen.sh")"
  cleanup
  [ "$(cat "$WORK/current-cell-a")" = original-source-and-expiry ] && [ ! -e "$WORK/current-cell-b" ]
)
expect 'managed playback cleanup restores the prior access row and removes only its new fixture' managed_access_cleanup_restores_the_snapshot

# A failed command leaves its captured output empty. jq 1.6, which the runner
# image installs, exits 0 under -e on empty input, so an empty capture must
# never satisfy a filter, however permissive.
json_has_refuses_empty_input() { ! json_has '' '(.items // []) | length == 0' && ! json_has $'\n ' 'true'; }
expect 'json_has never matches empty output' json_has_refuses_empty_input
json_has_matches_json() { json_has '{"items":[]}' '(.items // []) | length == 0' && ! json_has '{"items":[1]}' '.items | length == 0'; }
expect 'json_has still decides real JSON' json_has_matches_json

if [ "$failures" -gt 0 ]; then
  echo "lib.sh checks: $failures failed"
  exit 1
fi
echo "lib.sh checks: ok"
