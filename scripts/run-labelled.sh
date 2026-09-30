#!/usr/bin/env bash
# Runs a command with every line of its output prefixed by a label, so concurrent jobs stay readable as they stream,
# and exits with the command's status.
set -uo pipefail

if (( $# < 2 )); then
  echo "usage: $0 label command [args...]" >&2
  exit 2
fi

label=$1
shift
"$@" 2>&1 | awk -v prefix="[$label] " '{ print prefix $0; fflush() }'
exit "${PIPESTATUS[0]}"
