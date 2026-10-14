#!/bin/sh
# yugabyte-apply-baseline.sh <ysqlsh> <sql-file> [ysqlsh connection option ...]
#
# Applies a baseline file with ysqlsh, one statement at a time in autocommit, stopping at the first failing
# statement. A statement that fails with SQLSTATE 40001 or 40P01, or with a read restart whatever SQLSTATE the engine
# reports for it, makes the whole file run again, up to
# BASELINE_APPLY_ATTEMPTS (default 6) runs, BASELINE_APPLY_RETRY_DELAY seconds (default 5) apart; any other failure
# ends the apply with ysqlsh's exit status.
#
# YSQL cannot retry a DDL statement internally, so a read restart inside one fails the statement: ADD CONSTRAINT and
# other DDL read pg_yb_catalog_version, which every database's DDL and ANALYZE (including the auto-analyze service)
# write, so a catalog version raised anywhere on the universe during that read fails it with "Restart read required"
# (40001). Running the file again is safe because every baseline is idempotent (TestYugabyteServiceBaselineReapply-
# AndCompletion), and the tables an interrupted run created are reachable only by the superuser applying it: the
# owner and runtime roles are granted access after the apply.
set -eu

ysqlsh=$1
file=$2
shift 2
attempts=${BASELINE_APPLY_ATTEMPTS:-6}
delay=${BASELINE_APPLY_RETRY_DELAY:-5}

attempt=1
while :; do
  # VERBOSITY=verbose prints the SQLSTATE of the failing statement ("ERROR:  40001: Restart read required").
  if output=$("$ysqlsh" -X -q -v ON_ERROR_STOP=1 -v VERBOSITY=verbose "$@" -f "$file" 2>&1); then
    [ -z "$output" ] || printf '%s\n' "$output"
    exit 0
  else
    status=$?
  fi
  printf '%s\n' "$output" >&2
  if [ "$attempt" -ge "$attempts" ] || ! printf '%s\n' "$output" | grep -Eq 'ERROR: +((40001|40P01): |Restart read required)'; then
    exit "$status"
  fi
  attempt=$((attempt + 1))
  echo "baseline $file failed with a retryable SQLSTATE; applying it again in ${delay}s (attempt $attempt of $attempts)" >&2
  sleep "$delay"
done
