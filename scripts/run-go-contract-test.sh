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

coverage_file="${CONTRACT_COVERAGE_DIR%/}/${coverage_name}.out"
mkdir -p "$(dirname "$coverage_file")"
coverage_file=$(cd "$(dirname "$coverage_file")" && pwd)/$(basename "$coverage_file")

cd "$module_dir"
exec go test "$@" -coverpkg=./... -covermode=atomic -coverprofile="$coverage_file"
