#!/bin/bash
# Analyze coverage report and show summary
#
# Input: reports/coverage-report.txt (from coverage-report.sh)
# Output: Summary to stdout showing:
#   - Overall coverage (if make coverage was run)
#   - Coverage by module
#   - Files with 0% coverage
#   - EZ targets (pure helper/util functions)

REPORT_FILE="reports/coverage-report.txt"

if [ ! -f "$REPORT_FILE" ]; then
    echo "No report found. Run 'scripts/coverage-report.sh' first."
    exit 1
fi

echo "=== COVERAGE REPORT SUMMARY ==="
echo ""

# Overall from make coverage (if available)
if [ -f "coverage/coverage.out" ]; then
    echo "--- Overall (from make coverage) ---"
    # Filter generated code for accurate total
    filtered=$(mktemp)
    grep -v '\.pb\.go:' "coverage/coverage.out" | \
        grep -v '_grpc\.pb\.go:' | \
        grep -v 'graph/generated/' | \
        grep -v 'graph/model/models_gen\.go:' > "$filtered" 2>/dev/null || true

    if [ -s "$filtered" ]; then
        go tool cover -func="$filtered" 2>/dev/null | tail -1 || echo "Unable to calculate"
    else
        echo "No non-generated code in coverage"
    fi
    rm -f "$filtered"
    echo ""
fi

# By module (extract total: line from each module section)
echo "--- By Module ---"
current_module=""
while IFS= read -r line; do
    if [[ "$line" =~ ^"=== MODULE: "(.*)" ===" ]]; then
        current_module="${BASH_REMATCH[1]}"
    elif [[ "$line" =~ ^total: ]] && [ -n "$current_module" ]; then
        pct=$(echo "$line" | awk '{print $NF}')
        printf "%-30s %s\n" "$current_module" "$pct"
        current_module=""
    fi
done < "$REPORT_FILE" | sort -t'%' -k2 -rn
echo ""

# Count modules with 0% or no coverage
zero_modules=$(grep -A1 "^=== MODULE:" "$REPORT_FILE" | grep -E "(0\.0%|no coverage|tests failed|no tests)" | wc -l | tr -d ' ')
echo "Modules with 0% or no tests: $zero_modules"
echo ""

# Files with 0% coverage (excluding total lines)
echo "--- Files with 0% Coverage (Top 30) ---"
grep -E '\s+0\.0%$' "$REPORT_FILE" | grep -v "^total:" | head -30
echo ""

# EZ targets - pure function files that are good candidates for table tests
echo "--- EZ Targets (helpers, validators, utils with 0%) ---"
echo "These are likely pure functions, good for table-driven tests:"
echo ""
grep -E '(validate|helper|util|hash|error|sanitize|parse|encode|decode|format|convert).*0\.0%' "$REPORT_FILE" | \
    grep -v "^total:" | \
    head -15
echo ""

# Files with partial coverage (potential quick wins)
echo "--- Partial Coverage (10-50%, potential quick wins) ---"
grep -E '\s+[1-4][0-9]\.[0-9]%$' "$REPORT_FILE" | grep -v "^total:" | head -10
echo ""

# Summary stats
total_funcs=$(grep -E '^\S+\s+\S+\s+[0-9]+\.[0-9]+%$' "$REPORT_FILE" | grep -v "^total:" | wc -l | tr -d ' ')
zero_funcs=$(grep -E '\s+0\.0%$' "$REPORT_FILE" | grep -v "^total:" | wc -l | tr -d ' ')
covered_funcs=$((total_funcs - zero_funcs))

echo "--- Summary ---"
echo "Total functions tracked: $total_funcs"
echo "Functions with 0% coverage: $zero_funcs"
echo "Functions with >0% coverage: $covered_funcs"
if [ "$total_funcs" -gt 0 ]; then
    pct=$((covered_funcs * 100 / total_funcs))
    echo "Function coverage rate: ${pct}%"
fi
