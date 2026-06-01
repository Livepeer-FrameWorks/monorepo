#!/usr/bin/env bash
set -euo pipefail

# Refuses commits that add or modify migration files outside the next
# release version dir. Released migrations are immutable; post-release
# fixes must land in exactly one of the three next semantic versions from
# the latest release tag: next patch, next minor, or next major.
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

if [[ ! "$latest_tag" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  echo "ERROR: latest migration tag is not semantic: $latest_tag" >&2
  exit 1
fi

major=${BASH_REMATCH[1]}
minor=${BASH_REMATCH[2]}
patch=${BASH_REMATCH[3]}

allowed_patch="v${major}.${minor}.$((patch + 1))"
allowed_minor="v${major}.$((minor + 1)).0"
allowed_major="v$((major + 1)).0.0"

violations=()
seen_versions=()

version_gt_latest() {
  local ver=$1
  local higher
  higher=$(printf '%s\n%s\n' "$ver" "$latest_tag" | sort -V | tail -1)
  [ "$ver" != "$latest_tag" ] && [ "$higher" = "$ver" ]
}

is_allowed_next_version() {
  local ver=$1
  [ "$ver" = "$allowed_patch" ] || [ "$ver" = "$allowed_minor" ] || [ "$ver" = "$allowed_major" ]
}

remember_version() {
  local ver=$1
  local existing
  for existing in "${seen_versions[@]}"; do
    [ "$existing" = "$ver" ] && return
  done
  seen_versions+=("$ver")
}

check_added_or_modified() {
  local f=$1
  if [[ "$f" =~ $migration_re ]]; then
    local ver="${BASH_REMATCH[2]}"
    if ! version_gt_latest "$ver"; then
      violations+=("  $f  (version $ver <= latest tag $latest_tag)")
      return
    fi
    if ! is_allowed_next_version "$ver"; then
      violations+=("  $f  (version $ver is not the next patch/minor/major from $latest_tag)")
      return
    fi
    remember_version "$ver"
  fi
}

check_deleted() {
  local f=$1
  if [[ "$f" =~ $migration_re ]]; then
    if git cat-file -e "$base_ref:$f" 2>/dev/null; then
      violations+=("  $f  (deletes released migration)")
    fi
  fi
}

entries=()
mapfile -d '' entries < <(git diff --cached --name-status -z -- "$@")

i=0
while [ "$i" -lt "${#entries[@]}" ]; do
  status=${entries[$i]}
  i=$((i + 1))

  case "$status" in
    R*|C*)
      old_path=${entries[$i]}
      i=$((i + 1))
      new_path=${entries[$i]}
      i=$((i + 1))
      check_deleted "$old_path"
      check_added_or_modified "$new_path"
      ;;
    D*)
      path=${entries[$i]}
      i=$((i + 1))
      check_deleted "$path"
      ;;
    *)
      path=${entries[$i]}
      i=$((i + 1))
      check_added_or_modified "$path"
      ;;
  esac
done

if [ ${#seen_versions[@]} -gt 1 ]; then
  violations+=("  staged migrations use multiple next version dirs: ${seen_versions[*]}")
fi

if [ ${#violations[@]} -gt 0 ]; then
  echo "ERROR: migration version directory is invalid." >&2
  echo "Latest tag: $latest_tag" >&2
  echo "Allowed next versions: $allowed_patch, $allowed_minor, $allowed_major" >&2
  echo "Offending files:" >&2
  printf '%s\n' "${violations[@]}" >&2
  echo >&2
  echo "Move the migration into one of the allowed next version dirs." >&2
  echo "If you are intentionally rewinding the release, delete the tag and recut it first." >&2
  exit 1
fi
