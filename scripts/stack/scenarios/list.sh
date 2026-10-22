#!/usr/bin/env bash
# Prints the scenario scripts selected by STACK_SCENARIOS, in order:
#   (unset) | default  every "stack-scenario: default" scenario
#   manual             every "stack-scenario: manual" scenario
#   all                both
#   01,04,…            those numbers, whatever their tag
#
# Every scenario declares "staging: safe" or "staging: unsafe, <what it does>".
# With STACK_TARGET=staging only safe scenarios are selected: a scenario that
# restarts, stops, pauses, fault-injects or reconfigures anything never runs on
# a shared deployment. Naming an unsafe or undeclared one exits 1.
set -euo pipefail
dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
want=${STACK_SCENARIOS:-default}
staging=0
[ "${STACK_TARGET:-stack}" = staging ] && staging=1
refused=0
for f in "$dir"/[0-9][0-9]-*.sh; do
  tag=$(sed -n 's/^# stack-scenario: //p' "$f" | head -1)
  safety=$(sed -n 's/^# staging: //p' "$f" | head -1)
  num=$(basename "$f" | cut -c1-2)
  selected=0
  named=0
  case "$want" in
    default | manual) [ "$tag" = "$want" ] && selected=1 ;;
    all) selected=1 ;;
    *) case ",$want," in *",$num,"*) selected=1 named=1 ;; esac ;;
  esac
  [ "$selected" = 1 ] || continue
  if [ "$staging" = 1 ] && [ "$safety" != safe ]; then
    if [ "$named" = 1 ]; then
      echo "refused on staging: $(basename "$f") (${safety:-no staging declaration})" >&2
      refused=1
    fi
    continue
  fi
  echo "$f"
done
exit "$refused"
