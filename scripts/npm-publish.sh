#!/bin/bash
# Publish @livepeer-frameworks npm packages whose local version differs from npm.
#
# Usage:
#   ./scripts/npm-publish.sh           # Build + publish all bumped packages
#   ./scripts/npm-publish.sh --dry-run # Show what would be published
#
# Workflow:
#   1. Bump versions in package.json files
#   2. Run this script — it logs in once, builds, and publishes only changed versions
#
# Build order: core packages first (react/svelte depend on them via workspace:*)

set -euo pipefail

DRY_RUN=false
if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=true
fi

# All packages in dependency order (cores first)
PACKAGES=(
  npm_player/packages/core
  npm_player/packages/react
  npm_player/packages/svelte
  npm_player/packages/wc
  npm_studio/packages/core
  npm_studio/packages/react
  npm_studio/packages/svelte
  npm_studio/packages/wc
)

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Ensure logged in
if ! npm whoami &>/dev/null; then
  echo "Not logged in to npm. Logging in..."
  npm login
  echo ""
fi
echo "Logged in as: $(npm whoami)"
echo ""

to_publish=()

for pkg_dir in "${PACKAGES[@]}"; do
  pkg_json="$ROOT/$pkg_dir/package.json"
  name=$(node -e "console.log(require('$pkg_json').name)")
  local_version=$(node -e "console.log(require('$pkg_json').version)")

  # Get published version (empty string if never published)
  remote_version=$(npm view "$name" version 2>/dev/null || echo "")

  if [[ "$local_version" == "$remote_version" ]]; then
    echo "  $name@$local_version — already published, skipping"
  else
    echo "* $name@$local_version — ${remote_version:-unpublished} -> $local_version"
    to_publish+=("$pkg_dir")
  fi
done

echo ""

if [[ ${#to_publish[@]} -eq 0 ]]; then
  echo "Nothing to publish. Bump versions in package.json first."
  exit 0
fi

if $DRY_RUN; then
  echo "Dry run — would publish ${#to_publish[@]} package(s). Re-run without --dry-run to publish."
  exit 0
fi

echo "Building and publishing ${#to_publish[@]} package(s)..."
echo ""

for pkg_dir in "${to_publish[@]}"; do
  pkg_json="$ROOT/$pkg_dir/package.json"
  name=$(node -e "console.log(require('$pkg_json').name)")
  local_version=$(node -e "console.log(require('$pkg_json').version)")

  echo "--- $name@$local_version ---"

  echo "  Building..."
  (cd "$ROOT/$pkg_dir" && pnpm run build)

  echo "  Publishing..."
  (cd "$ROOT/$pkg_dir" && pnpm publish --access public --no-git-checks)

  echo "  Done."
  echo ""
done

echo "All done."
