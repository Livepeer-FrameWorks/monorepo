#!/usr/bin/env bash
set -euo pipefail

# Usage: gen_release_manifest.sh <VERSION> <GIT_COMMIT> <BUILD_DATE> "<SERVICES>"
# Example: gen_release_manifest.sh v1.2.3 abc123 2025-09-03T12:00:00Z "quartermaster gateway ..."

VERSION=${1:-"0.0.0-dev"}
GIT_COMMIT=${2:-"unknown"}
BUILD_DATE=${3:-"unknown"}
SERVICES_STR=${4:-""}

out_file="releases/${VERSION}.yaml"

IFS=' ' read -r -a SERVICES <<< "${SERVICES_STR}"

cat > "${out_file}" <<YAML
# FrameWorks Release Manifest
platform_version: ${VERSION}
git_commit: ${GIT_COMMIT}
release_date: ${BUILD_DATE}

services:
YAML

for svc in "${SERVICES[@]}"; do
  # normalize image name scheme to match Makefile docker tags
  image="frameworks-${svc}:${VERSION}"
  # allow injecting digests via DIGEST_<svcname> env, e.g., DIGEST_quartermaster=sha256:...
  # replace '-' with '_' for env var key
  key=${svc//-/_}
  env_key="DIGEST_${key}"
  digest="${!env_key:-}"
  if [[ -n "${digest}" ]]; then
    cat >> "${out_file}" <<YAML
  - name: ${svc}
    image: ${image}
    digest: ${digest}
YAML
  else
    cat >> "${out_file}" <<YAML
  - name: ${svc}
    image: ${image}
YAML
  fi
done

cat >> "${out_file}" <<'YAML'

migrations:
  # Example:
  # quartermaster: [20250903_add_bootstrap_tokens]
  # commodore: []

api_contracts:
  # gateway_graphql_schema: sha256:...
  # internal_apis_min: v1.0.0
  # internal_apis_max: v2.0.0

events:
  # analytics_events_schema: v1

rollout:
  channel: stable
  tenants_canary: []
  regions: []
  max_unhealthy_percent: 5

security:
  signed_manifest: ""   # cosign signature location
  sboms: []              # optional SBOM artifact locations

notes: |
  - Add human-readable release notes here.
YAML

echo "Wrote ${out_file}"
