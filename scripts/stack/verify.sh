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
#   STACK_DVR_WINDOW_SECONDS=N  the demo tier's DVR window, so the chapter length
#                           scenario 04 records (default 120)
#
# Exit status: 0 all passed; 1 a scenario or the log check failed; 2 nothing
# failed but a scenario was BLOCKED (the stack lacked something it needs).
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/stack/common.sh
. "$here/common.sh"
cd "$stack_repo_root" || exit 1

teardown() {
  local rc=$?
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

# The TS SDK smoke imports npm_api's built dist from the mounted checkout.
if [ ! -f npm_api/dist/index.js ] || [ -n "$(find npm_api/src -newer npm_api/dist/index.js -print -quit 2>/dev/null)" ]; then
  echo "building npm_api dist for the TS SDK smoke"
  # A fresh workspace (remote-dev slot, CI) has no node_modules; install only
  # what the API package needs.
  if [ ! -d npm_api/node_modules ]; then
    pnpm install --frozen-lockfile --filter '@livepeer-frameworks/api...' 2>&1 | tail -3 ||
      echo "WARN: npm_api dependency install failed"
  fi
  pnpm --filter @livepeer-frameworks/api build 2>&1 | tail -5 ||
    echo "WARN: npm_api build failed; the TS SDK smoke will report BLOCKED"
fi

scenarios=()
while IFS= read -r path; do
  [ -n "$path" ] && scenarios+=("$path")
done < <(stack_compose exec -T -e STACK_SCENARIOS="${STACK_SCENARIOS:-default}" stack-runner \
  bash /repo/scripts/stack/scenarios/list.sh)
[ "${#scenarios[@]}" -gt 0 ] || { echo "no scenarios selected (STACK_SCENARIOS=${STACK_SCENARIOS:-default})"; exit 1; }

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
dead="$(dead_containers)"
if [ -n "$dead" ]; then
  printf 'containers down before the scenarios:\n%s\n' "$dead"
  summary+=("FAIL     containers down before the scenarios")
  failed=1
fi
for path in "${scenarios[@]}"; do
  name="$(basename "$path" .sh)"
  printf '\n==== %s ====\n' "$name"
  stack_compose exec -T -e STACK_DVR_WINDOW_SECONDS="${STACK_DVR_WINDOW_SECONDS:-120}" stack-runner bash "$path"
  rc=$?
  case "$rc" in
    0) summary+=("PASS     $name") ;;
    2) summary+=("BLOCKED  $name"); blocked=1 ;;
    *) summary+=("FAIL     $name (exit $rc)"); failed=1 ;;
  esac
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

printf '\n==== stack slot %s summary ====\n' "$STACK_SLOT"
printf '%s\n' "${summary[@]}"
[ "$failed" -ne 0 ] && exit 1
[ "$blocked" -ne 0 ] && exit 2
exit 0
