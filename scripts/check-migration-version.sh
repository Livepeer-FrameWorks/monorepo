#!/usr/bin/env bash
set -eo pipefail

# Keep the shipped Git state, the one pending release, and migration paths in
# one lifecycle:
#
#   development: latest tag < one catalog release == every pending migration
#   release:     latest tag == catalog release; no migration is "future"
#   afterwards:  code-only work is allowed without inventing another release
#
# A release tag is intentionally NOT required before its code lands. The
# catalog is the explicit declaration of the single release being prepared;
# the tag records that the release actually shipped.

catalog_path="cli/internal/releases/catalog.yaml"
migration_re='^pkg/database/(sql/migrations|sql/clickhouse/migrations)/[^/]+/(v[0-9]+\.[0-9]+\.[0-9]+)/'
diff_base=""
worktree=false
path_args=()

# v0.2.96 contract/001 shipped with plain indexes on CITEXT, which YugabyteDB rejects. Production's
# attempt failed before the migration entered the ledger, so the corrected release artifact embeds
# the portable, non-destructive functional-index form and production records this replacement hash.
# Keep this exception byte-exact: it authorizes the audited repair, not subsequent edits to shipped
# migration history.
approved_repair_path="pkg/database/sql/migrations/commodore/v0.2.96/contract/001_dvr_chapter_playback_index_realign.sql"
approved_repair_sha256="f2bdab1cc45feb7062294b16aeeb327be48720a7dd0df052e4c94d525c424af7"

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

violations=()
pending_version=""
if [ "${#pending_versions[@]}" -gt 1 ]; then
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
    if [ -z "$pending_version" ]; then
      violations+=("  $file  (migration $version is newer than shipped tag $latest_tag, but the catalog declares no pending release)")
    elif [ "$version" != "$pending_version" ]; then
      violations+=("  $file  (migration $version does not target the one pending catalog release $pending_version)")
    fi
  fi
}

# Inventory the complete checked-out tree, not only the current diff. This is
# what prevents old v0.2.97/v0.3.0 buckets from hiding beside v0.2.96.
while IFS= read -r file; do
  check_pending_path "$file"
done < <(find pkg/database/sql/migrations pkg/database/sql/clickhouse/migrations -type f -name '*.sql' -print | LC_ALL=C sort)

candidate_sha256() {
  local file=$1
  if [ -n "$diff_base" ]; then
    git show "HEAD:$file" 2>/dev/null | shasum -a 256 | awk '{print $1}'
  elif [ "$worktree" = true ]; then
    shasum -a 256 "$file" 2>/dev/null | awk '{print $1}'
  else
    git show ":$file" 2>/dev/null | shasum -a 256 | awk '{print $1}'
  fi
}

is_approved_shipped_repair() {
  local file=$1
  [ "$file" = "$approved_repair_path" ] || return 1
  [ "$(candidate_sha256 "$file")" = "$approved_repair_sha256" ]
}

check_added_or_modified() {
  local file=$1
  if [[ ! "$file" =~ $migration_re ]]; then
    return
  fi

  local version=${BASH_REMATCH[2]}
  if ! version_gt "$version" "$latest_tag"; then
    if is_approved_shipped_repair "$file"; then
      return
    fi
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

if [ -n "$diff_base" ]; then
  git cat-file -e "$diff_base^{commit}" 2>/dev/null || {
    echo "ERROR: release-state diff base is not available: $diff_base" >&2
    exit 1
  }
  check_name_status_stream < <(git diff --name-status -z "$diff_base"...HEAD -- "${path_args[@]}")
elif [ "$worktree" = true ]; then
  check_name_status_stream < <(git diff --name-status -z HEAD -- "${path_args[@]}")
  while IFS= read -r -d '' file; do
    check_added_or_modified "$file"
  done < <(git ls-files --others --exclude-standard -z -- "${path_args[@]}")
else
  # Pre-commit mode: lefthook supplies staged migration paths as path_args.
  check_name_status_stream < <(git diff --cached --name-status -z -- "${path_args[@]}")
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
  echo "Code and migrations for one release may land before its tag, but they must all target the single release declared in $catalog_path." >&2
  exit 1
fi

if [ -n "$pending_version" ]; then
  echo "Release state valid: shipped=$latest_tag, pending=$pending_version"
else
  echo "Release state valid: shipped=$latest_tag, pending=none"
fi
