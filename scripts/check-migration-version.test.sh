#!/usr/bin/env bash
set -eo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
guard="$script_dir/check-migration-version.sh"
test_root=$(mktemp -d "${TMPDIR:-/tmp}/frameworks-release-state.XXXXXX")
trap 'rm -rf "$test_root"' EXIT

cd "$test_root"
git init -q
git config user.email release-state-test@example.invalid
git config user.name release-state-test
mkdir -p cli/internal/releases
mkdir -p pkg/database/sql/migrations/commodore/v0.2.95/expand
mkdir -p pkg/database/sql/clickhouse/migrations/periscope

write_empty_catalog() {
  printf '%s\n' 'schema_migration_floor: v0.2.95' 'releases: []' > cli/internal/releases/catalog.yaml
}

write_pending_catalog() {
  printf '%s\n' \
    'schema_migration_floor: v0.2.95' \
    'releases:' \
    '  - version: v0.2.96' > cli/internal/releases/catalog.yaml
}

expect_pass() {
  local label=$1
  shift
  if ! "$guard" "$@" >"$test_root/output" 2>&1; then
    echo "FAIL: $label should pass" >&2
    sed 's/^/  /' "$test_root/output" >&2
    exit 1
  fi
}

expect_fail() {
  local label=$1
  shift
  if "$guard" "$@" >"$test_root/output" 2>&1; then
    echo "FAIL: $label should fail" >&2
    exit 1
  fi
}

write_empty_catalog
printf '%s\n' 'SELECT 1;' > pkg/database/sql/migrations/commodore/v0.2.95/expand/001_released.sql
git add .
git commit -qm 'released state'
git tag v0.2.95

write_pending_catalog
mkdir -p pkg/database/sql/migrations/commodore/v0.2.96/expand
printf '%s\n' 'SELECT 2;' > pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
expect_pass 'code and migrations may precede the release tag' --worktree

git add .
git commit -qm 'prepare v0.2.96'
pending_commit=$(git rev-parse HEAD)
expect_pass 'committed pending release matches its catalog' --diff-base v0.2.95
printf '%s\n' 'schema_migration_floor: v0.2.95' 'releases:' '  - version: v0.2.96' '  - version: v0.2.97' > cli/internal/releases/catalog.yaml
expect_fail 'two pending releases without a reachable RC are rejected' --worktree
write_pending_catalog
git tag v0.2.96-rc1
expect_pass 'an RC keeps the base release pending' --worktree
printf '%s\n' 'SELECT 22;' > pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
expect_fail 'an RC migration is already immutable' --worktree
git add pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
expect_fail 'the commit hook protects RC migrations too' pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
git show HEAD:pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql > pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
git add pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
printf '%s\n' 'SELECT 3;' > pkg/database/sql/migrations/commodore/v0.2.96/expand/002_candidate_fix.sql
expect_pass 'the next RC may append a migration in the same release bucket' --worktree
rm pkg/database/sql/migrations/commodore/v0.2.96/expand/002_candidate_fix.sql
expect_pass 'a missing diff base falls back to the latest shipped tag' --diff-base deadbeefdeadbeefdeadbeefdeadbeefdeadbeef
if ! grep -q 'shipped migrations are compared against v0.2.95' "$test_root/output"; then
  echo 'FAIL: missing diff base should report its shipped-tag fallback' >&2
  exit 1
fi

mkdir -p pkg/database/sql/migrations/commodore/v0.2.97/expand
printf '%s\n' 'SELECT 3;' > pkg/database/sql/migrations/commodore/v0.2.97/expand/001_too_far.sql
expect_fail 'a migration outside the one pending release is rejected' --worktree
rm pkg/database/sql/migrations/commodore/v0.2.97/expand/001_too_far.sql

printf '%s\n' \
  'schema_migration_floor: v0.2.95' \
  'releases:' \
  '  - version: v0.2.96' \
  '  - version: v0.2.97' > cli/internal/releases/catalog.yaml
printf '%s\n' 'SELECT 4;' > pkg/database/sql/migrations/commodore/v0.2.97/expand/001_next_release.sql
expect_pass 'a reachable RC freezes its release while one successor is prepared' --worktree
rm pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
expect_fail 'preparing the successor cannot delete an RC migration' --worktree
git show HEAD:pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql > pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
printf '%s\n' 'SELECT 5;' > pkg/database/sql/migrations/commodore/v0.2.96/expand/002_late_rc_change.sql
expect_fail 'the frozen RC bucket cannot accept new migrations while preparing its successor' --worktree
rm pkg/database/sql/migrations/commodore/v0.2.96/expand/002_late_rc_change.sql
printf '%s\n' 'SELECT 22;' > pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
expect_fail 'preparing the successor cannot unlock the previous RC migration' --worktree
git show HEAD:pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql > pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
git add .
git commit -qm 'prepare successor to frozen RC'
expect_pass 'committed successor and frozen RC validate against the last final tag' --diff-base v0.2.95
printf '%s\n' 'SELECT 23;' > pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
git add pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
expect_fail 'the commit hook still protects the previous RC while its successor is pending' pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
git show HEAD:pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql > pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
git add pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
printf '%s\n' '  - version: v0.2.98' >> cli/internal/releases/catalog.yaml
expect_fail 'a frozen RC does not allow two successors' --worktree
printf '%s\n' 'schema_migration_floor: v0.2.95' 'releases:' '  - version: v0.2.97' '  - version: v0.2.98' > cli/internal/releases/catalog.yaml
expect_fail 'two releases without a matching reachable RC are rejected' --worktree
rm pkg/database/sql/migrations/commodore/v0.2.97/expand/001_next_release.sql
git show "$pending_commit":cli/internal/releases/catalog.yaml > cli/internal/releases/catalog.yaml
git add .
git commit -qm 'resume single pending RC fixture'

printf '%s\n' 'SELECT 99;' > pkg/database/sql/migrations/commodore/v0.2.95/expand/001_released.sql
expect_fail 'a shipped migration is immutable' --worktree
git show HEAD:pkg/database/sql/migrations/commodore/v0.2.95/expand/001_released.sql > pkg/database/sql/migrations/commodore/v0.2.95/expand/001_released.sql

git tag v0.2.96 "$pending_commit"
expect_pass 'a release tag appearing before CI runs does not reclassify its migrations as mutations' --diff-base v0.2.95
printf '%s\n' 'code-only work' > README.md
expect_pass 'code-only work after a tag needs no future release declaration' --worktree

git add README.md
git commit -qm 'code only after release'

printf '%s\n' 'SELECT 3;' > pkg/database/sql/migrations/commodore/v0.2.96/expand/002_unshipped_feature.sql
git add .
git commit -qm 'feature migration prepared before learning of release tag'
printf '%s\n' 'schema_migration_floor: v0.2.95' 'releases:' '  - version: v0.2.96' '  - version: v0.2.97' > cli/internal/releases/catalog.yaml
git mv pkg/database/sql/migrations/commodore/v0.2.96/expand/002_unshipped_feature.sql pkg/database/sql/migrations/commodore/v0.2.97/expand/001_feature.sql
git add cli/internal/releases/catalog.yaml
expect_pass 'the hook permits moving SQL that was never in the shipped tag' pkg/database/sql/migrations/commodore/v0.2.96/expand/002_unshipped_feature.sql pkg/database/sql/migrations/commodore/v0.2.97/expand/001_feature.sql
expect_pass 'worktree validation uses the final tree when an unshipped migration moves' --worktree
rm pkg/database/sql/migrations/commodore/v0.2.97/expand/001_feature.sql
git add -u
git show HEAD:cli/internal/releases/catalog.yaml > cli/internal/releases/catalog.yaml
git add cli/internal/releases/catalog.yaml
git commit -qm 'remove unshipped fixture'

printf '%s\n' 'SELECT 200;' > pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
git add pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
git commit -qm 'badly mutate shipped migration'
expect_fail 'diff-base mode catches a committed shipped-migration edit' --diff-base v0.2.96
expect_fail 'missing-base fallback catches a committed shipped-migration edit' --diff-base deadbeefdeadbeefdeadbeefdeadbeefdeadbeef

bad_mutation_commit=$(git rev-parse HEAD)
printf '%s\n' 'later code-only work' >> README.md
git add README.md
git commit -qm 'code only after bad migration mutation'
expect_fail 'a later event base cannot hide an earlier shipped-migration edit' --diff-base "$bad_mutation_commit"

git show v0.2.96:pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql > pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
git add pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
git commit -qm 'restore shipped fixture for floor test'
printf '%s\n' \
  'schema_migration_floor: v0.3.0' \
  'releases:' \
  '  - version: v0.3.0' > cli/internal/releases/catalog.yaml
rm pkg/database/sql/migrations/commodore/v0.2.95/expand/001_released.sql
rm pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
expect_pass 'a pending catalog floor may consolidate older shipped migrations' --worktree

printf '%s\n' 'SELECT 201;' > pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
expect_fail 'a floor never permits rewriting shipped migration history' --worktree

echo 'Release-state guard tests passed'
