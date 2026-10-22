#!/usr/bin/env bash
# One command for "is the monorepo still working end to end": brings up a
# production-shaped stack slot, runs the scenarios inside it, checks every
# container's logs for defect signatures, and tears the slot down.
#
#   STACK_SLOT=N            slot (default 1)
#   STACK_SCENARIOS=...     default | manual | all | 01,04,... (scenarios/list.sh)
#   MIST_SOURCE_DIR=<dir>   build MistServer from a local checkout
#   STACK_KEEP=1            leave the slot running afterwards (for debugging)
#   STACK_REUSE=1           run against an already-up slot instead of up.sh
#   STACK_REPEAT=N          repeat each selected scenario N times (default 1)
#   STACK_DVR_WINDOW_SECONDS=N  the demo tier's DVR window, so the chapter length
#                           scenarios 04 and 39 record (default 120; 39 needs >=1800)
#   STACK_SWEEP=0           skip the post-run invariant sweep (sweep.sh)
#
# Exit status: 0 all passed; 1 a scenario or the log check failed; 2 nothing
# failed but a scenario was BLOCKED (the stack lacked something it needs).
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ "${STACK_TARGET:-stack}" = staging ]; then
  exec bash "$here/verify-target.sh"
fi
# shellcheck source=scripts/stack/common.sh
. "$here/common.sh"
cd "$stack_repo_root" || exit 1
# Another checkout's stack in this slot is neither reused nor torn down.
stack_require_slot || exit 1

teardown() {
  local rc=$?
  # up.sh refused because another checkout claimed the slot meanwhile; its
  # stack is not this run's evidence.
  [ -z "$(stack_slot_foreign_owner)" ] || return 0
  # Keep the evidence of a failed run: containers are about to go away.
  if [ "$rc" -ne 0 ]; then
    stack_compose logs --no-color --timestamps >"$STACK_STATE_DIR/compose.log" 2>&1 || true
    echo "compose logs of the failed run: $STACK_STATE_DIR/compose.log"
  fi
  if [ "${STACK_KEEP:-}" = 1 ]; then
    echo "STACK_KEEP=1: slot $STACK_SLOT left running (scripts/stack/down.sh removes it)"
  else
    bash "$here/down.sh" >/dev/null 2>&1 || true
  fi
}

# A fresh slot's whole log is this run, bring-up included; a reused slot is
# checked from now on only.
logs_since=""
if [ "${STACK_REUSE:-}" = 1 ] && [ -f "$STACK_STATE_DIR/ready" ]; then
  echo "reusing slot $STACK_SLOT (ready since $(cat "$STACK_STATE_DIR/ready"))"
  logs_since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
else
  trap teardown EXIT
  # A fresh run starts from an empty slot: volumes a kept run left behind were
  # initialized with that run's generated secrets.
  bash "$here/down.sh" || exit 1
  rm -f "$ENV_FILE"
  bash "$here/up.sh" || exit 1
fi

# The TS SDK smoke in stack-runner imports npm_api's built dist from the
# mounted checkout.
# shellcheck source=scripts/stack/sdk-prereqs.sh
. "$here/sdk-prereqs.sh"
build_npm_api_dist "$stack_repo_root"

scenarios=()
while IFS= read -r path; do
  [ -n "$path" ] && scenarios+=("$path")
done < <(stack_compose exec -T -e STACK_SCENARIOS="${STACK_SCENARIOS:-default}" stack-runner \
  bash /repo/scripts/stack/scenarios/list.sh)
[ "${#scenarios[@]}" -gt 0 ] || { echo "no scenarios selected (STACK_SCENARIOS=${STACK_SCENARIOS:-default})"; exit 1; }
if printf '%s\n' "${scenarios[@]}" | grep -Eq '/(27|28)-'; then
  echo 'building the operator CLI for the selected stack scenarios'
  make build-bin-cli || exit 1
fi
if printf '%s\n' "${scenarios[@]}" | grep -Eq '/27-'; then
  make build-stack-billing-runner || exit 1
fi
repeat=${STACK_REPEAT:-1}
case "$repeat" in
  '' | *[!0-9]*) echo "STACK_REPEAT must be an integer from 1 to 100" >&2; exit 1 ;;
esac
if [ "$repeat" -lt 1 ] || [ "$repeat" -gt 100 ]; then
  echo "STACK_REPEAT must be an integer from 1 to 100" >&2
  exit 1
fi

# A service container that exited with an error or was OOM-killed breaks the
# scenarios that route through it, which would otherwise read as a product
# failure elsewhere. One-shot services exit 0 and are not listed.
dead_containers() {
  local id
  for id in $(stack_compose ps -a -q 2>/dev/null); do
    docker inspect -f '{{.Name}} {{.State.Status}} exit={{.State.ExitCode}} oom={{.State.OOMKilled}}' "$id" 2>/dev/null
  done | awk '$2 != "running" && ($3 != "exit=0" || $4 == "oom=true") { sub(/^\//, "", $1); print }'
}

failed=0 blocked=0
summary=()
# The sweep checks every entity created from here on, attributes each to the
# scenario whose window it falls in, and counts container restarts against
# what the containers had before the scenarios.
run_since=$(date +%s)
sweep_windows="$STACK_STATE_DIR/sweep-scenarios.tsv"
sweep_baseline="$STACK_STATE_DIR/sweep-restarts.txt"
: >"$sweep_windows"
for id in $(stack_compose ps -a -q 2>/dev/null); do
  docker inspect -f '{{index .Config.Labels "com.docker.compose.service"}} {{.RestartCount}}' "$id" 2>/dev/null
done >"$sweep_baseline"
dead="$(dead_containers)"
if [ -n "$dead" ]; then
  printf 'containers down before the scenarios:\n%s\n' "$dead"
  summary+=("FAIL     containers down before the scenarios")
  failed=1
fi
for path in "${scenarios[@]}"; do
  name="$(basename "$path" .sh)"
  for iteration in $(seq 1 "$repeat"); do
    label="$name ($iteration/$repeat)"
    printf '\n==== %s ====\n' "$label"
    scenario_since="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
    window_start=$(date +%s)
    stack_compose exec -T -e STACK_DVR_WINDOW_SECONDS="${STACK_DVR_WINDOW_SECONDS:-120}" stack-runner bash "$path"
    rc=$?
    printf '%s\t%s\t%s\n' "$name" "$window_start" "$(date +%s)" >>"$sweep_windows"
    case "$rc" in
      0) summary+=("PASS     $label") ;;
      2) summary+=("BLOCKED  $label"); blocked=1 ;;
      *) summary+=("FAIL     $label (exit $rc)"); failed=1 ;;
    esac
    if bash "$here/logcheck.sh" "$scenario_since" "$name"; then
      summary+=("PASS     $label logs")
    else
      summary+=("FAIL     $label logs")
      failed=1
    fi
  done
done

dead="$(dead_containers)"
if [ -n "$dead" ]; then
  printf '\ncontainers down after the scenarios:\n%s\n' "$dead"
  summary+=("FAIL     containers down after the scenarios")
  failed=1
fi

printf '\n==== logcheck ====\n'
if bash "$here/logcheck.sh" "$logs_since"; then
  summary+=("PASS     logcheck")
else
  summary+=("FAIL     logcheck")
  failed=1
fi

if [ "${STACK_SWEEP:-1}" != 0 ]; then
  printf '\n==== sweep ====\n'
  sweep_started=$(date +%s)
  stack_compose exec -T -e STACK_SWEEP_WAIT="${STACK_SWEEP_WAIT:-240}" stack-runner bash /repo/scripts/stack/sweep.sh \
    "$run_since" "/repo/.stack/slot-$STACK_SLOT/sweep-scenarios.tsv" "/repo/.stack/slot-$STACK_SLOT/sweep-restarts.txt"
  rc=$?
  label="sweep ($(($(date +%s) - sweep_started))s)"
  case "$rc" in
    0) summary+=("PASS     $label") ;;
    2) summary+=("BLOCKED  $label"); blocked=1 ;;
    *) summary+=("FAIL     $label"); failed=1 ;;
  esac
fi

printf '\n==== stack slot %s summary ====\n' "$STACK_SLOT"
printf '%s\n' "${summary[@]}"
[ "$failed" -ne 0 ] && exit 1
[ "$blocked" -ne 0 ] && exit 2
exit 0
