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
FRAMEWORKS_NGINX_TEST_IMAGE="$image" "${MAKE:-make}" --no-print-directory test-privateer \
  GO_TAG_FLAGS=-tags=ingress_verify GO_TEST_PACKAGES=./internal/agent \
  GO_TEST_FLAGS='-run ^TestTenantAliasPrivateerDockerIngressLifecycle$ -v -timeout 180s'
