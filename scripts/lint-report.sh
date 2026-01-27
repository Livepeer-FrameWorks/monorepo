#!/bin/bash
# Run golangci-lint on all Go modules and save output to reports/lint-report.txt

set -e

REPORT_DIR="reports"
REPORT_FILE="$REPORT_DIR/lint-report.txt"

mkdir -p "$REPORT_DIR"
> "$REPORT_FILE"

echo "Running golangci-lint on all Go modules..."

for dir in $(find . -name "go.mod" -exec dirname {} \; | sort); do
    module_name=$(basename "$dir")
    echo "==> $module_name"
    echo "=== MODULE: $module_name ===" >> "$REPORT_FILE"
    (cd "$dir" && golangci-lint run --timeout=5m ./... 2>&1) >> "$REPORT_FILE" || true
    echo "" >> "$REPORT_FILE"
done

echo ""
echo "Report saved to: $REPORT_FILE"
echo "Run 'scripts/lint-analyze.sh' to see summary"
