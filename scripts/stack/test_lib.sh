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

# Values are read by the extracted production loop and ingress_specs.
# shellcheck disable=SC2034
session_ingress_loop_preserves_input() (
  local source="$here/scenarios/56-session-validator-revocation.sh"
  local WORK="$work/session-ingresses" descriptor tenant=fixture-tenant
  local STACK_QUARTERMASTER_GRPC_ADDR=quartermaster STACK_PURSER_GRPC_ADDR=purser
  local STACK_PERISCOPE_GRPC_ADDR=periscope STACK_SIGNALMAN_GRPC_ADDR=signalman
  local STACK_BOSUN_GRPC_ADDR=bosun STACK_LOOKOUT_GRPC_ADDR=lookout
  mkdir -p "$WORK"
  descriptor="$WORK/descriptor"
  printf 'fixture-token\n' >"$WORK/revoked.jwt"
  eval "$(sed -n '/^ingress_specs() {$/,/^}$/p' "$source")"
  python3 - "$source" "$WORK/loop.sh" <<'INGRESS'
import pathlib,sys
source=pathlib.Path(sys.argv[1]).read_text()
start=source.index("while IFS='|' read -r service address method body; do")
end=source.index('done < <(ingress_specs)',start)+len('done < <(ingress_specs)')
pathlib.Path(sys.argv[2]).write_text(source[start:end])
INGRESS
  stack_exec() {
    if [ "$2" = test ]; then
      printf '%s\n' "$4" >>"$WORK/retained"
      # docker compose exec forwards stdin even for a command that ignores it.
      cat >/dev/null
    fi
  }
  must_check() { shift; "$@"; }
  ingress_rpc() { return 0; }
  rpc_error() { return 1; }
  . "$WORK/loop.sh"
  [ "$(wc -l <"$WORK/retained" | tr -d ' ')" = 6 ] || {
    printf 'expected six retained ingress descriptors, got:\n' >&2
    cat "$WORK/retained" >&2
    return 1
  }
)
expect 'all six ingress descriptors survive interactive exec stdin forwarding' session_ingress_loop_preserves_input

revocation_log_failure_is_red() (
  FAIL=0 PASS=0
  SINCE=now REFUSED_AT=later
  foghorn_calls() { printf '0\n'; return 2; }
  sed -n '/^  .*calls=$(foghorn_calls/,/^  gone()/p' "$here/scenarios/18-playback-grant-revocation.sh" |
    sed '$d' >"$work/revocation-log-check.sh"
  [ -s "$work/revocation-log-check.sh" ] || return 1
  # The fixture's own FAIL line goes to a file: on the runner's output it
  # reads as a failed check although this one passed.
  . "$work/revocation-log-check.sh" >"$work/revocation-log-check.out"
  [ "$FAIL" = 1 ] && [ "$PASS" = 0 ] &&
    grep -q 'could not read per-viewer trigger calls before the refusal' "$work/revocation-log-check.out"
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
  # Like ffmpeg flushing its output, the publisher takes a moment to exit on SIGTERM.
  bash -c 'trap "command sleep 0.3; exit 0" TERM; command sleep 300 & wait' >/dev/null 2>&1 &
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

recording_setup_case() (
  local mode=$1 outer_pids="$work/pids"
  work="$work/recording-$mode"
  mkdir -p "$work"
  trap - EXIT
  PUB=""
  log() { :; }
  # Advance only the fixture's admission clock; no live publishers or real
  # network calls are needed to exercise the scenario's readiness boundary.
  date() {
    local now=$(($(cat "$work/clock" 2>/dev/null || echo 0) + 10))
    echo "$now" >"$work/clock"
    echo "$now"
  }
  create_stream() {
    local n=$(($(cat "$work/streams" 2>/dev/null || echo 0) + 1))
    echo "$n" >"$work/streams"
    printf '{"streamKey":"key-%s"}\n' "$n"
  }
  internal_name_of_key() { echo "$1"; }
  publish() {
    local key=$2 n=$(($(cat "$work/attempts-$2" 2>/dev/null || echo 0) + 1))
    echo "$n" >"$work/attempts-$key"
    echo "$key" >"$work/current-key"
    command sleep 300 >/dev/null 2>&1 &
    echo $! >>"$work/pids"
    echo $! >>"$outer_pids"
    echo $!
  }
  admitted_generation() {
    local key=$(cat "$work/current-key") pid=$(tail -1 "$work/pids")
    if [ "$(cat "$work/attempts-$key")" = 1 ] || { [ "$mode" = persistent ] && [ "$key" = key-2 ]; }; then
      kill "$pid" 2>/dev/null || true
    else
      echo "generation-$key"
    fi
  }
  sed -n '/^STALLS=4$/,/^stall_dvrs()/p' "$here/scenarios/04-recording.sh" |
    sed '$d' >"$work/setup.sh"
  . "$work/setup.sh"
  echo setup-completed
) >"$work/recording-$1.log" 2>&1
recording_setup_recovers_transient_refusal() {
  recording_setup_case transient || return 1
  grep -qs setup-completed "$work/recording-transient.log" &&
    [ "$(cat "$work/recording-transient/attempts-key-1")" -gt 1 ] &&
    [ "$(cat "$work/recording-transient/streams")" = 4 ]
}
recording_setup_refusal_cleans_prior_publishers() {
  recording_setup_case persistent && return 1
  ! grep -qs setup-completed "$work/recording-persistent.log" || return 1
  local pid state attempt
  while read -r pid; do
    for attempt in {1..100}; do
      state=$(ps -o stat= -p "$pid" 2>/dev/null || true)
      [ -z "$state" ] || [[ "$state" = Z* ]] && break
      command sleep 0.01
    done
    [ -z "$state" ] || [[ "$state" = Z* ]] || return 1
  done <"$work/recording-persistent/pids"
}
expect 'recording setup waits for admitted publishers after transient refusal' recording_setup_recovers_transient_refusal
expect 'recording setup fails persistent refusal and releases earlier publishers' recording_setup_refusal_cleans_prior_publishers

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

refusal_backend_probe() (
  local evidence=$1
  publish() {
    printf '%s\n' "$evidence" >"$STACK_STATE_DIR/publish-key.log"
    command sleep 300 >/dev/null 2>&1 &
    echo $! >>"$work/pids"
    echo $!
  }
  refusal_history_probe no
)
expect 'refusal probe recognizes the librtmp outbound publish command' refusal_backend_probe 'Invoking publish'
refusal_rejects_fc_publish_only() { ! refusal_backend_probe 'Invoking FCPublish'; }
expect 'FCPublish negotiation alone does not prove the publish command was sent' refusal_rejects_fc_publish_only
refusal_rejects_publish_response_only() { ! refusal_backend_probe 'NetStream.Publish.Start'; }
expect 'a publish status response alone does not prove the publish command was sent' refusal_rejects_publish_response_only

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
immediate_success_is_accepted() { within_bound_since "$(monotonic_ms)" 1 'immediate success' true >"$work/immediate-success.log"; }
expect 'bounded wait accepts an immediate success' immediate_success_is_accepted

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

bound_playback_grant_evidence() (
  IN=fixture START=now SERVING=edge
  logs_since() {
    printf '%s\n' '{"msg":"Playback grant applied","internal_name":"live+fixture_fw1p_10000000000040008000000000000002","time":"now"}'
  }
  eval "$(sed -n '/^grant_applied() {/p' "$here/scenarios/17-playback-grants.sh")"
  grant_applied
)
expect 'playback grant evidence accepts the served publisher generation' bound_playback_grant_evidence

runtime_log_evidence_is_exact() (
  local runtime rows
  logs_since() {
    printf '{"msg":"Playback grant applied","internal_name":"%s","tenant_authority_version":2,"object_authority_version":7}\n' "$runtime"
  }
  for runtime in fixture live+fixture pull+fixture \
    live+fixture_fw1p_10000000000040008000000000000002 \
    live+fixture_fw1r_10000000000040008000000000000003; do
    rows=$(log_runtime_json now fixture '.internal_name' edge) || return 1
    [ "$rows" = "$runtime" ] || return 1
    rows=$(log_runtime_json now fixture '"\(.tenant_authority_version)/\(.object_authority_version)"' edge) || return 1
    [ "$rows" = 2/7 ] || return 1
  done
  for runtime in live+fixture-other \
    live+other_fw1p_10000000000040008000000000000002 \
    live+fixture_fw1p_10000000000040008000000000000002_extra \
    live+fixture_fw1p_00000000000000000000000000000000 \
    live+fixture_fw1p_10000000-0000-4000-8000-000000000002 \
    live+fixture_fw1p_A0000000000040008000000000000002 \
    live+fixture_fw1z_10000000000040008000000000000002; do
    rows=$(log_runtime_json now fixture '.internal_name' edge) || return 1
    [ -z "$rows" ] || return 1
  done
)
expect 'runtime log evidence accepts canonical bindings and rejects other streams or malformed names' runtime_log_evidence_is_exact

runtime_log_failure_discards_partial_evidence() (
  local rows
  logs_since() {
    printf '%s\n' '{"msg":"Playback grant applied","internal_name":"live+fixture"}'
    return 1
  }
  if rows=$(log_runtime_json now fixture '.internal_name' edge); then return 1; fi
  [ -z "$rows" ]
)
expect 'a failed runtime-log read cannot prove a grant even after partial output' runtime_log_failure_discards_partial_evidence

restart_counts_requests_not_applications() (
  INS=(fixture) RESTART_TS=now STACK_EDGE_A_SERVICE=edge
  local fixture_requests=${1:-1} unreadable=${2:-0}
  logs_since() {
    local i
    for i in 1 2; do
      printf '%s\n' '{"msg":"Playback grant applied","internal_name":"live+fixture","tenant_authority_version":2,"object_authority_version":7}'
    done
    for ((i=1; i<=fixture_requests; i++)); do
      printf '{"msg":"Playback grant requested","internal_name":"live+fixture","request_id":"request-%s"}\n' "$i"
    done
    [ "$unreadable" = 0 ]
  }
  eval "$(sed -n '/^grant_fetches() {$/,/^}$/p' "$here/scenarios/19-helmsman-restart-grants.sh")"
  python3 - "$here/scenarios/19-helmsman-restart-grants.sh" "$work/restart-fetch-check.sh" <<'RESTART'
import pathlib,sys
source=pathlib.Path(sys.argv[1]).read_text()
start=source.rindex('for in in "${INS[@]}"; do')
end=source.index('\nforwarded=',start)
pathlib.Path(sys.argv[2]).write_text(source[start:end])
RESTART
  PASS=0 FAIL=0
  . "$work/restart-fetch-check.sh" >/dev/null
  if [ "$fixture_requests" = 1 ] && [ "$unreadable" = 0 ]; then
    [ "$PASS" = 1 ] && [ "$FAIL" = 0 ]
  else
    [ "$PASS" = 0 ] && [ "$FAIL" = 1 ]
  fi
)
expect 'restart measures one request even when its grant is applied twice' restart_counts_requests_not_applications
expect 'restart rejects two requests for one stream' restart_counts_requests_not_applications 2
expect 'restart refuses unreadable request evidence' restart_counts_requests_not_applications 1 1

tenant_refusal_requires_unclaimed_unchanged_lease() (
  . "$here/tenant-lifecycle.sh"
  local tenant=fixture uuid=stream internal=logical
  local FOGHORN_A_DB=cell-a FOGHORN_B_DB=cell-b
  local fixture_lease="|7" history=0 unavailable=0
  pg() {
    [ "$unavailable" = 0 ] || return 1
    if [ "$1" = commodore ]; then printf '%s\n' "$fixture_lease"; else printf '%s\n' "$history"; fi
  }
  tenant_refusal_clean 7 || return 1
  fixture_lease='held|7'
  ! tenant_refusal_clean 7 || return 1
  fixture_lease='|8'
  ! tenant_refusal_clean 7 || return 1
  fixture_lease='|7' history=1
  ! tenant_refusal_clean 7 || return 1
  history=0 unavailable=1
  ! tenant_refusal_clean 7
)
expect 'wrong private-ingest target cannot claim ownership, advance epoch or create a session' tenant_refusal_requires_unclaimed_unchanged_lease

tenant_admission_counter_requires_readable_evidence() (
  . "$here/tenant-lifecycle.sh"
  local unavailable=0 empty=0
  docker() {
    [ "$unavailable" = 0 ] || return 1
    [ "$empty" = 0 ] || return 0
    printf '%s\n' 'helmsman_control_stream_connected 1'
    printf '%s\n' 'helmsman_mist_webhook_requests_total{status="received",trigger_type="PUSH_REWRITE"} 2'
    printf '%s\n' 'helmsman_mist_webhook_requests_total{status="success",trigger_type="PUSH_REWRITE"} 2'
    printf '%s\n' 'helmsman_mist_webhook_requests_total{status="received",trigger_type="STREAM_END"} 9'
  }
  [ "$(tenant_admission_count edge)" = 2 ] || return 1
  empty=1
  ! tenant_admission_count edge || return 1
  empty=0
  unavailable=1
  ! tenant_admission_count edge
)
expect 'first private publish counts received admissions and rejects unreadable metrics' tenant_admission_counter_requires_readable_evidence

tenant_flow_rejects_a_second_admission() (
  . "$here/tenant-lifecycle.sh"
  local fixture_extra=${1:-0} tenant=fixture run=fixture since=now streams=() publisher=""
  local FOGHORN_A_URLS=http://cell-a FOGHORN_B_URLS=http://cell-b
  local calls="$work/private-admission-calls" starts="$work/private-publish-starts"
  printf '0\n' >"$calls"
  : >"$starts"
  refresh_tenant_session() { return 0; }
  create_stream() { printf '%s\n' '{"id":"stream","streamId":"uuid","playbackId":"playback","streamKey":"fixture-key"}'; }
  internal_name_of() { printf 'logical\n'; }
  set_placement() { return 0; }
  policy_effective() { return 0; }
  preview_ready() { return 0; }
  pg() { printf '7\n'; }
  tenant_start_publisher() { printf '%s\n' "$2" >>"$starts"; publisher=fixture; }
  tenant_policy_refused() { return 0; }
  tenant_refusal_clean() { return 0; }
  tenant_admission_count() {
    local read_count
    read_count=$(cat "$calls")
    printf '%s\n' "$((read_count + 1))" >"$calls"
    printf '%s\n' "$((read_count * (1 + fixture_extra)))"
  }
  kill() { return 0; }
  eventually() { shift 2; "$@"; }
  stream_active() { return 0; }
  private_media_at() { return 0; }
  stream_ended() { return 0; }
  PASS=0 FAIL=0
  stream_flow TENANT_PRIVATE private-edge cell-a private-node private-cluster rtmp http://cell-a admission public-edge >/dev/null || return 1
  [ "$FAIL" = "$fixture_extra" ] && [ "$(cat "$starts")" = $'public-edge\nprivate-edge' ]
)
expect 'the private flow makes one denied and one correct connection' tenant_flow_rejects_a_second_admission
expect 'a second admission on the correct connection fails the private flow' tenant_flow_rejects_a_second_admission 1

capacity_health_uses_service_auth() (
  local STACK_API_TOKEN=tenant-credential SERVICE_TOKEN=service-for-health
  local node_body='{"nodeId":"edge-capacity"}' FOGHORN_INTERNAL_GRPC_PORT=18019
  local fixture_bin="$work/capacity-rpc-bin"
  mkdir -p "$fixture_bin"
  cat >"$fixture_bin/grpcurl" <<'GRPC'
#!/usr/bin/env bash
for arg in "$@"; do
  if [ "$arg" = 'authorization: Bearer service-for-health' ]; then
    printf '{"isHealthy":true}\n'
    exit 0
  fi
done
echo 'Unauthenticated: internal listener requires service credentials' >&2
exit 16
GRPC
  chmod +x "$fixture_bin/grpcurl"
  export PATH="$fixture_bin:$PATH"
  stack_exec() { shift; "$@"; }
  eval "$(sed -n '/^rpc() {$/,/^}$/p' "$here/scenarios/70-node-capacity.sh")"
  eval "$(sed -n '/^node_health()/p' "$here/scenarios/70-node-capacity.sh")"
  node_health | jq -e '.isHealthy' >/dev/null
)
expect 'capacity health probe authenticates to the internal Foghorn listener' capacity_health_uses_service_auth

rotation_degradation_is_scoped() (
  eval "$(sed -n '/^no_upload_degradation() {$/,/^}$/p' "$here/scenarios/79-rotated-upload.sh")"
  logs_since() { printf '%s\n' '{"msg":"Processing work admitted by memory","artifact_hash":"this-upload"}' '{"msg":"Stream transcode degraded to local processing","stream":"processing+another-upload"}'; }
  no_upload_degradation this-upload now || return 1
  logs_since() { printf '%s\n' '{"msg":"Processing work admitted by memory","artifact_hash":"this-upload"}' '{"msg":"Stream transcode degraded to local processing","stream":"processing+this-upload"}'; }
  ! no_upload_degradation this-upload now || return 1
  logs_since() { return 1; }
  ! no_upload_degradation this-upload now
)
expect 'rotation checks ignore other uploads and fail on unreadable degradation logs' rotation_degradation_is_scoped

ladder_downgrade_uses_tier_model() (
  local STACK_REPO="$work/tier-model" tenant=fixture-tenant
  mkdir -p "$STACK_REPO/bin"
  cat >"$STACK_REPO/bin/cli" <<'TIERCLI'
#!/usr/bin/env bash
case "$*" in *'--tier free --billing-model prepaid'*) exit 1 ;; esac
TIERCLI
  chmod +x "$STACK_REPO/bin/cli"
  eval "$(sed -n '/^set_tier() /p' "$here/scenarios/67-transcode-ladder-downgrade.sh")"
  set_tier free
)
expect 'ladder downgrade uses the tier model instead of forcing Free into prepaid' ladder_downgrade_uses_tier_model

original_attachment_uses_runner_tools() (
  local STACK_STATE_DIR="$work/attachment"
  mkdir -p "$STACK_STATE_DIR"
  printf 'Content-Disposition: attachment; filename="phone.mov"\r\n' >"$STACK_STATE_DIR/original-headers"
  rg() { return 127; }
  check() { shift; "$@"; }
  eval "$(sed -n '/check .*original is an attachment/p' "$here/scenarios/68-vod-original-retention.sh")"
)
expect 'original attachment check works with the runner toolchain' original_attachment_uses_runner_tools

# Scenario 02 after its CPU fallback: the publisher's buffer on edge-node-1 lists
# the masked 480 source and both renditions; a cell-B replica, whose list reached
# Periscope last, lists only the renditions viewers see.
ladder_heights_read_the_origin() (
  local FOGHORN_A_DB=foghorn FOGHORN_B_DB=foghorn_b STACK_TENANT_ID=tenant
  pg() {
    case "$1|$2" in
      commodore\|*"FROM commodore.streams"*) echo internal ;;
      foghorn\|*"FROM foghorn.ingest_sessions"*"ended_at IS NULL"*) echo edge-node-1 ;;
    esac
  }
  ch() {
    local renditions='"r360":{"type":"video","codec":"H264","width":640,"height":360},"r480":{"type":"video","codec":"H264","width":854,"height":480}'
    case "$1" in
      *"node_id = 'edge-node-1'"*) echo "{$renditions,\"src\":{\"type\":\"video\",\"codec\":\"H264\",\"width\":854,\"height\":480}}" ;;
      *) echo "{$renditions}" ;;
    esac
  }
  [ "$(video_heights stream-uuid | tr '\n' ' ')" = "360 480 480 " ]
)
expect 'ladder heights read the publisher buffer, not a newer cell-B replica list' ladder_heights_read_the_origin

if [ "$failures" -gt 0 ]; then
  echo "lib.sh checks: $failures failed"
  exit 1
fi
echo "lib.sh checks: ok"
