#!/bin/bash
# Run mutation testing on Go packages using Gremlins
# Usage: ./scripts/mutation-test.sh [package_path]
#
# Examples:
#   ./scripts/mutation-test.sh pkg/auth/          # Test auth package
#   ./scripts/mutation-test.sh pkg/x402/          # Test x402 package
#   ./scripts/mutation-test.sh ./...              # Test everything (slow!)
#
# Results:
#   KILLED     - Test caught the mutation (good)
#   LIVED      - Test missed the mutation (bad - add tests!)
#   NOT_COVERED - No tests for this code
#   TIMED_OUT  - Tests took too long
#
# Install gremlins: go install github.com/go-gremlins/gremlins/cmd/gremlins@latest

set -e

# Check if gremlins is installed
if ! command -v gremlins &> /dev/null; then
    echo "Error: gremlins not found"
    echo "Install with: go install github.com/go-gremlins/gremlins/cmd/gremlins@latest"
    exit 1
fi

# Default to critical packages if no argument
PACKAGE="${1:-pkg/auth/}"

echo "Running mutation tests on: $PACKAGE"
echo ""
echo "This may take a while..."
echo ""

# Run gremlins
# --tags: build tags (if any)
# --timeout-coefficient: multiplier for test timeout (default 10x)
# --threshold: fail if mutation score below this (0 = no threshold)
gremlins unleash "$PACKAGE" \
    --timeout-coefficient 5 \
    --workers 4

echo ""
echo "Mutation testing complete."
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
