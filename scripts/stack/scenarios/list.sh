#!/usr/bin/env bash
# Prints the scenario scripts selected by STACK_SCENARIOS, in order:
#   (unset) | default  every "stack-scenario: default" scenario
#   manual             every "stack-scenario: manual" scenario
#   all                both
#   01,04,…            those numbers, whatever their tag
set -euo pipefail
dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
want=${STACK_SCENARIOS:-default}
for f in "$dir"/[0-9][0-9]-*.sh; do
  tag=$(sed -n 's/^# stack-scenario: //p' "$f" | head -1)
  num=$(basename "$f" | cut -c1-2)
  case "$want" in
    default | manual) [ "$tag" = "$want" ] && echo "$f" ;;
    all) echo "$f" ;;
    *) case ",$want," in *",$num,"*) echo "$f" ;; esac ;;
  esac
done
