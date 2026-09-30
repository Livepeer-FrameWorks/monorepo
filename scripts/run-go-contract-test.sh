#!/usr/bin/env bash

set -euo pipefail

if [[ $# -lt 3 ]]; then
	echo "usage: $0 <module-directory> <coverage-name> <go-test-arguments...>" >&2
	exit 2
fi

module_dir=$1
coverage_name=$2
shift 2

if [[ -z "${CONTRACT_COVERAGE_DIR:-}" ]]; then
	cd "$module_dir"
	exec go test "$@"
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
