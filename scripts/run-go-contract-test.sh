#!/usr/bin/env bash

set -euo pipefail

if [[ $# -lt 3 ]]; then
	echo "usage: $0 <module-directory> <coverage-name> <go-test-arguments...>" >&2
	exit 2
fi

module_dir=$1
coverage_name=$2
shift 2

# On the suite-owned Yugabyte engine (run-yugabyte-contract-fixture.sh) a test process leaves databases behind when it
# exits: the baselines it held for reuse and the databases it cached across tests. Every tablet they keep counts against
# the engine's tablet replica limit for the rest of the lane, so the databases that appeared while this go test ran are
# dropped when it exits, whatever its outcome. The fixture runs one go test at a time on its engine, so each of them
# belongs to this process.
shared_yugabyte=${FRAMEWORKS_YUGABYTE_TEST_CONTAINER:-}
shared_yugabyte_before=""

shared_yugabyte_sql() {
	docker exec -e PGCONNECT_TIMEOUT=10 "$shared_yugabyte" \
		ysqlsh -h "$shared_yugabyte" -U yugabyte -d yugabyte -X -v ON_ERROR_STOP=1 -tA -c "$1"
}

shared_yugabyte_databases() {
	shared_yugabyte_sql "SELECT format('%I', datname) FROM pg_database WHERE NOT datistemplate AND datname NOT IN ('yugabyte', 'postgres', 'system_platform')"
}

release_shared_yugabyte_databases() {
	local status=$? after database literal attempt failed=0
	if ! after=$(shared_yugabyte_databases); then
		echo "ERROR: could not list the databases on shared Yugabyte engine $shared_yugabyte to release them" >&2
		exit $(( status == 0 ? 1 : status ))
	fi
	while IFS= read -r database; do
		[[ -n "$database" ]] || continue
		literal=${database//\'/\'\'}
		for (( attempt = 1; ; attempt++ )); do
			shared_yugabyte_sql "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE format('%I', datname) = '$literal' AND pid <> pg_backend_pid()" >/dev/null || true
			if shared_yugabyte_sql "DROP DATABASE IF EXISTS $database" >/dev/null; then
				break
			fi
			if (( attempt == 5 )); then
				echo "ERROR: could not drop database $database from shared Yugabyte engine $shared_yugabyte" >&2
				failed=1
				break
			fi
			sleep 2
		done
	done < <(LC_ALL=C comm -13 <(printf '%s\n' "$shared_yugabyte_before" | LC_ALL=C sort) <(printf '%s\n' "$after" | LC_ALL=C sort))
	if (( failed )) && (( status == 0 )); then
		exit 1
	fi
	exit "$status"
}

if [[ -n "$shared_yugabyte" ]]; then
	shared_yugabyte_before=$(shared_yugabyte_databases)
	trap release_shared_yugabyte_databases EXIT
fi

if [[ -z "${CONTRACT_COVERAGE_DIR:-}" ]]; then
	cd "$module_dir"
	go test "$@"
	exit 0
fi

case "$coverage_name" in
	/*|*..*)
		echo "coverage name must be a relative path without '..': $coverage_name" >&2
		exit 2
		;;
esac

# A contract that runs again in another engine shape (CONTRACT_COVERAGE_VARIANT) writes its own profile beside the first.
if [[ -n "${CONTRACT_COVERAGE_VARIANT:-}" ]]; then
	if [[ ! "$CONTRACT_COVERAGE_VARIANT" =~ ^[a-z0-9-]+$ ]]; then
		echo "coverage variant must be lowercase letters, digits, and dashes: $CONTRACT_COVERAGE_VARIANT" >&2
		exit 2
	fi
	coverage_name="${coverage_name}-${CONTRACT_COVERAGE_VARIANT}"
fi

coverage_file="${CONTRACT_COVERAGE_DIR%/}/${coverage_name}.out"
mkdir -p "$(dirname "$coverage_file")"
coverage_file=$(cd "$(dirname "$coverage_file")" && pwd)/$(basename "$coverage_file")

cd "$module_dir"
go test "$@" -coverpkg=./... -covermode=atomic -coverprofile="$coverage_file"
if ! awk '
NR == 1 { if ($0 != "mode: atomic") exit 1; next }
NF != 3 || $1 !~ /:[0-9]+[.][0-9]+,[0-9]+[.][0-9]+$/ || $2 !~ /^[0-9]+$/ || $3 !~ /^[0-9]+$/ { exit 1 }
{ records++ }
END { if (!records) exit 1 }
' "$coverage_file"; then
	echo "ERROR: contract coverage is missing or empty: $coverage_file" >&2
	exit 1
fi
