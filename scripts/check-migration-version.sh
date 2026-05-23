#!/usr/bin/env bash
set -euo pipefail

# Refuses commits that add or modify migration files under a version dir
# at or below the latest git tag. Released migrations are immutable; a
# post-release fix must land in a higher version dir (or, if the release
# is being rewound, the tag itself must be deleted and recut first).
#
# Deleting a released migration is also blocked. Deleting a migration that
# only exists on the current branch is allowed so a bad pre-release path can
# be corrected before the commit is amended.

latest_tag=$(git tag --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)

if [ -z "$latest_tag" ]; then
  exit 0
fi

migration_re='^pkg/database/(sql/migrations|sql/clickhouse/migrations)/[^/]+/(v[0-9]+\.[0-9]+\.[0-9]+)/'
base_ref=$(git merge-base HEAD origin/master 2>/dev/null || git rev-parse HEAD)

violations=()

for f in "$@"; do
  if [[ "$f" =~ $migration_re ]]; then
    ver="${BASH_REMATCH[2]}"
    if ! git cat-file -e ":$f" 2>/dev/null; then
      if git cat-file -e "$base_ref:$f" 2>/dev/null; then
        violations+=("  $f  (deletes released migration)")
      fi
      continue
    fi
    # ver is the higher of (ver, latest_tag) iff sort -V puts latest_tag first.
    higher=$(printf '%s\n%s\n' "$ver" "$latest_tag" | sort -V | tail -1)
    if [ "$ver" = "$latest_tag" ] || [ "$higher" != "$ver" ]; then
      violations+=("  $f  (version $ver <= latest tag $latest_tag)")
    fi
  fi
done

if [ ${#violations[@]} -gt 0 ]; then
  echo "ERROR: migrations under released version dirs are immutable." >&2
  echo "Latest tag: $latest_tag" >&2
  echo "Offending files:" >&2
  printf '%s\n' "${violations[@]}" >&2
  echo >&2
  echo "Move the migration into the next in-progress version dir (vX.Y.Z > $latest_tag)." >&2
  echo "If you are intentionally rewinding the release, delete the tag and recut it first." >&2
  exit 1
fi
