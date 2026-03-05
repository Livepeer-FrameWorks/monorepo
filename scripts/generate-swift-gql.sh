#!/bin/bash
# Generates Swift string constants from .gql operation files.
# The generated file is committed to git and used by the macOS tray app
# so it stays in sync with the canonical .gql fragments and queries.
#
# Usage: ./scripts/generate-swift-gql.sh
#
# Strips Houdini-specific directives (@paginate, @mask_disable) that
# the gateway GraphQL endpoint doesn't understand.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OPS_DIR="$REPO_ROOT/pkg/graphql/operations"
OUT="$REPO_ROOT/app_mac/Sources/Gateway/GeneratedQueries.swift"

{
  echo "// AUTO-GENERATED from pkg/graphql/operations/ — do not edit"
  echo "// Re-generate: ./scripts/generate-swift-gql.sh"
  echo "// swiftlint:disable all"
  echo ""
  echo "enum GQL {"

  for dir in fragments queries mutations subscriptions; do
    if [ ! -d "$OPS_DIR/$dir" ]; then
      continue
    fi
    echo ""
    echo "  // MARK: - ${dir}"

    for f in "$OPS_DIR/$dir"/*.gql; do
      [ -f "$f" ] || continue
      name=$(basename "$f" .gql)
      echo ""
      echo "  static let $name = \"\"\""
      # Strip Houdini directives: @paginate(...) and @mask_disable
      sed -E 's/@paginate(\([^)]*\))?//g; s/@mask_disable//g' "$f"
      echo "  \"\"\""
    done
  done

  echo "}"
} > "$OUT"

echo "[generate-swift-gql] Wrote $(grep -c 'static let' "$OUT") constants to $OUT"
