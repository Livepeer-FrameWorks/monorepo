#!/usr/bin/env bash
set -uo pipefail
status=0
run() {
  "$@" || status=1
}
run make --no-print-directory test-navigator GO_TEST_PACKAGES='./cmd/navigator ./internal/worker ./internal/provider/bunny' GO_TEST_FLAGS="-run 'TestTenantAlias|TestReconcileRecordSetPromotesExistingRecord' -v"
run make --no-print-directory test-cli GO_TEST_PACKAGES='./cmd ./pkg/provisioner' GO_TEST_FLAGS='-run TenantAlias -v'
run make --no-print-directory test-privateer GO_TEST_PACKAGES='./internal/agent' GO_TEST_FLAGS='-run TenantAlias -v'
run make --no-print-directory test-pkg GO_TEST_PACKAGES='./middleware' GO_TEST_FLAGS='-run TestPublicPlayback -v'
run make --no-print-directory test-foghorn GO_TEST_PACKAGES='./internal/control ./internal/balancer' GO_TEST_FLAGS="-run 'TestPreparedIngestEndpointPreservesSelectedRegion|TestPlacementRouterUsesObservedRegion' -v"
exit "$status"
