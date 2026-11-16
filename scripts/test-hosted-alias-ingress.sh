#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
image=$(awk '
  /^  - name: / { matched = ($3 == "nginx"); next }
  matched && /^    image:/ { image = $2 }
  matched && /^    digest:/ { digest = $2 }
  END { if (image != "" && digest != "") print image "@" digest }
' "$repo/config/infrastructure.yaml")
[[ -n "$image" ]] || { echo 'release-pinned nginx image missing' >&2; exit 1; }
cd "$repo"
result_log=$(mktemp)
trap 'rm -f "$result_log"' EXIT
run_required_test() {
  local target=$1 packages=$2 test_name=$3
  # Make expands dollar signs in command-line variables before invoking Go.
  FRAMEWORKS_NGINX_TEST_IMAGE="$image" "${MAKE:-make}" --no-print-directory "$target" \
    GO_TAG_FLAGS=-tags=ingress_verify "GO_TEST_PACKAGES=$packages" \
    "GO_TEST_FLAGS=-run ^${test_name}\$\$ -v -timeout 180s" | tee "$result_log"
  grep -q "^--- PASS: $test_name (" "$result_log" || {
    echo "required hosted ingress test $test_name did not pass" >&2
    exit 1
  }
}
run_required_test test-privateer ./internal/agent TestTenantAliasPrivateerDockerIngressLifecycle
run_required_test test-cli ./pkg/provisioner TestNginxRootConfigAcceptsMaximumLengthAlias
