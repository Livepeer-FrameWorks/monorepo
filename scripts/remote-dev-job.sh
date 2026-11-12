#!/usr/bin/env bash
# Own a remote command's temporary files and process group together.
set -uo pipefail

if (( $# < 2 )); then
  echo 'usage: remote-dev-job.sh <temporary-root> <command> [args...]' >&2
  exit 2
fi
temporary_root=$1
shift
command -v setsid >/dev/null || { echo 'remote-dev-job: setsid is required' >&2; exit 2; }
command_umask=$(umask)
umask 0077
mkdir -p "$temporary_root" || exit 1
job_tmp=$(mktemp -d "$temporary_root/job.XXXXXXXX") || exit 1
umask "$command_umask"
export TMPDIR="$job_tmp" GOTMPDIR="$job_tmp"
runner_pid=''
signal_status=0

stop_group() {
  [[ -n "$runner_pid" ]] || return 0
  kill -0 -- "-$runner_pid" 2>/dev/null || return 0
  kill -TERM -- "-$runner_pid" 2>/dev/null || true
  local deadline=$((SECONDS + 10))
  while kill -0 -- "-$runner_pid" 2>/dev/null; do
    if (( SECONDS >= deadline )); then
      kill -KILL -- "-$runner_pid" 2>/dev/null || true
      break
    fi
    sleep 0.1
  done
}

cleanup() {
  local status=$?
  trap - EXIT
  # A background compiler must stop before its temporary objects disappear.
  stop_group
  [[ -z "$runner_pid" ]] || wait "$runner_pid" 2>/dev/null || true
  rm -rf -- "$job_tmp"
  exit "$status"
}
trap cleanup EXIT

forward() {
  if [[ -z "$runner_pid" ]]; then
    signal_status=$1
    return
  fi
  trap '' HUP INT TERM
  # TERM reaches the existing test/stack teardown handlers on SSH disconnect.
  stop_group
  exit "$1"
}
trap 'forward 129' HUP
trap 'forward 130' INT
trap 'forward 143' TERM

# Explicit stdin preserves streamed input when the command runs asynchronously.
(( signal_status == 0 )) || exit "$signal_status"
setsid -- "$@" <&0 &
runner_pid=$!
(( signal_status == 0 )) || forward "$signal_status"
wait "$runner_pid"
exit "$?"
