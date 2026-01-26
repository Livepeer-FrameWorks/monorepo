#!/usr/bin/env bash
set -uo pipefail

# Dead code analysis for Go services using golang.org/x/tools/cmd/deadcode
# Outputs: reports/deadcode-{service}.txt

REPORTS_DIR="${REPORTS_DIR:-reports}"
mkdir -p "$REPORTS_DIR"

echo "=== Dead Code Analysis (deadcode) ==="
echo ""

# Service directories and their main packages
SERVICES="
api_gateway:./cmd/bridge
api_control:./cmd/commodore
api_tenants:./cmd/quartermaster
api_billing:./cmd/purser
api_analytics_ingest:./cmd/periscope
api_analytics_query:./cmd/periscope
api_firehose:./cmd/decklog
api_balancing:./cmd/foghorn
api_sidecar:./cmd/helmsman
api_realtime:./cmd/signalman
api_dns:./cmd/navigator
api_mesh:./cmd/privateer
api_forms:./cmd/forms
api_ticketing:./cmd/deckhand
cli:.
pkg:./...
"

for entry in $SERVICES; do
    service="${entry%%:*}"
    main_pkg="${entry#*:}"
    report_file="$REPORTS_DIR/deadcode-${service}.txt"

    echo "Analyzing $service ($main_pkg)..."

    if [[ -d "$service" ]]; then
        (
            cd "$service"
            # Run deadcode, filtering out generated files
            output=$(deadcode -test "$main_pkg" 2>&1)
            status=$?
            if [[ "$status" -ne 0 ]]; then
                {
                    echo "# ERROR: deadcode failed for $service (exit $status)"
                    echo "$output"
                } > "../$report_file"
                echo "  WARNING: deadcode failed (exit $status)"
                exit 0
            fi
            set +o pipefail
            printf '%s\n' "$output" | \
                grep -v '\.pb\.go:' | \
                grep -v '_grpc\.pb\.go:' | \
                grep -v 'graph/generated/' | \
                grep -v 'graph/model/models_gen' \
                > "../$report_file"
            set -o pipefail
        )

        count=$(wc -l < "$report_file" | tr -d ' ')
        if [[ "$count" -gt 0 ]]; then
            echo "  Found $count potentially dead functions"
        else
            echo "  No dead code detected"
        fi
    else
        echo "  Skipped (directory not found)"
    fi
done

echo ""
echo "Reports saved to $REPORTS_DIR/deadcode-*.txt"
