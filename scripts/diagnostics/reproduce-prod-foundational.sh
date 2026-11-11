#!/usr/bin/env bash
set -uo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root" || exit 1
out=${FOUNDATIONAL_TEST_OUTPUT:-.stack/prod-foundational-regressions/$(date -u +%Y%m%dT%H%M%SZ)}
mkdir -p "$out"
mode=${1:-green}
failed=0
while IFS='|' read -r target packages pattern; do
 name=${target#test-}
 make "$target" "GO_TEST_PACKAGES=$packages" "GO_TEST_FLAGS=-run '$pattern'" >"$out/$name.log" 2>&1
 status=$?
 printf '%s\t%s\t%s\n' "$target" "$status" "$pattern" >>"$out/results-$mode.tsv"
 cat "$out/$name.log"
 if [[ $mode == red ]]; then
  if [[ $status == 0 ]] || ! grep -q -- '--- FAIL: Test' "$out/$name.log"; then failed=1; fi
 elif [[ $status != 0 ]]; then failed=1
 fi
done <<'CASES'
test-foghorn|./internal/control|TestServerCertHolderRejectsUnknownPublicIdentity|TestServerCertHolderRejectsExpiredRetainedLeaf|TestPreRegisterEdgeWaitsForPrivatePublicIdentity
test-quartermaster|./internal/grpc|TestInfrastructureWriteScopeIncludesRead|TestValidateBootstrapTokenSeparatesRendezvousFromPrivateIdentity
test-bridge|./internal/resolvers|TestPlacementWriteScopeIncludesRead
test-cli|./cmd|TestRenderEdgeDeployResultRetainsCreatedClusterAfterPreregisterFailure
test-periscope-query|./internal/handlers|TestReservationMalformedTenantDoesNotBlockHealthyTenantOrRelease|TestReservationQueryFailureDoesNotReleasePreviousHolds
test-purser|./internal/grpc|TestPurserRefreshRetainsFailedCacheObligationAfterAuthorityAcceptance
test-navigator|./cmd/navigator|TestApplyAckDoesNotWaitForAliasProvider|TestApplyAckPersistenceFailureDoesNotWakeOrAccept
CASES
exit "$failed"
