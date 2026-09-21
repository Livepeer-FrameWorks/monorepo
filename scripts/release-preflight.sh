#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]] || [[ ! $1 =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc(0|[1-9][0-9]*))?$ ]]; then
  echo "Usage: $0 vX.Y.Z[-rcN] (lowercase rc)" >&2
  exit 2
fi

release_version=$1
release_base=${release_version%%-rc*}
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/.." && pwd)
catalog_path=cli/internal/releases/catalog.yaml
release_paths=(
  "$catalog_path"
  pkg/database/sql/schema
  pkg/database/sql/clickhouse/periscope.sql
  pkg/database/sql/migrations
  pkg/database/sql/clickhouse/migrations
)

cd "$repo_root"

if ! git diff --quiet HEAD -- "${release_paths[@]}" ||
  [[ -n $(git ls-files --others --exclude-standard -- "${release_paths[@]}") ]]; then
  echo "release-preflight: release catalog, schemas, and migrations must be committed before tagging" >&2
  exit 1
fi

catalog_version=$(awk '
  /^releases:[[:space:]]*$/ { in_releases = 1; next }
  in_releases && /^[^[:space:]#]/ { exit }
  in_releases && /^[[:space:]]*-[[:space:]]+version:[[:space:]]+/ { version = $3 }
  END { print version }
' "$catalog_path")
if [[ "$catalog_version" != "$release_base" ]]; then
  echo "release-preflight: target $release_version does not match latest catalog release ${catalog_version:-<none>}" >&2
  exit 1
fi

scripts/check-migration-version.sh --worktree
(cd cli && go run . release-metadata "$release_version" >/dev/null)

echo "Release preflight passed: $release_version"
