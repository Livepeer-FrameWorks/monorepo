#!/bin/bash
# Run mutation testing on Go packages using Gremlins
#
# Usage:
#   ./scripts/mutation-test.sh [package_path]    # Test specific package
#   ./scripts/mutation-test.sh --all             # Test all critical modules
#   ./scripts/mutation-test.sh --changed         # Test packages changed vs main
#   ./scripts/mutation-test.sh --changed HEAD~3  # Test packages changed in last 3 commits
#
# Examples:
#   ./scripts/mutation-test.sh pkg/auth/
#   ./scripts/mutation-test.sh pkg/x402/
#   ./scripts/mutation-test.sh --all
#   ./scripts/mutation-test.sh --changed
#
# Results:
#   KILLED     - Test caught the mutation (good)
#   LIVED      - Test missed the mutation (bad - add tests!)
#   NOT_COVERED - No tests for this code
#   TIMED_OUT  - Tests took too long
#
# Install gremlins: go install github.com/go-gremlins/gremlins/cmd/gremlins@latest

set -e

# Critical modules to test with --all
CRITICAL_PACKAGES=(
    # Core / security
    "pkg/auth"
    "pkg/x402"
    "pkg/clients/listmonk"
    "api_gateway/internal/errors"
    "api_gateway/internal/webhooks"
    "api_gateway/internal/middleware"
    # Service business logic
    "api_firehose/internal/grpc"
    "api_dns/internal/logic"
    "api_dns/internal/store"
    "api_dns/internal/provider/cloudflare"
    "api_dns/internal/worker"
    "api_sidecar/internal/control"
    "api_sidecar/internal/handlers"
    "api_mesh/internal/agent"
    "api_mesh/internal/dns"
    "api_mesh/internal/wireguard"
    "api_realtime/internal/grpc"
    "api_forms/internal/handlers"
    "api_forms/internal/validation"
)

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Check if gremlins is installed
if ! command -v gremlins &> /dev/null; then
    echo -e "${RED}Error: gremlins not found${NC}"
    echo "Install with: go install github.com/go-gremlins/gremlins/cmd/gremlins@latest"
    exit 1
fi

# Parse arguments
MODE="single"
COMPARE_REF="origin/main"
PACKAGES=()

case "${1:-}" in
    --all)
        MODE="all"
        PACKAGES=("${CRITICAL_PACKAGES[@]}")
        ;;
    --changed)
        MODE="changed"
        COMPARE_REF="${2:-origin/main}"
        ;;
    --help|-h)
        echo "Usage: $0 [OPTIONS] [package_path]"
        echo ""
        echo "Options:"
        echo "  --all              Test all critical modules"
        echo "  --changed [REF]    Test packages changed vs REF (default: origin/main)"
        echo "  --help             Show this help"
        echo ""
        echo "Examples:"
        echo "  $0 pkg/auth/           # Test auth package"
        echo "  $0 --all               # Test all critical modules"
        echo "  $0 --changed           # Test packages changed vs main"
        echo "  $0 --changed HEAD~5    # Test packages changed in last 5 commits"
        exit 0
        ;;
    "")
        echo -e "${YELLOW}No package specified. Use --all for critical modules or specify a path.${NC}"
        echo ""
        echo "Examples:"
        echo "  $0 pkg/auth/"
        echo "  $0 --all"
        echo "  $0 --changed"
        exit 1
        ;;
    *)
        MODE="single"
        PACKAGES=("$1")
        ;;
esac

# Get changed packages if --changed
if [ "$MODE" == "changed" ]; then
    echo -e "${YELLOW}Finding packages changed vs $COMPARE_REF...${NC}"

    CHANGED=$(git diff --name-only "$COMPARE_REF" 2>/dev/null | \
        grep '\.go$' | \
        grep -v '_test\.go$' | \
        grep -v '\.pb\.go$' | \
        grep -v 'generated' | \
        grep -v 'models_gen\.go$' || true)

    if [ -z "$CHANGED" ]; then
        echo -e "${GREEN}No Go files changed - nothing to test${NC}"
        exit 0
    fi

    # Extract unique package directories
    mapfile -t PACKAGES < <(echo "$CHANGED" | xargs -I{} dirname {} | sort -u)
    echo "Changed packages: ${PACKAGES[*]}"
fi

echo ""
echo -e "${GREEN}Running mutation tests...${NC}"
echo "Mode: $MODE"
echo "Packages: ${PACKAGES[*]}"
echo ""

# Run mutation tests
FAILED=0
TOTAL=${#PACKAGES[@]}

for pkg in "${PACKAGES[@]}"; do
    echo ""
    echo "=========================================="
    echo -e "${YELLOW}Testing: $pkg${NC}"
    echo "=========================================="

    # Find the go.mod directory for this package (monorepo support)
    MODULE_DIR="."
    RELATIVE_PKG="$pkg"

    # Check if package is under a subdirectory with its own go.mod
    if [[ "$pkg" == pkg/* ]] && [ -f "pkg/go.mod" ]; then
        MODULE_DIR="pkg"
        RELATIVE_PKG="${pkg#pkg/}"
    elif [[ "$pkg" == api_* ]] || [[ "$pkg" == cli/* ]]; then
        # Extract the service directory (e.g., api_gateway from api_gateway/internal/errors)
        SERVICE_DIR=$(echo "$pkg" | cut -d'/' -f1)
        if [ -f "$SERVICE_DIR/go.mod" ]; then
            MODULE_DIR="$SERVICE_DIR"
            RELATIVE_PKG="${pkg#$SERVICE_DIR/}"
        fi
    fi

    echo "Module: $MODULE_DIR, Package: $RELATIVE_PKG"

    if (cd "$MODULE_DIR" && gremlins unleash "./$RELATIVE_PKG" \
        --timeout-coefficient 5 \
        --workers 4); then
        echo -e "${GREEN}$pkg: DONE${NC}"
    else
        echo -e "${RED}$pkg: FAILED (or no tests)${NC}"
        FAILED=$((FAILED + 1))
    fi
done

echo ""
echo "=========================================="
echo "SUMMARY"
echo "=========================================="
echo "Packages tested: $TOTAL"
echo "Failed/skipped: $FAILED"
echo ""
echo "Interpret results:"
echo "  - KILLED: Tests caught the mutation (good)"
echo "  - LIVED: Tests MISSED the mutation (add assertions!)"
echo "  - NOT_COVERED: No tests exist for this code"
echo ""
echo "Target mutation scores:"
echo "  - Security/billing code: >80%"
echo "  - Business logic: >60%"
echo "  - Utilities: >50%"
