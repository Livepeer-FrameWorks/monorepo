#!/bin/bash
# Run tests with coverage on all Go modules and generate analysis report
# Output: reports/coverage-report.txt (per-function coverage %)
#
# Filters out generated code (same patterns as Makefile):
#   - *.pb.go (protobuf)
#   - *_grpc.pb.go (gRPC)
#   - graph/generated/* (GraphQL)
#   - graph/model/models_gen.go (GraphQL models)

# Don't exit on error - some modules may have no tests
set +e

REPORT_DIR="reports"
REPORT_FILE="$REPORT_DIR/coverage-report.txt"

mkdir -p "$REPORT_DIR"
> "$REPORT_FILE"

echo "Running coverage on all Go modules..."
echo ""

for dir in $(find . -name "go.mod" -exec dirname {} \; | sort); do
    module_name=$(basename "$dir")
    echo "==> $module_name"
    echo "=== MODULE: $module_name ===" >> "$REPORT_FILE"

    (
        cd "$dir"
        tmpfile=$(mktemp)

        # Run tests with coverage
        if go test ./... -coverprofile="$tmpfile" -covermode=atomic -count=1 >/dev/null 2>&1; then
            if [ -s "$tmpfile" ]; then
                # Filter out generated code (same as Makefile)
                filtered=$(mktemp)
                grep -v '\.pb\.go:' "$tmpfile" | \
                    grep -v '_grpc\.pb\.go:' | \
                    grep -v 'graph/generated/' | \
                    grep -v 'graph/model/models_gen\.go:' > "$filtered" || true

                # Get coverage stats (filtered)
                if [ -s "$filtered" ]; then
                    go tool cover -func="$filtered" 2>/dev/null >> "../$REPORT_FILE"
                else
                    echo "  (all code is generated)" >> "../$REPORT_FILE"
                fi
                rm -f "$filtered"
            else
                echo "  (no coverage data)" >> "../$REPORT_FILE"
            fi
        else
            echo "  (tests failed or no tests)" >> "../$REPORT_FILE"
        fi
        rm -f "$tmpfile"
    )

    echo "" >> "$REPORT_FILE"
done

echo ""
echo "Report saved to: $REPORT_FILE"
echo "Run 'scripts/coverage-analyze.sh' to see summary"
