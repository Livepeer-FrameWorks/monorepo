#!/usr/bin/env bash
# Runs the verify-prepush stages concurrently, each in its own detached worktree of HEAD, the way CI runs each job
# in its own checkout. A stage's in-place generators and their git diff checks then only see that stage's files, and
# the checkout this runs from is never written to.
#
# usage: verify-prepush.sh JOBS STAGE[+SETUP]...
#
# Up to JOBS stages run at once, started in the order given. SETUP is the Make target that installs what the stage's
# CI job installs before its steps (the frozen pnpm install, the Ansible collections). Stages with the same SETUP run
# it one at a time, because pnpm install runs lefthook install, which writes the hooks every worktree shares, and
# the installs then share the pnpm store without contending for it. Every stage runs to the
# end whatever the others do; the summary lists each stage's result and duration, and the exit status is non-zero
# when any stage failed.
#
# Worktrees live in <checkout>.prepush beside the checkout: a fixed path, so Go's build and lint caches, which key
# on the package directory, carry over between runs, and on the checkout's filesystem, so pnpm links packages from
# its store instead of copying them.
set -uo pipefail

self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
root="${repo}.prepush"
make="${MAKE:-make}"

format_duration() {
  local seconds=$1
  printf '%dm%02ds' $((seconds / 60)) $((seconds % 60))
}

# The body of one stage, run inside its worktree. The stage's Make runs serially and stops at its first failing
# step, as the CI job's steps do, whatever -j or -k the invoking Make passed down.
stage_body() {
  local worktree=$1 stage=$2 setup=$3 lock=$4 status=0
  if [[ -n "$setup" ]]; then
    until mkdir "$lock" 2>/dev/null; do sleep 1; done
    "$make" -C "$worktree" -j1 -S --no-print-directory "$setup" || status=$?
    rmdir "$lock"
    if (( status != 0 )); then
      echo "Setup $setup failed with status $status"
      return "$status"
    fi
  fi
  "$make" -C "$worktree" -j1 -S --no-print-directory "$stage"
}

run_stage() {
  local spec=$1 stage setup="" started status
  stage=${spec%%+*}
  [[ "$spec" == *+* ]] && setup=${spec#*+}
  started=$(date +%s)
  "$repo/scripts/run-labelled.sh" "$stage" "$self" stage-body "$root/$stage" "$stage" "$setup" "$root/setup-$setup.lock" \
    | tee "$root/logs/$stage.log"
  status=${PIPESTATUS[0]}
  printf '%s %s\n' "$status" "$(($(date +%s) - started))" >"$root/results/$stage"
  # xargs stops starting stages when one exits 255, so every failure exits 1.
  (( status == 0 )) || exit 1
}

case "${1:-}" in
  stage-body) shift; stage_body "$@"; exit ;;
  run-stage) shift; run_stage "$@"; exit ;;
esac

if (( $# < 2 )); then
  echo "usage: $0 JOBS STAGE[+SETUP]..." >&2
  exit 2
fi
jobs=$1
shift
stages=("$@")

dirty="$(git -C "$repo" status --porcelain --untracked-files=no)"
if [[ -n "$dirty" ]]; then
  echo "ERROR: verify-prepush verifies the commit HEAD, and these tracked files differ from it; commit them first:" >&2
  echo "$dirty" >&2
  exit 1
fi
commit="$(git -C "$repo" rev-parse HEAD)"

remove_worktrees() {
  local path
  git -C "$repo" worktree list --porcelain | sed -n 's/^worktree //p' | while IFS= read -r path; do
    case "$path" in
      "$root"/*) git -C "$repo" worktree remove --force "$path" >/dev/null 2>&1 || true ;;
    esac
  done
  rm -rf "$root" 2>/dev/null
  git -C "$repo" worktree prune
  if [[ -e "$root" ]]; then
    echo "WARNING: could not remove $root completely (files owned by another user?); remove it before the next run" >&2
  fi
}

if ! mkdir "$root" 2>/dev/null; then
  owner="$(cat "$root/pid" 2>/dev/null || true)"
  if [[ -n "$owner" ]] && kill -0 "$owner" 2>/dev/null; then
    echo "ERROR: verify-prepush (pid $owner) is already running from $repo" >&2
    exit 1
  fi
  echo "Removing the worktrees an interrupted verify-prepush left in $root"
  remove_worktrees
  mkdir "$root" || exit 1
fi
echo "$$" >"$root/pid"
mkdir "$root/logs" "$root/results"

runner_pid=""
cleanup() {
  remove_worktrees
}
trap cleanup EXIT

# The stages run in a process group of their own, so an interrupt reaches every stage and its Docker and test
# processes, and the worktrees are removed only after they have stopped.
forward() {
  trap '' INT TERM
  echo "verify-prepush interrupted; stopping every stage..." >&2
  [[ -n "$runner_pid" ]] && kill -s "$1" -- "-$runner_pid" 2>/dev/null
  wait "$runner_pid" 2>/dev/null
  exit "$2"
}
trap 'forward INT 130' INT
trap 'forward TERM 143' TERM

echo "verify-prepush: commit $commit, ${#stages[@]} stages, $jobs at a time, worktrees in $root"
for spec in "${stages[@]}"; do
  stage=${spec%%+*}
  git -C "$repo" worktree add --detach --quiet "$root/$stage" "$commit" || exit 1
done

printf '%s\n' "${stages[@]}" >"$root/stages"
started=$(date +%s)
set -m
xargs -n 1 -P "$jobs" "$self" run-stage <"$root/stages" &
runner_pid=$!
set +m
wait "$runner_pid"
elapsed=$(($(date +%s) - started))

failed=0
for spec in "${stages[@]}"; do
  stage=${spec%%+*}
  if [[ -f "$root/results/$stage" ]] && read -r status _ <"$root/results/$stage" && (( status != 0 )); then
    echo
    echo "=== $stage failed (status $status); last 40 lines ==="
    tail -n 40 "$root/logs/$stage.log"
  fi
done

echo
echo "verify-prepush summary for $commit (wall time $(format_duration "$elapsed"), $jobs stages at a time):"
for spec in "${stages[@]}"; do
  stage=${spec%%+*}
  if [[ -f "$root/results/$stage" ]]; then
    read -r status seconds <"$root/results/$stage"
    if (( status == 0 )); then result=PASS; else result=FAIL; failed=1; fi
    printf '  %-4s  %-34s %s\n' "$result" "$stage" "$(format_duration "$seconds")"
  else
    printf '  %-4s  %-34s\n' "SKIP" "$stage"
    failed=1
  fi
done
if (( failed )); then
  echo "verify-prepush FAILED"
  exit 1
fi
echo "verify-prepush passed"
