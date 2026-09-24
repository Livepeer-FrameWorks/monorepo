#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

if [[ $# -eq 0 ]]; then
  exit 0
fi

configs=()
files=()

for file in "$@"; do
  file=${file#./}
  [[ -f "$file" ]] || continue

  case "$file" in
    eslint.base.config.js|*/eslint.config.js)
      echo 'ESLint configuration changed; linting the full frontend workspace.'
      make lint-frontend
      exit 0
      ;;
  esac

  dir=$(dirname "$file")
  while [[ "$dir" != "." && ! -f "$dir/eslint.config.js" ]]; do
    dir=$(dirname "$dir")
  done
  [[ -f "$dir/eslint.config.js" ]] || continue

  files+=("$dir|${file#"$dir"/}")
  seen=false
  for config in "${configs[@]:-}"; do
    if [[ "$config" == "$dir" ]]; then
      seen=true
      break
    fi
  done
  if [[ "$seen" == false ]]; then
    configs+=("$dir")
  fi
done

for config in "${configs[@]}"; do
  staged=()
  for file in "${files[@]}"; do
    if [[ "${file%%|*}" == "$config" ]]; then
      staged+=("${file#*|}")
    fi
  done

  printf 'Linting %d staged frontend file(s) in %s\n' "${#staged[@]}" "$config"
  (
    cd "$config"
    ./node_modules/.bin/eslint --no-warn-ignored "${staged[@]}"
  )
done
