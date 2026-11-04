#!/usr/bin/env bash
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/fw-managed-access-cleanup.XXXXXX")
trap 'rm -rf "$work"' EXIT
export STACK_ENDPOINTS_FILE=/dev/null STACK_STATE_DIR="$work/state"
. "$here/lib.sh"
failures=0
expect() {
  local what=$1
  shift
  if "$@"; then printf 'ok    %s\n' "$what"; else printf 'FAIL  %s\n' "$what"; failures=$((failures + 1)); fi
}

run_cleanup_case() (
  local mode=$1
  export WORK="$work/$mode" MODE="$mode" TENANT=cleanup-tenant
  export CELL_A_CLUSTER=cell-a CELL_B_CLUSTER=cell-b OPERATOR_TOKEN=fixture-token
  export FIXTURES=0 BILLING_FIXTURE=1 ACCESS_FIXTURES=1 SOURCE_PID=""
  export STACK_REPO="$WORK/repo"
  CLEANUP_STARTED=0
  FAIL=0
  mkdir -p "$STACK_REPO/bin"
  : >"$WORK/access-cell-a.before"
  : >"$WORK/access-cell-b.before"
  printf '0\n' >"$WORK/clock"
  cat >"$STACK_REPO/bin/cli" <<'CLI'
#!/usr/bin/env bash
if [ "$MODE" = revoke-error ]; then echo 'injected revoke error' >&2; exit 1; fi
: >"$WORK/pending"
printf 'fixture grant\n' >"$WORK/row-cell-a"
printf 'fixture grant\n' >"$WORK/row-cell-b"
CLI
  chmod +x "$STACK_REPO/bin/cli"
  date() { if [ "$*" = '+%s' ]; then cat "$WORK/clock"; else command date "$@"; fi; }
  sleep() { printf '%s\n' "$(($(cat "$WORK/clock") + 3))" >"$WORK/clock"; }
  pg_diagnostic() {
    case "$2" in
    *purser.media_authority_refresh_outbox*)
      local polls
      polls=$(($(cat "$WORK/polls" 2>/dev/null || echo 0) + 1))
      printf '%s\n' "$polls" >"$WORK/polls"
      if [ "$MODE" = query-error ]; then echo 'injected outbox query error' >&2; return 1; fi
      if [ "$MODE" = timeout ] || [ "$polls" -lt 2 ]; then echo 1; return 0; fi
      # The deferred subscription controller materializes platform-tier access
      # and only then acknowledges its durable outbox obligation.
      printf 'controller grant\n' >"$WORK/row-cell-a"
      rm -f "$WORK/pending"
      echo 0
      ;;
    *quartermaster.tenant_cluster_access*)
      local cluster=cell-a
      [[ "$2" == *"cluster_id='cell-b'"* ]] && cluster=cell-b
      # Without the controller acknowledgement barrier, the writer can land
      # between fixture DELETE and its verification SELECT.
      if [ -f "$WORK/pending" ] || [ "$MODE" = mismatch ]; then printf 'late controller grant\n' >"$WORK/row-$cluster"; fi
      cat "$WORK/row-$cluster" 2>/dev/null || true
      ;;
    esac
  }
  pg_fixture_diagnostic() {
    local cluster=cell-a
    [[ "$2" == *"cluster_id='cell-b'"* ]] && cluster=cell-b
    printf '%s\n' "$cluster" >>"$WORK/restores"
    if [ "$MODE" = restore-error ] && [ "$cluster" = cell-a ]; then echo 'injected restore SQL error' >&2; return 1; fi
    rm -f "$WORK/row-$cluster"
  }
  pg() { pg_diagnostic "$@"; }
  pg_fixture() { pg_fixture_diagnostic "$@"; }
  eval "$(sed -n '/^restore_cluster_access_fixture() {/,/^}$/p' "$here/scenarios/48-direct-replica-reopen.sh")"
  eval "$(sed -n '/^cleanup() {$/,/^}$/p' "$here/scenarios/48-direct-replica-reopen.sh")"
  cleanup >"$WORK/output" 2>&1
  cleanup >>"$WORK/output" 2>&1
  case "$mode" in
  delayed)
    [ "$FAIL" = 0 ] && [ "$(cat "$WORK/polls" 2>/dev/null)" = 2 ] &&
      [ ! -e "$WORK/row-cell-a" ] && [ ! -e "$WORK/row-cell-b" ] &&
      [ "$(wc -l <"$WORK/restores" | tr -d ' ')" = 2 ]
    ;;
  query-error)
    [ "$FAIL" -gt 0 ] && [ ! -e "$WORK/restores" ] &&
      [ "$(cat "$WORK/polls" 2>/dev/null)" = 1 ] && grep -q 'injected outbox query error' "$WORK/output"
    ;;
  timeout)
    [ "$FAIL" -gt 0 ] && [ ! -e "$WORK/restores" ] && grep -q 'timed out' "$WORK/output"
    ;;
  revoke-error)
    [ "$FAIL" -gt 0 ] && [ ! -e "$WORK/restores" ] && [ ! -e "$WORK/polls" ] && grep -q 'injected revoke error' "$WORK/output"
    ;;
  restore-error)
    [ "$FAIL" -gt 0 ] && [ "$(wc -l <"$WORK/restores" | tr -d ' ')" = 2 ] && grep -q 'injected restore SQL error' "$WORK/output"
    ;;
  mismatch)
    [ "$FAIL" -gt 0 ] && [ "$(wc -l <"$WORK/restores" | tr -d ' ')" = 2 ] &&
      grep -q 'snapshot mismatch' "$WORK/output" && grep -q 'late controller grant' "$WORK/access-cell-a.before.after"
    ;;
  esac
)
expect 'delayed billing controller finishes before snapshots restore exactly once' run_cleanup_case delayed
expect 'controller query errors surface immediately and prevent restoration' run_cleanup_case query-error
expect 'controller acknowledgement timeout prevents restoration' run_cleanup_case timeout
expect 'failed billing revoke prevents restoration' run_cleanup_case revoke-error
expect 'restore SQL errors remain visible and are not blindly retried' run_cleanup_case restore-error
expect 'restore mismatch preserves actual snapshot for diagnosis' run_cleanup_case mismatch
[ "$failures" = 0 ] || exit 1
