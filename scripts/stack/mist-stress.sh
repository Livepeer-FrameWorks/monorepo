#!/usr/bin/env bash
# Exercise real Mist clip processing under bounded CPU load. The stack's clip
# burst is repeated through verify.sh, including its per-run log assertions.
# MIST_SOURCE_DIR selects the fork checkout when bringing up a fresh slot.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
workers=${MIST_STRESS_CPU_WORKERS:-2}
repeat=${MIST_STRESS_RUNS:-4}
case "$workers:$repeat" in
  *[!0-9:]* | :* | *:) echo 'MIST_STRESS_CPU_WORKERS and MIST_STRESS_RUNS must be integers' >&2; exit 2 ;;
esac
[ "$workers" -le 16 ] && [ "$repeat" -ge 1 ] && [ "$repeat" -le 100 ] || exit 2

load_pids=()
cleanup() {
  for pid in "${load_pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${load_pids[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT
for _ in $(seq 1 "$workers"); do
  bash -c 'while :; do :; done' >/dev/null 2>&1 &
  load_pids+=("$!")
done

STACK_SCENARIOS=24 STACK_REPEAT="$repeat" bash "$here/verify.sh"
