#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 4 || $# -gt 5 ]]; then
  echo "usage: $0 <module-dir> <package> <selector> <family-regex> [source-build-tag]" >&2
  exit 2
fi

module_dir=$1
package=$2
selector=$3
family_regex=$4
source_build_tag=${5:-}
build_tags=${FRAMEWORKS_TEST_SELECTION_TAGS:-schema_verify}
actual_file=$(mktemp)
selected_file=$(mktemp)
selection_cache=${FRAMEWORKS_TEST_SELECTION_GOCACHE:-${TMPDIR:-/tmp}/frameworks-go-test-selection-cache}
mkdir -p "$selection_cache"
trap 'rm -f "$actual_file" "$selected_file"' EXIT

if [[ -n "$source_build_tag" ]]; then
  package_dir=$(
    cd "$module_dir"
    GOCACHE="$selection_cache" go list -tags "$build_tags" -f '{{.Dir}}' "$package"
  )
  test_files=$(
    cd "$module_dir"
    GOCACHE="$selection_cache" go list -tags "$build_tags" -f '{{range .TestGoFiles}}{{println .}}{{end}}{{range .XTestGoFiles}}{{println .}}{{end}}' "$package"
  )
  while IFS= read -r test_file; do
    [[ -n "$test_file" ]] || continue
    test_path="$package_dir/$test_file"
    if sed -n '1,8p' "$test_path" | grep -Eq "^//go:build .*${source_build_tag}"; then
      sed -n -E 's/^func (Test[[:alnum:]_]+)\(.*/\1/p' "$test_path"
    fi
  done <<<"$test_files" | grep -E "$family_regex" | sort -u >"$actual_file"
else
  (
    cd "$module_dir"
    GOCACHE="$selection_cache" go test -tags "$build_tags" -list "$family_regex" "$package"
  ) | awk '/^Test/ { print $1 }' | sort -u >"$actual_file"
fi

printf '%s\n' "$selector" | tr '|' '\n' | sed '/^$/d' | sort -u >"$selected_file"

if [[ ! -s "$actual_file" ]]; then
  echo "ERROR: $module_dir $package listed zero tests matching $family_regex" >&2
  exit 1
fi

missing=$(comm -23 "$selected_file" "$actual_file")
omitted=$(comm -13 "$selected_file" "$actual_file")
if [[ -n "$missing" || -n "$omitted" ]]; then
  echo "ERROR: explicit test selector drift for $module_dir $package" >&2
  if [[ -n "$missing" ]]; then
    echo "Named by the selector but absent:" >&2
    printf '%s\n' "$missing" >&2
  fi
  if [[ -n "$omitted" ]]; then
    echo "Present in the package but omitted by the selector:" >&2
    printf '%s\n' "$omitted" >&2
  fi
  exit 1
fi
