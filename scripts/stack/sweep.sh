#!/usr/bin/env bash
# Post-run invariant sweep: generic properties of every entity the run created
# (streams, VODs, DVR recordings and their chapter VODs, clips), whichever
# scenario created it, plus host health. The checks are in sweep.py; this
# wrapper supplies the endpoint map lib.sh resolves (the stack runner's, or the
# staging adapter's) and, on staging, the operator CLI's cluster doctor.
#
#   sweep.sh <since epoch> [scenario windows file] [restart baseline file]
#
#   STACK_SWEEP_WAIT=N       seconds eventual states may take to settle (default 240)
#   STACK_SWEEP_THUMB_MIN=N  ready artifacts at least this long need thumbnails (default 10)
#
# Exit status: 0 all held; 1 an invariant failed; 2 a source was unreadable.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
since=${1:?usage: sweep.sh <since epoch> [windows file] [restart baseline file]}
windows=${2:-}
baseline=${3:-}
STACK_SCENARIO=sweep
# shellcheck source=scripts/stack/lib.sh
. "$here/lib.sh"
STACK_SCENARIO=sweep

export BRIDGE_URL STACK_API_TOKEN STACK_TENANT_ID CLICKHOUSE_URL CHANDLER_URL PG_HOST PG_PORT \
  FOGHORN_A_DB FOGHORN_B_DB STACK_TARGET="${STACK_TARGET:-stack}"
export POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-}" COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-}"

rc=0
# Doctor FAIL lines that are expected on the target, as "<regex>\t<reason>".
# Keep each entry to one check and one cause.
doctor_allow="$STACK_STATE_DIR/sweep-doctor-allow.tsv"
cat >"$doctor_allow" <<'ALLOW'
ALLOW
if [ "$STACK_TARGET" = staging ]; then
  printf '\n-- sweep[doctor]\n'
  if [ -x "${STACK_REPO:-}/bin/cli" ] && [ -f "${STACK_CLI_MANIFEST:-}" ] && [ -n "${STACK_CLI_CONTEXT:-}" ]; then
    "$STACK_REPO/bin/cli" --context "$STACK_CLI_CONTEXT" cluster doctor --deep --manifest "$STACK_CLI_MANIFEST" \
      >"$STACK_STATE_DIR/sweep-doctor.log" 2>&1
    if python3 "$here/sweep.py" doctor "$STACK_STATE_DIR/sweep-doctor.log" "$doctor_allow" |
      sed 's/^FAIL  /FAIL  sweep[doctor]: /; s/^ALLOW /ALLOW sweep[doctor]: /'; [ "${PIPESTATUS[0]}" = 0 ]; then
      echo "PASS  sweep[doctor]: no unexplained FAIL ($STACK_STATE_DIR/sweep-doctor.log)"
    else
      rc=1
    fi
  else
    echo 'BLOCKED  sweep[doctor]: staging needs bin/cli (make build-bin-cli), STACK_CLI_MANIFEST and STACK_CLI_CONTEXT'
    rc=2
  fi
else
  # Wildcard listeners an edge container has in the stack and not in
  # production, as "<proto>/<port>": 8082 is the stack edge proxy's public HTTP
  # port (80/443 in production), and Helmsman's 18007 binds every interface
  # because scenario 32 posts Mist webhooks to it from the runner (production
  # binds it to 127.0.0.1 through HELMSMAN_BIND_ADDR).
  export STACK_SWEEP_EDGE_INTERNAL_PORTS="${STACK_SWEEP_EDGE_INTERNAL_PORTS-tcp/8082 tcp/18007}"
  # The compose stack has no SSH hosts for the doctor to read; scenario 28 runs
  # the CLI doctor against the stack's Foghorns, and sweep.py reads the
  # containers' restarts, core dumps and listeners directly.
  printf '\nNOTE  sweep[doctor]: the compose stack has no doctor hosts; container health is checked by sweep[host]\n'
fi

printf '\n-- sweep[analytics, api, artifacts, dvr, host]\n'
python3 "$here/sweep.py" run --since "$since" ${windows:+--windows "$windows"} ${baseline:+--baseline "$baseline"} \
  --allow "$STACK_STATE_DIR/sweep-allow.tsv" --ledger "$STACK_STATE_DIR/sweep-ledger.jsonl" \
  --wait "${STACK_SWEEP_WAIT:-240}" --thumb-min "${STACK_SWEEP_THUMB_MIN:-10}"
run_rc=$?
if [ "$run_rc" = 1 ] || [ "$rc" = 1 ]; then exit 1; fi
if [ "$run_rc" != 0 ] || [ "$rc" != 0 ]; then exit 2; fi
exit 0
