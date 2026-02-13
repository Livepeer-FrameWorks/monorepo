#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

make graphql-frontend

if ! git diff --quiet -- website_application/\$houdini; then
  echo "Frontend GraphQL codegen drift detected in website_application/\$houdini."
  echo "Run 'make graphql-frontend' and commit the generated changes."
  git --no-pager diff --stat -- website_application/\$houdini
  exit 1
fi

echo "Frontend GraphQL codegen is up to date."
