#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

if [[ $# -eq 0 ]]; then
  echo 'No staged Go files to lint.'
  exit 0
fi

pairs=()

append_unique() {
  local candidate=$1
  local existing

  for existing in "${pairs[@]:-}"; do
    if [[ "$existing" == "$candidate" ]]; then
      return
    fi
  done

  pairs+=("$candidate")
}

for file in "$@"; do
  file=${file#./}
  [[ "$file" == *.go ]] || continue

  package_dir=$(dirname "$file")
  module_dir=$package_dir
  while [[ "$module_dir" != "." && ! -f "$module_dir/go.mod" ]]; do
    module_dir=$(dirname "$module_dir")
  done

  if [[ ! -f "$module_dir/go.mod" ]]; then
    echo "Unable to find a Go module for $file" >&2
    exit 1
  fi

  # A deletion can remove the final Go file from a package, leaving nothing to lint.
  remaining_go_file=$(find "$package_dir" -maxdepth 1 -type f -name '*.go' -print -quit 2>/dev/null)
  if [[ -z "$remaining_go_file" ]]; then
    continue
  fi

  if [[ "$package_dir" == "$module_dir" ]]; then
    package_pattern=.
  else
    package_pattern="./${package_dir#"$module_dir"/}"
  fi

  append_unique "$module_dir|$package_pattern"
done

if [[ ${#pairs[@]} -eq 0 ]]; then
  echo 'No remaining staged Go packages to lint.'
  exit 0
fi

modules=()
for pair in "${pairs[@]}"; do
  module_dir=${pair%%|*}
  seen=false
  for existing in "${modules[@]:-}"; do
    if [[ "$existing" == "$module_dir" ]]; then
      seen=true
      break
    fi
  done
  if [[ "$seen" == false ]]; then
    modules+=("$module_dir")
  fi
done

for module_dir in "${modules[@]}"; do
  packages=()
  for pair in "${pairs[@]}"; do
    if [[ "${pair%%|*}" == "$module_dir" ]]; then
      packages+=("${pair#*|}")
    fi
  done

  printf 'Linting staged Go changes in %s:' "$module_dir"
  printf ' %s' "${packages[@]}"
  printf '\n'
  (
    cd "$module_dir"
    golangci-lint run --timeout=5m --new-from-rev=HEAD "${packages[@]}"
  )
done
