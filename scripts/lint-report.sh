#!/bin/bash
# Run golangci-lint on all Go modules and save output to reports/lint-report.txt

set -e

REPORT_DIR="${REPORT_DIR:-reports}"
REPORT_FILE="$REPORT_DIR/lint-report.txt"

mkdir -p "$REPORT_DIR"
> "$REPORT_FILE"

echo "Running golangci-lint on all Go modules..."

for dir in ${GO_SERVICES:-$(git ls-files -- 'go.mod' '*/go.mod' | xargs -n1 dirname | sort -u | sed 's|^|./|')}; do
    module_name=$(basename "$dir")
    echo "==> $module_name"
    echo "=== MODULE: $module_name ===" >> "$REPORT_FILE"
    lint_args=(run --timeout=5m)
    if [[ -n ${GO_BUILD_TAGS-nomsgpack} ]]; then
        lint_args+=("--build-tags=${GO_BUILD_TAGS-nomsgpack}")
    fi
    (cd "$dir" && golangci-lint "${lint_args[@]}" ./... 2>&1) >> "$REPORT_FILE" || true
    echo "" >> "$REPORT_FILE"
done

echo ""
echo "Report saved to: $REPORT_FILE"
echo "Run 'scripts/lint-analyze.sh' to see summary"
