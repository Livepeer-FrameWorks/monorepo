#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
files=()
while IFS= read -r -d '' file; do
  [[ -f "$file" ]] && files+=("$file")
done < <(git ls-files --cached --others --exclude-standard -z -- '*.go')

if [[ ${#files[@]} -eq 0 ]]; then
  exit 0
fi

unformatted=$(gofmt -l "${files[@]}")
if [[ -n "$unformatted" ]]; then
  printf 'Go files requiring gofmt:\n%s\n' "$unformatted" >&2
  exit 1
fi
echo 'All Go files are gofmt-clean.'
