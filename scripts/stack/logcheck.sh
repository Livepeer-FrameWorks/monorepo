#!/usr/bin/env bash
# Fails when any container of the slot logged a defect signature during the
# run: every staging defect that reached the logs before anyone noticed it
# (22P02 persistence errors, Mist core dumps, "completed" then "exhausted",
# refusals of an unmappable state page) matches one of these.
#
#   logcheck.sh [since]   since: docker logs --since value (default: whole run)
#
# Mist FAIL lines are defects unless allowlisted below with the reason.
set -uo pipefail

# shellcheck source=scripts/stack/common.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
cd "$stack_repo_root" || exit 1

since="${1:-}"
since_args=()
[ -n "$since" ] && since_args=(--since "$since")

# "Address already in use": a connector replacement raced the listener it
# replaces, which leaves the node without that protocol.
signatures='core dumped|dumped core|SIGSEGV|SIGABRT|terminate called|^panic:|goroutine [0-9]+ \[running\]|SQLSTATE|exhausted retries|Could not map process-controlled|unrecoverable error|Address already in use'
# Mist FAIL| lines that are expected in the stack, with why:
#   the Livepeer fallback scenarios kill the gateway on purpose, which surfaces
#   only as connection-level failures (a refused or reset connection, and the
#   process shutting down its sink); an HTTP status from the gateway, such as
#   the staging 403, is never allowlisted;
#   a publisher ending its push closes the connection under Mist.
mist_fail_allow='Could not connect to|Connection refused|Connection reset|Broken pipe|Sink thread failed|Logging unclean exit reason: set inactive'
# Signature hits that are expected, with why: the same gateway kill ends the
# Livepeer process with its designed give-up exit, which is what hands the
# stream to the CPU fallback. Scenarios 02 and 03 assert that fallback happens
# only while the gateway is down, so an unexpected give-up still fails there.
# shellcheck disable=SC2016 # the backticks are literal Mist log text
signature_allow='Process `Livepeer` \(PID [0-9]+\) exited with unrecoverable error \(code 2: too many upload failures\)'

out="$(mktemp)"
trap 'rm -f "$out"' EXIT
found=0
while IFS= read -r svc; do
  [ -n "$svc" ] || continue
  stack_compose logs --no-color --no-log-prefix ${since_args[@]+"${since_args[@]}"} "$svc" 2>/dev/null >"$out" || continue
  hits="$(grep -E "$signatures" "$out" | grep -vE "$signature_allow" || true)"
  mist_fails="$(grep -E 'FAIL\|' "$out" | grep -vE "$mist_fail_allow" || true)"
  if [ -n "$hits" ] || [ -n "$mist_fails" ]; then
    found=1
    printf '\n== %s\n' "$svc"
    printf '%s\n' "$hits" "$mist_fails" | sed '/^$/d' | sort | uniq -c | sort -rn | head -20
  fi
done < <(stack_compose ps --services 2>/dev/null)

if [ "$found" -ne 0 ]; then
  echo
  echo "logcheck: FAIL (defect signatures above)"
  exit 1
fi
echo "logcheck: PASS"
