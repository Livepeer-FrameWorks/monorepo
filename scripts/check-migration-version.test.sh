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
  printf '%s\n' 'releases: []' > cli/internal/releases/catalog.yaml
}

write_pending_catalog() {
  printf '%s\n' \
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

mkdir -p pkg/database/sql/migrations/commodore/v0.2.97/expand
printf '%s\n' 'SELECT 3;' > pkg/database/sql/migrations/commodore/v0.2.97/expand/001_too_far.sql
expect_fail 'a migration outside the one pending release is rejected' --worktree
rm pkg/database/sql/migrations/commodore/v0.2.97/expand/001_too_far.sql

printf '%s\n' \
  'releases:' \
  '  - version: v0.2.96' \
  '  - version: v0.2.97' > cli/internal/releases/catalog.yaml
expect_fail 'multiple unshipped catalog releases are rejected' --worktree
git show HEAD:cli/internal/releases/catalog.yaml > cli/internal/releases/catalog.yaml

printf '%s\n' 'SELECT 99;' > pkg/database/sql/migrations/commodore/v0.2.95/expand/001_released.sql
expect_fail 'a shipped migration is immutable' --worktree
git show HEAD:pkg/database/sql/migrations/commodore/v0.2.95/expand/001_released.sql > pkg/database/sql/migrations/commodore/v0.2.95/expand/001_released.sql

git tag v0.2.96 "$pending_commit"
printf '%s\n' 'code-only work' > README.md
expect_pass 'code-only work after a tag needs no future release declaration' --worktree

git add README.md
git commit -qm 'code only after release'
printf '%s\n' 'SELECT 200;' > pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
git add pkg/database/sql/migrations/commodore/v0.2.96/expand/001_pending.sql
git commit -qm 'badly mutate shipped migration'
expect_fail 'diff-base mode catches a committed shipped-migration edit' --diff-base v0.2.96

echo 'Release-state guard tests passed'
