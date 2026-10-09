#!/usr/bin/env bash
set -eo pipefail

# One release may be prepared ahead of the last final tag. A reachable RC may
# also freeze that release while its single successor is prepared; the frozen
# bucket accepts only migration files already present in the RC.

catalog_path="cli/internal/releases/catalog.yaml"
migration_re='^pkg/database/(sql/migrations|sql/clickhouse/migrations)/[^/]+/(v[0-9]+\.[0-9]+\.[0-9]+)/'
diff_base=""
worktree=false
path_args=()

while [ "$#" -gt 0 ]; do
  case "$1" in
    --diff-base)
      if [ "$#" -lt 2 ]; then
        echo "ERROR: --diff-base requires a Git revision" >&2
        exit 2
      fi
      diff_base=$2
      shift 2
      ;;
    --worktree)
      worktree=true
      shift
      ;;
    --)
      shift
      while [ "$#" -gt 0 ]; do
        path_args+=("$1")
        shift
      done
      ;;
    *)
      path_args+=("$1")
      shift
      ;;
  esac
done

latest_tag=""
while IFS= read -r tag; do
  if [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    latest_tag=$tag
    break
  fi
done < <(git tag --merged HEAD --sort=-v:refname)

if [ -z "$latest_tag" ]; then
  echo "ERROR: no reachable final release tag (vX.Y.Z) found; cannot establish shipped migration state" >&2
  exit 1
fi

if [ ! -f "$catalog_path" ]; then
  echo "ERROR: release catalog not found: $catalog_path" >&2
  exit 1
fi

version_gt() {
  local left=${1#v}
  local right=${2#v}
  local left_major left_minor left_patch right_major right_minor right_patch
  IFS=. read -r left_major left_minor left_patch <<< "$left"
  IFS=. read -r right_major right_minor right_patch <<< "$right"

  if [ "$left_major" -ne "$right_major" ]; then
    [ "$left_major" -gt "$right_major" ]
    return
  fi
  if [ "$left_minor" -ne "$right_minor" ]; then
    [ "$left_minor" -gt "$right_minor" ]
    return
  fi
  [ "$left_patch" -gt "$right_patch" ]
}

catalog_versions=()
while IFS= read -r version; do
  catalog_versions+=("$version")
done < <(
  awk '
    /^releases:[[:space:]]*$/ { in_releases = 1; next }
    in_releases && /^[^[:space:]#]/ { exit }
    in_releases && /^[[:space:]]*-[[:space:]]+version:[[:space:]]+/ { print $3 }
  ' "$catalog_path"
)

pending_versions=()
for version in "${catalog_versions[@]}"; do
  if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "ERROR: release catalog version is not a final vX.Y.Z release: $version" >&2
    exit 1
  fi
  if version_gt "$version" "$latest_tag"; then
    pending_versions+=("$version")
  fi
done

schema_migration_floor=$(awk '$1 == "schema_migration_floor:" { print $2; exit }' "$catalog_path")
if [[ ! "$schema_migration_floor" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "ERROR: release catalog must declare schema_migration_floor as a final vX.Y.Z release" >&2
  exit 1
fi

reachable_rc_tags=()
while IFS= read -r tag; do
  if [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-rc[0-9]+$ ]]; then
    reachable_rc_tags+=("$tag")
  fi
done < <(git tag --merged HEAD --sort=-v:refname)

violations=()
pending_version=""
frozen_version=""
frozen_rc=""
if [ "${#pending_versions[@]}" -eq 2 ]; then
  frozen_version=${pending_versions[0]}
  pending_version=${pending_versions[1]}
  if version_gt "$frozen_version" "$pending_version"; then
    frozen_version=${pending_versions[1]}
    pending_version=${pending_versions[0]}
  fi
  for tag in "${reachable_rc_tags[@]}"; do
    if [ "${tag%%-rc*}" = "$frozen_version" ]; then
      frozen_rc=$tag
      break
    fi
  done
  if [ -z "$frozen_rc" ]; then
    violations+=("  release catalog declares multiple unshipped releases after $latest_tag without a reachable RC freezing $frozen_version: ${pending_versions[*]}")
  fi
elif [ "${#pending_versions[@]}" -gt 2 ]; then
  violations+=("  release catalog declares multiple unshipped releases after $latest_tag: ${pending_versions[*]}")
elif [ "${#pending_versions[@]}" -eq 1 ]; then
  pending_version=${pending_versions[0]}
fi

check_pending_path() {
  local file=$1
  if [[ ! "$file" =~ $migration_re ]]; then
    return
  fi

  local version=${BASH_REMATCH[2]}
  if version_gt "$version" "$latest_tag"; then
    if [ -n "$frozen_rc" ] && [ "$version" = "$frozen_version" ]; then
      if ! git cat-file -e "$frozen_rc:$file" 2>/dev/null; then
        violations+=("  $file  (adds a migration to $frozen_version, frozen at $frozen_rc while $pending_version is prepared)")
      fi
      return
    fi
    if [ -z "$pending_version" ]; then
      violations+=("  $file  (migration $version is newer than shipped tag $latest_tag, but the catalog declares no pending release)")
    elif [ "$version" != "$pending_version" ]; then
      violations+=("  $file  (migration $version does not target the one pending catalog release $pending_version)")
    fi
  fi
}

# Inventory the complete checked-out tree, not only the current diff. This is
# what prevents undeclared future migration buckets from hiding beside the pending release.
while IFS= read -r file; do
  check_pending_path "$file"
done < <(find pkg/database/sql/migrations pkg/database/sql/clickhouse/migrations -type f -name '*.sql' -print | LC_ALL=C sort)

check_added_or_modified() {
  local file=$1
  if [[ ! "$file" =~ $migration_re ]]; then
    return
  fi

  local version=${BASH_REMATCH[2]}
  if ! version_gt "$version" "$latest_tag"; then
    violations+=("  $file  (changes a migration at $version that is immutable because $latest_tag has shipped)")
    return
  fi
  check_pending_path "$file"
}

check_deleted() {
  local file=$1
  if [[ ! "$file" =~ $migration_re ]]; then
    return
  fi

  local version=${BASH_REMATCH[2]}
  if ! version_gt "$version" "$latest_tag"; then
    # A declared schema-floor release may consolidate all older SQL into the canonical baselines. This is intentionally
    # deletion-only: modifying shipped SQL is still forbidden, and deletion is refused unless the floor itself is the
    # one pending catalog release. That makes a squash an explicit release operation rather than a permanent escape.
    if [ "$pending_version" = "$schema_migration_floor" ] && version_gt "$schema_migration_floor" "$version"; then
      return
    fi
    violations+=("  $file  (deletes a migration at $version that is immutable because $latest_tag has shipped)")
  fi
}

check_name_status_stream() {
  while IFS= read -r -d '' status; do
    case "$status" in
      R*|C*)
        IFS= read -r -d '' old_path
        IFS= read -r -d '' new_path
        check_deleted "$old_path"
        check_added_or_modified "$new_path"
        ;;
      D*)
        IFS= read -r -d '' path
        check_deleted "$path"
        ;;
      *)
        IFS= read -r -d '' path
        check_added_or_modified "$path"
        ;;
    esac
  done
}

latest_tag_commit=$(git rev-parse "$latest_tag^{commit}")

# Every reachable RC above the final tag protects its recorded migration bytes,
# including the frozen release when a successor becomes the pending release.
for rc_tag in "${reachable_rc_tags[@]}"; do
  if ! version_gt "${rc_tag%%-rc*}" "$latest_tag"; then
    continue
  fi
  rc_diff=(git diff --no-renames --name-only -z --diff-filter=MD)
  if [ -n "$diff_base" ]; then
    rc_diff+=("$rc_tag" HEAD)
  elif [ "$worktree" = true ]; then
    rc_diff+=("$rc_tag")
  else
    rc_diff+=(--cached "$rc_tag")
  fi
  while IFS= read -r -d '' file; do
    if [[ "$file" =~ $migration_re ]]; then
      violations+=("  $file  (changes migration bytes already shipped in $rc_tag; append a new migration instead)")
    fi
  done < <("${rc_diff[@]}" -- "${path_args[@]}")
done

if [ -n "$diff_base" ]; then
  if ! git cat-file -e "$diff_base^{commit}" 2>/dev/null; then
    echo "WARNING: release-state diff base is not available: $diff_base; shipped migrations are compared against $latest_tag" >&2
  fi

  # The event base can predate a release commit even when its tag is already
  # visible to this job. The shipped tag tree is the immutable baseline: it
  # excludes migrations contained in that release and includes every later
  # committed change, even when a subsequent event base would hide it.
  check_name_status_stream < <(git diff --name-status -z "$latest_tag_commit"...HEAD -- "${path_args[@]}")
elif [ "$worktree" = true ]; then
  # Compare the final working tree directly with shipped SQL. An intermediate
  # feature commit can contain a migration moved after a concurrent release.
  check_name_status_stream < <(git diff --name-status -z "$latest_tag_commit" -- "${path_args[@]}")
  while IFS= read -r -d '' file; do
    check_added_or_modified "$file"
  done < <(git ls-files --others --exclude-standard -z -- "${path_args[@]}")
else
  # Pre-commit mode: lefthook supplies staged migration paths as path_args.
  check_name_status_stream < <(git diff --cached --name-status -z "$latest_tag_commit" -- "${path_args[@]}")
fi

if [ "${#violations[@]}" -gt 0 ]; then
  echo "ERROR: release/migration state is inconsistent." >&2
  echo "Latest shipped tag: $latest_tag" >&2
  if [ -n "$pending_version" ]; then
    echo "Pending catalog release: $pending_version" >&2
  else
    echo "Pending catalog release: none" >&2
  fi
  printf '%s\n' "${violations[@]}" >&2
  echo >&2
  echo "Code and migrations must target the pending release declared in $catalog_path; a preceding RC bucket stays frozen." >&2
  exit 1
fi

if [ -n "$pending_version" ]; then
  echo "Release state valid: shipped=$latest_tag, frozen_rc=${frozen_rc:-none}, pending=$pending_version"
else
  echo "Release state valid: shipped=$latest_tag, pending=none"
fi
