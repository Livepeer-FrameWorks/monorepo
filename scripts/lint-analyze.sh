#!/bin/bash
# Analyze lint report and show summary

REPORT_FILE="reports/lint-report.txt"

if [ ! -f "$REPORT_FILE" ]; then
    echo "No report found. Run 'scripts/lint-report.sh' first."
    exit 1
fi

echo "=== LINT REPORT SUMMARY ==="
echo ""

# Only match actual lint error lines: filename.go:line:col: message (linter)
# These lines start with a path containing .go:
LINT_LINES=$(grep -E '^[a-zA-Z0-9_./-]+\.go:[0-9]+:[0-9]+:.*\([a-zA-Z]+\)$' "$REPORT_FILE")

# Total count
total=$(echo "$LINT_LINES" | wc -l | tr -d ' ')
echo "Total violations: $total"
echo ""

# By linter
echo "--- By Linter ---"
echo "$LINT_LINES" | grep -oE '\([a-zA-Z]+\)$' | tr -d '()' | sort | uniq -c | sort -rn
echo ""

# By module
echo "--- By Module ---"
for module in $(grep "^=== MODULE:" "$REPORT_FILE" | sed 's/=== MODULE: \(.*\) ===/\1/'); do
    module_lines=$(sed -n "/^=== MODULE: $module ===/,/^=== MODULE:/p" "$REPORT_FILE")
    count=$(echo "$module_lines" | grep -E '^[a-zA-Z0-9_./-]+\.go:[0-9]+:[0-9]+:.*\([a-zA-Z]+\)$' | wc -l | tr -d ' ')
    if [ "$count" -gt 0 ]; then
        printf "%-25s %s\n" "$module" "$count"
    fi
done | sort -t' ' -k2 -rn
echo ""

# Top 10 files with most issues
echo "--- Top 10 Files ---"
echo "$LINT_LINES" | cut -d: -f1 | sort | uniq -c | sort -rn | head -10
