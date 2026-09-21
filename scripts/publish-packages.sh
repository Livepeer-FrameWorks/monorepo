#!/usr/bin/env bash
# Publish every FrameWorks package whose version is not on its registry yet.
#
#   npm   @livepeer-frameworks/api, then the player and StreamCrafter cores, then their
#         react, svelte and wc wrappers
#   pypi  livepeer-frameworks (sdk_python)
#   go    github.com/Livepeer-FrameWorks/sdk-go, a mirror of sdk_go/ with a v<version> tag
#
# Once npm, PyPI and the Go mirror all serve the SDK version, the monorepo commit is tagged
# sdk-v<version> locally; make verify-api-compat reads that tag once it is pushed.
#
# Usage:
#   scripts/publish-packages.sh [--dry-run] [--only npm|pypi|go]
#
#   --dry-run   Print the registry state and every publish, push and tag the run would do;
#               changes nothing and runs no gates.
#   --only X    Publish to one registry (npm, pypi or go). The gates still run, and the
#               sdk-v tag is still created when all three registries already serve the version.
#
# Release procedure:
#   1. pnpm changeset          describe the change
#   2. pnpm version-packages   bump versions and changelogs; copies the SDK version into Go and Python
#   3. commit, and push the commit to origin master
#   4. scripts/publish-packages.sh
#
# The Go mirror publishes the checked-out commit, which must be contained in origin/master because
# it pins pkg to that commit's pseudo-version and the Go proxy must resolve it from the public
# monorepo. npm and PyPI intentionally publish the current package directories. Every step skips a
# version its registry already has, so re-running after a failure continues where the previous run
# stopped.
#
# Credentials are the owner's own: npm login (prompted once when npm whoami fails), twine's usual
# sources (~/.pypirc, keyring, TWINE_* variables, or its prompt), and git credentials that may
# push to the sdk-go repository (SDK_GO_PUSH_URL overrides the SSH push URL).

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

SDK_GO_READ_URL="https://github.com/Livepeer-FrameWorks/sdk-go.git"
SDK_GO_PUSH_URL="${SDK_GO_PUSH_URL:-git@github.com:Livepeer-FrameWorks/sdk-go.git}"
PYPI_PROJECT="livepeer-frameworks"
PKG_MODULE="github.com/Livepeer-FrameWorks/monorepo/pkg"

# Publish order: every @livepeer-frameworks dependency of a package is listed before it.
NPM_PACKAGES=(
  npm_api
  npm_player/packages/core
  npm_studio/packages/core
  npm_player/packages/react
  npm_player/packages/svelte
  npm_player/packages/wc
  npm_studio/packages/react
  npm_studio/packages/svelte
  npm_studio/packages/wc
)

usage() {
  echo "usage: $0 [--dry-run] [--only npm|pypi|go]" >&2
  exit 2
}

fail() {
  echo "" >&2
  echo "ERROR: $*" >&2
  exit 1
}

DRY_RUN=false
ONLY=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=true ;;
    --only)
      [[ $# -ge 2 ]] || usage
      ONLY="$2"
      shift
      ;;
    --only=*) ONLY="${1#--only=}" ;;
    -h | --help) usage ;;
    *) usage ;;
  esac
  shift
done
case "$ONLY" in
  "" | npm | pypi | go) ;;
  *) fail "--only takes npm, pypi or go, not '$ONLY'" ;;
esac

selected() { [[ -z "$ONLY" || "$ONLY" == "$1" ]]; }

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/publish-packages.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT

for tool in git node pnpm npm go curl make python3 tar; do
  command -v "$tool" >/dev/null || fail "$tool is not installed"
done

pkg_field() { node -p "require('$ROOT/$1/package.json').$2"; }

# ---------------------------------------------------------------------------
# Registry lookups. Each prints yes or no, and stops the run when the registry cannot be read,
# so an outage never reads as "not published".

npm_has() {
  local out
  if out=$(npm view "$1@$2" version 2>"$WORK_DIR/npm-view.err"); then
    [[ "$out" == "$2" ]] && echo yes || echo no
  elif grep -q E404 "$WORK_DIR/npm-view.err"; then
    echo no
  else
    cat "$WORK_DIR/npm-view.err" >&2
    fail "cannot read $1@$2 from npm"
  fi
}

pypi_has() {
  local code
  code=$(curl -sS -o /dev/null -w '%{http_code}' "https://pypi.org/pypi/$PYPI_PROJECT/$1/json") ||
    fail "cannot reach pypi.org"
  case "$code" in
    200) echo yes ;;
    404) echo no ;;
    *) fail "pypi.org answered HTTP $code for $PYPI_PROJECT $1" ;;
  esac
}

# Prints yes, no, or unreachable (the repository is missing or not public).
sdk_go_has_ref() {
  local status=0
  GIT_TERMINAL_PROMPT=0 git ls-remote --exit-code "$SDK_GO_READ_URL" "$1" >/dev/null 2>&1 || status=$?
  case "$status" in
    0) echo yes ;;
    2) echo no ;;
    *) echo unreachable ;;
  esac
}

# Polls a lookup until the registry serves the version; registry reads lag a publish by seconds.
wait_served() {
  local label="$1"
  shift
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    [[ "$("$@")" == yes ]] && return 0
    sleep 15
  done
  fail "$label is still not served 150 seconds after publishing"
}

# ---------------------------------------------------------------------------
# Local checks.

cd "$ROOT"

# Every public workspace package under the published directories is in NPM_PACKAGES, and each
# is listed after the @livepeer-frameworks packages it depends on.
node - "$ROOT" "${NPM_PACKAGES[@]}" <<'EOF' || fail "NPM_PACKAGES in $0 is incomplete or out of dependency order"
const fs = require("fs");
const path = require("path");
const [root, ...dirs] = process.argv.slice(2);
const listed = new Set(dirs);
const seen = new Set();
let ok = true;
for (const dir of dirs) {
  const pkg = require(path.join(root, dir, "package.json"));
  const deps = { ...pkg.dependencies, ...pkg.peerDependencies };
  for (const dep of Object.keys(deps)) {
    if (dep.startsWith("@livepeer-frameworks/") && !seen.has(dep)) {
      console.error(`${pkg.name} depends on ${dep}, which is not listed before it`);
      ok = false;
    }
  }
  seen.add(pkg.name);
}
for (const parent of ["npm_player/packages", "npm_studio/packages"]) {
  for (const name of fs.readdirSync(path.join(root, parent))) {
    const dir = `${parent}/${name}`;
    const file = path.join(root, dir, "package.json");
    if (!fs.existsSync(file) || require(file).private || listed.has(dir)) continue;
    console.error(`${dir} is a public package missing from NPM_PACKAGES`);
    ok = false;
  }
}
process.exit(ok ? 0 : 1);
EOF

VERSION=$(pkg_field npm_api version)
go_version=$(sed -n 's/^const SDKVersion = "\(.*\)"$/\1/p' sdk_go/manifest_gen.go)
py_version=$(sed -n 's/^SDK_VERSION: Final = "\(.*\)"$/\1/p' sdk_python/src/livepeer_frameworks/_generated/manifest.py)
if [[ "$go_version" != "$VERSION" || "$py_version" != "$VERSION" ]]; then
  fail "SDK versions differ (npm_api $VERSION, sdk_go ${go_version:-?}, sdk_python ${py_version:-?}); run make sdk-version-sync (pnpm version-packages runs it) and commit"
fi

SHA=$(git rev-parse HEAD)
blockers=()
pending_changesets=()
for file in .changeset/*.md; do
  [[ -e "$file" && "$(basename "$file")" != README.md ]] && pending_changesets+=("$(basename "$file")")
done
if [[ ${#pending_changesets[@]} -gt 0 ]]; then
  blockers+=("unapplied changesets (${pending_changesets[*]}); run pnpm version-packages and commit")
fi
if ! git merge-base --is-ancestor HEAD refs/remotes/origin/master 2>/dev/null; then
  blockers+=("HEAD ${SHA:0:12} is not in origin/master; push the release commit to master first")
fi

# ---------------------------------------------------------------------------
# Plan.

echo "Release commit: $SHA"
echo "SDK version:    $VERSION (npm_api, sdk_go, sdk_python)"
[[ -n "$ONLY" ]] && echo "Registries:     $ONLY only"
echo ""

npm_todo=()
API_HAS=no
echo "npm:"
for dir in "${NPM_PACKAGES[@]}"; do
  name=$(pkg_field "$dir" name)
  version=$(pkg_field "$dir" version)
  has=$(npm_has "$name" "$version")
  [[ "$dir" == npm_api ]] && API_HAS=$has
  if [[ "$has" == yes ]]; then
    echo "    $name@$version  published"
  else
    latest=$(npm view "$name" version 2>/dev/null || true)
    echo "  * $name@$version  new (npm latest: ${latest:-none})"
    npm_todo+=("$dir")
  fi
done

PYPI_HAS=$(pypi_has "$VERSION")
echo ""
echo "PyPI:"
if [[ "$PYPI_HAS" == yes ]]; then
  echo "    $PYPI_PROJECT $VERSION  published"
else
  echo "  * $PYPI_PROJECT $VERSION  new"
fi

GO_HAS=$(sdk_go_has_ref "refs/tags/v$VERSION")
GO_MAIN=no
echo ""
echo "Go mirror ($SDK_GO_READ_URL):"
case "$GO_HAS" in
  yes) echo "    v$VERSION  tagged" ;;
  no)
    GO_MAIN=$(sdk_go_has_ref refs/heads/main)
    if [[ "$GO_MAIN" == yes ]]; then
      echo "  * v$VERSION  new, fast-forwards main"
    else
      echo "  * v$VERSION  new, first release (main does not exist yet)"
    fi
    ;;
  unreachable)
    echo "  ! cannot read the repository"
    if selected go; then
      blockers+=("github.com/Livepeer-FrameWorks/sdk-go cannot be read anonymously; create it as a public repository")
    fi
    ;;
esac

todo_npm=false todo_pypi=false todo_go=false todo_tag=false
selected npm && [[ ${#npm_todo[@]} -gt 0 ]] && todo_npm=true
selected pypi && [[ "$PYPI_HAS" == no ]] && todo_pypi=true
selected go && [[ "$GO_HAS" == no ]] && todo_go=true

# The SDK version is released once npm, PyPI and the Go mirror all serve it, counting what this
# run publishes.
missing=()
[[ "$API_HAS" == yes ]] || { $todo_npm && [[ "${npm_todo[0]}" == npm_api ]]; } || missing+=(npm)
[[ "$PYPI_HAS" == yes ]] || $todo_pypi || missing+=(pypi)
[[ "$GO_HAS" == yes ]] || $todo_go || missing+=(go)

TAG="sdk-v$VERSION"
tag_local=$(git rev-parse -q --verify "refs/tags/$TAG^{commit}" || true)
tag_origin_status=0
git ls-remote --exit-code --tags origin "refs/tags/$TAG" >/dev/null 2>&1 || tag_origin_status=$?
echo ""
echo "Monorepo tag $TAG:"
if [[ -n "$tag_local" && "$tag_local" != "$SHA" ]]; then
  echo "  ! exists locally on ${tag_local:0:12}, not on HEAD"
  blockers+=("local tag $TAG points at ${tag_local:0:12}, not the release commit ${SHA:0:12}")
elif [[ -n "$tag_local" ]]; then
  echo "    exists locally on HEAD$([[ $tag_origin_status -eq 0 ]] && echo ", pushed" || true)"
elif [[ $tag_origin_status -eq 0 ]]; then
  echo "    exists on origin"
elif [[ ${#missing[@]} -gt 0 ]]; then
  echo "    not created: $VERSION would still be missing from ${missing[*]} after this run"
else
  echo "  * created on HEAD once npm, PyPI and the Go mirror serve $VERSION"
  todo_tag=true
fi

echo ""
if ! $todo_npm && ! $todo_pypi && ! $todo_go && ! $todo_tag; then
  echo "Nothing to publish. Bump versions with pnpm changeset and pnpm version-packages first."
  exit 0
fi

if $DRY_RUN; then
  echo "Dry run. A real run would:"
  echo "  - run make sdk-release-gates and the playback-verifier test (TestSDKPlaybackTokensPassThePlaybackVerifier)"
  if $todo_npm; then
    echo "  - build ${NPM_PACKAGES[*]}"
    for dir in "${npm_todo[@]}"; do
      echo "  - pnpm publish --access public: $(pkg_field "$dir" name)@$(pkg_field "$dir" version)"
    done
  fi
  if $todo_pypi; then
    echo "  - build sdk_python and twine upload $PYPI_PROJECT $VERSION"
  fi
  if $todo_go; then
    base="the split history of sdk_go/"
    [[ "$GO_MAIN" == yes ]] && base="sdk-go main"
    echo "  - commit sdk_go/ at ${SHA:0:12} on top of $base, with pkg pinned to $PKG_MODULE@${SHA:0:12}"
    echo "  - git push --atomic $SDK_GO_PUSH_URL HEAD:refs/heads/main refs/tags/v$VERSION"
  fi
  if $todo_tag; then
    echo "  - git tag $TAG ${SHA:0:12} (you push it: git push origin refs/tags/$TAG)"
  fi
  if [[ ${#blockers[@]} -gt 0 ]]; then
    echo ""
    echo "A real run would refuse now:"
    printf '  - %s\n' "${blockers[@]}"
    exit 1
  fi
  exit 0
fi

if [[ ${#blockers[@]} -gt 0 ]]; then
  printf 'Refusing to publish:\n' >&2
  printf '  - %s\n' "${blockers[@]}" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Gates. A failure publishes nothing.

if $todo_npm || $todo_pypi || $todo_go; then
  echo "=== Release gates ==="
  make sdk-release-gates || fail "make sdk-release-gates failed; nothing was published"
  make test-foghorn GO_TEST_PACKAGES=./internal/triggers/ \
    GO_TEST_FLAGS="-run TestSDKPlaybackTokensPassThePlaybackVerifier" ||
    fail "the SDK playback tokens failed the playback verifier; nothing was published"
  echo ""
fi

# ---------------------------------------------------------------------------
# npm

publish_npm() {
  echo "=== npm ==="
  if ! npm whoami >/dev/null 2>&1; then
    echo "Not logged in to npm. Logging in..."
    npm login
  fi
  echo "Logged in as $(npm whoami)"

  # Wrappers build against their core's dist/, so every package builds, in publish order.
  for dir in "${NPM_PACKAGES[@]}"; do
    echo "Building $dir"
    (cd "$ROOT/$dir" && pnpm run build) || fail "building $dir failed"
  done

  local dir name version
  for dir in "${npm_todo[@]}"; do
    name=$(pkg_field "$dir" name)
    version=$(pkg_field "$dir" version)
    # The cores' workspace:^ range on the API becomes ^$VERSION when packed, so that version
    # must be installable first. It publishes earlier in this run when it is new.
    if [[ -n "$(node -p "const p = require('$ROOT/$dir/package.json'); (p.dependencies || {})['@livepeer-frameworks/api'] || ''")" ]]; then
      [[ "$(npm_has @livepeer-frameworks/api "$VERSION")" == yes ]] ||
        fail "@livepeer-frameworks/api@$VERSION is not on npm; $name@$version would not install (run without --only, or --only npm)"
    fi
    echo "Publishing $name@$version"
    (cd "$ROOT/$dir" && pnpm publish --access public --no-git-checks) ||
      fail "publishing $name@$version failed; re-run to continue from here"
    [[ "$name" == "@livepeer-frameworks/api" ]] &&
      wait_served "@livepeer-frameworks/api@$VERSION on npm" npm_has @livepeer-frameworks/api "$VERSION"
  done
  echo ""
}

# ---------------------------------------------------------------------------
# PyPI

publish_pypi() {
  echo "=== PyPI ==="
  make sdk-py-venv
  local py="$ROOT/sdk_python/.venv/bin/python"
  "$py" -m build "$ROOT/sdk_python" --outdir "$WORK_DIR/dist" || fail "building sdk_python failed"
  "$py" -m twine check --strict "$WORK_DIR"/dist/* || fail "twine check rejected the sdk_python build"
  "$py" -m twine upload "$WORK_DIR"/dist/* ||
    fail "uploading $PYPI_PROJECT $VERSION failed; re-run to continue from here"
  echo ""
}

# ---------------------------------------------------------------------------
# Go mirror. Everything happens in clones under $WORK_DIR; the monorepo is only read.

publish_go() {
  echo "=== Go mirror ==="
  local mirror="$WORK_DIR/sdk-go"
  if [[ "$GO_MAIN" == yes ]]; then
    # The release commit's parent is sdk-go main, so the push is a fast-forward.
    git clone --quiet --single-branch --branch main --no-tags "$SDK_GO_READ_URL" "$mirror" ||
      fail "cannot clone sdk-go main"
    git -C "$mirror" rm -rq --ignore-unmatch -- .
    git -C "$ROOT" archive "$SHA" sdk_go | tar -x -C "$mirror" --strip-components=1
  else
    # First release: start from the history of sdk_go/, split in a throwaway monorepo clone.
    git clone --quiet --no-checkout "$ROOT" "$WORK_DIR/monorepo"
    (cd "$WORK_DIR/monorepo" && git subtree split --quiet --prefix=sdk_go --branch=sdk-go-release "$SHA" >/dev/null) ||
      fail "git subtree split of sdk_go failed"
    git clone --quiet --branch sdk-go-release "$WORK_DIR/monorepo" "$mirror"
  fi

  # The conformance fixtures travel with the module so its tests run in sdk-go.
  mkdir -p "$mirror/testdata"
  git -C "$ROOT" archive "$SHA" sdk_conformance | tar -x -C "$mirror/testdata"

  (
    cd "$mirror"
    # Outside the monorepo, pkg resolves to the released commit's pseudo-version.
    local pkg_version
    pkg_version=$(GOFLAGS=-mod=mod go list -m -f '{{.Version}}' "$PKG_MODULE@$SHA") ||
      fail "the Go proxy cannot resolve $PKG_MODULE@$SHA; the monorepo must be public and the commit pushed"
    go mod edit -dropreplace="$PKG_MODULE" -require="$PKG_MODULE@$pkg_version"
    go mod tidy
    go vet ./... || fail "go vet failed in the sdk-go release tree"
    go test -count=1 ./... || fail "go test failed in the sdk-go release tree"
    git add -A
    git commit --quiet --allow-empty -m "sdk-go v$VERSION (monorepo $SHA)"
    git tag "v$VERSION"
    # Either main advances and the tag is created, or neither changes: a main that moved since
    # the clone rejects the whole push.
    git push --atomic "$SDK_GO_PUSH_URL" HEAD:refs/heads/main "refs/tags/v$VERSION" ||
      fail "pushing sdk-go v$VERSION failed; re-run to rebuild on the current main"
  )
  echo ""
}

# Called from if bodies, not && lists, so set -e stays in force inside each function.
if $todo_npm; then publish_npm; fi
if $todo_pypi; then publish_pypi; fi
if $todo_go; then publish_go; fi

# ---------------------------------------------------------------------------
# Monorepo tag, only once every registry serves the SDK version.

if $todo_tag; then
  echo "=== $TAG ==="
  wait_served "@livepeer-frameworks/api@$VERSION on npm" npm_has @livepeer-frameworks/api "$VERSION"
  wait_served "$PYPI_PROJECT $VERSION on PyPI" pypi_has "$VERSION"
  wait_served "sdk-go v$VERSION" sdk_go_has_ref "refs/tags/v$VERSION"
  git tag "$TAG" "$SHA"
  echo "Tagged $TAG on ${SHA:0:12}. Push it so make verify-api-compat sees the release:"
  echo "  git push origin refs/tags/$TAG"
  echo ""
fi

echo "Done."
