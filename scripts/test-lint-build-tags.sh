#!/usr/bin/env bash
set -euo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin"
cat >"$work/bin/golangci-lint" <<'LINTER'
#!/usr/bin/env bash
printf '%s\n' "$@" >"$LINT_ARGS_FILE"
LINTER
chmod +x "$work/bin/golangci-lint"

for target in lint-go lint-all lint-fix lint-report; do
  for tags in nomsgpack custom,another ''; do
    args="$work/args"
    PATH="$work/bin:$PATH" LINT_ARGS_FILE="$args" \
      "${MAKE:-make}" -C "$repo" --no-print-directory "$target" \
      GO_SERVICES=./api_network_observer GO_BUILD_TAGS="$tags" REPORTS_DIR="$work/reports" >"$work/output" 2>&1 || {
        cat "$work/output" >&2
        exit 1
      }
    found=0
    while IFS= read -r arg; do
      case "$arg" in
        --build-tags=*)
          if [[ -z "$tags" || "$arg" != "--build-tags=$tags" ]]; then
            echo "$target used different build tags: $arg (expected '$tags')" >&2
            exit 1
          fi
          found=$((found + 1))
          ;;
      esac
    done <"$args"
    if [[ -n "$tags" && "$found" != 1 ]]; then
      echo "$target did not use the build/test tags '$tags'" >&2
      exit 1
    fi
  done
done
echo 'All lint entry points use the build/test tags.'
