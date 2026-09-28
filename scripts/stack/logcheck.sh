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
#   the staging 403, is not allowlisted here (see drop_designed_refusals for the
#   refusals that are by design);
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

# Two Livepeer HTTP refusals are designed outcomes, each allowlisted only for a stream the
# refusing side names in its own log; the same status for any other stream, such as the staging
# 403 refused while its session was open, still fails:
#   403: Foghorn refused the transcode because its ingest session had ended (session_ended, e.g.
#   node_lost while the edge was cut off; scenario 14 case B). The generation is over, the node is
#   told to stop it, and Mist hands the stream to local renditions.
#   503: the gateway had no orchestrator session for the stream (ErrNoOrchs: its orchestrator
#   declined under dynamic capacity management while the host was loaded). Mist gives up on the
#   gateway and Foghorn records the stream as degraded to local renditions.
stream_names_logged_by() { # stream_names_logged_by <service regex> <fixed text> <stream regex>
  stack_compose ps --services 2>/dev/null | grep -E "$1" | while IFS= read -r svc; do
    stack_compose logs --no-color --no-log-prefix ${since_args[@]+"${since_args[@]}"} "$svc" 2>/dev/null
  done | grep -F "$2" | grep -oE "$3" | sort -u
}
ended_session_streams="$(stream_names_logged_by '^foghorn' 'livepeer auth: refused a transcode of an ended ingest session' \
  '"stream":"live\+[A-Za-z0-9_-]+"' | cut -d'"' -f4)"
# The gateway's manifest ID is the stream name plus a per-process suffix after the last '-'.
no_orchestrator_streams="$(stream_names_logged_by '^livepeer-gateway' 'err="ErrNoOrchs"' \
  'manifestID=[A-Za-z0-9_+]+-[A-Za-z0-9]+' | sed -E 's/^manifestID=//; s/-[A-Za-z0-9]+$//' | sort -u)"
drop_designed_refusals() { # stdin: log lines; drops each allowlisted refusal of the streams above
  ENDED_SESSION_STREAMS="$ended_session_streams" NO_ORCHESTRATOR_STREAMS="$no_orchestrator_streams" awk '
    BEGIN {
      n403 = split(ENVIRON["ENDED_SESSION_STREAMS"], s403, "\n")
      n503 = split(ENVIRON["NO_ORCHESTRATOR_STREAMS"], s503, "\n")
    }
    function names(line, list, n,    i) {
      for (i = 1; i <= n; i++) if (list[i] != "" && index(line, list[i] "→") + index(line, list[i] " (") > 0) return 1
      return 0
    }
    /Livepeer upload fatal HTTP status 403 Forbidden/ && names($0, s403, n403) { next }
    /Livepeer upload fatal HTTP status 503 Service Unavailable/ && names($0, s503, n503) { next }
    { print }'
}

found=0
while IFS= read -r svc; do
  [ -n "$svc" ] || continue
  stack_compose logs --no-color --no-log-prefix ${since_args[@]+"${since_args[@]}"} "$svc" 2>/dev/null >"$out" || continue
  hits="$(grep -E "$signatures" "$out" | grep -vE "$signature_allow" | drop_designed_refusals || true)"
  mist_fails="$(grep -E 'FAIL\|' "$out" | grep -vE "$mist_fail_allow" | drop_designed_refusals || true)"
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
