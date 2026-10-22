#!/usr/bin/env bash
# Unit checks for scenario selection (scripts/stack/scenarios/list.sh): on
# STACK_TARGET=staging only "staging: safe" scenarios are selected, and naming
# an unsafe or undeclared one is refused; the stack target is unaffected.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=$(cd "$(mktemp -d "${TMPDIR:-/tmp}/fw-stack-list-test.XXXXXX")" && pwd)
trap 'rm -rf "$work"' EXIT
failures=0
expect() { # expect <description> <command...>
  local what=$1
  shift
  if "$@"; then printf 'ok    %s\n' "$what"; else printf 'FAIL  %s\n' "$what"; failures=$((failures + 1)); fi
}

mkdir -p "$work/scenarios"
cp "$here/scenarios/list.sh" "$work/scenarios/list.sh"
fixture() { # fixture <file> <stack-scenario tag> [staging declaration]
  {
    echo '#!/usr/bin/env bash'
    echo "# stack-scenario: $2"
    [ -n "${3:-}" ] && echo "# staging: $3"
  } >"$work/scenarios/$1"
}
fixture 01-safe.sh default safe
fixture 02-restarts.sh default 'unsafe, restarts a replica'
fixture 03-undeclared.sh default
fixture 04-manual-safe.sh manual safe

list() { # list <target> <selection>: names on stdout, refusals in $work/err
  STACK_TARGET=$1 STACK_SCENARIOS=$2 bash "$work/scenarios/list.sh" 2>"$work/err" | xargs -n1 basename 2>/dev/null | tr '\n' ' '
  return "${PIPESTATUS[0]}"
}
selects() { [ "$(list "$1" "$2")" = "$3" ]; }
refuses() { # refuses <selection> <file named in the refusal>
  ! list staging "$1" >/dev/null && grep -qF "refused on staging: $2" "$work/err"
}

expect "stack default selects every default scenario" selects stack default "01-safe.sh 02-restarts.sh 03-undeclared.sh "
expect "staging default selects only safe scenarios" selects staging default "01-safe.sh "
expect "staging manual selects only safe scenarios" selects staging manual "04-manual-safe.sh "
expect "staging all selects only safe scenarios" selects staging all "01-safe.sh 04-manual-safe.sh "
expect "staging refuses a named unsafe scenario" refuses 01,02 02-restarts.sh
expect "staging refuses a named undeclared scenario" refuses 03 03-undeclared.sh
expect "stack runs a named unsafe scenario" selects stack 02 "02-restarts.sh "

every_declared() {
  local f ok=0
  for f in "$here"/scenarios/[0-9][0-9]-*.sh; do
    if ! grep -Eq '^# staging: (safe|unsafe, .+)$' "$f"; then
      echo "      $(basename "$f") has no staging declaration"
      ok=1
    fi
  done
  return "$ok"
}
expect "every scenario declares whether it is safe on staging" every_declared

if [ "$failures" -gt 0 ]; then
  echo "scenario list checks: $failures failed"
  exit 1
fi
echo "scenario list checks: ok"
