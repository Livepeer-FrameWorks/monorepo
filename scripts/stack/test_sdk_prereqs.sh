#!/usr/bin/env bash
# Unit checks for scripts/stack/sdk-prereqs.sh against stubbed python3/pnpm, and
# for a deployed-target run preparing what scenario 07's SDK smokes need instead
# of reporting them BLOCKED.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/fw-stack-sdk-test.XXXXXX")
trap 'rm -rf "$work"' EXIT
failures=0
expect() { # expect <description> <command...>
  local what=$1
  shift
  if "$@"; then printf 'ok    %s\n' "$what"; else printf 'FAIL  %s\n' "$what"; failures=$((failures + 1)); fi
}

mkdir -p "$work/bin" "$work/repo/sdk_python" "$work/repo/npm_api/src" "$work/repo/npm_api/dist"
cat >"$work/bin/python3" <<'EOF'
#!/usr/bin/env bash
# Stub: "python3 -m venv <dir>" makes a venv whose pip records its arguments.
[ "$1 $2" = "-m venv" ] || exit 1
mkdir -p "$3/bin"
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "$*" >>"%s"\n' "$FIXTURE_DIR/pip-calls" >"$3/bin/pip"
chmod +x "$3/bin/pip"
EOF
cat >"$work/bin/pnpm" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FIXTURE_DIR/pnpm-calls"
EOF
chmod +x "$work/bin/python3" "$work/bin/pnpm"

run_helper() { # run_helper <function> <args...>
  (
    export PATH="$work/bin:$PATH" FIXTURE_DIR="$work"
    # shellcheck source=scripts/stack/sdk-prereqs.sh
    . "$here/sdk-prereqs.sh"
    "$@"
  )
}

venv_installs_declared_deps() {
  : >"$work/pip-calls"
  local bin
  bin=$(run_helper python_sdk_venv "$work/repo" "$work/state/sdk-venv" 2>/dev/null) || return 1
  [ "$bin" = "$work/state/sdk-venv/bin" ] && grep -q -- "$work/repo/sdk_python\$" "$work/pip-calls"
}
expect "the Python SDK venv installs the dependencies sdk_python/pyproject.toml declares" venv_installs_declared_deps

fresh_dist_is_kept() {
  : >"$work/pnpm-calls"
  touch "$work/repo/npm_api/src/index.ts"
  sleep 1
  touch "$work/repo/npm_api/dist/index.js"
  run_helper build_npm_api_dist "$work/repo" >/dev/null
  [ ! -s "$work/pnpm-calls" ]
}
expect "a dist newer than every npm_api source is not rebuilt" fresh_dist_is_kept

stale_dist_is_built() {
  : >"$work/pnpm-calls"
  sleep 1
  touch "$work/repo/npm_api/src/index.ts"
  run_helper build_npm_api_dist "$work/repo" >/dev/null
  grep -q 'build' "$work/pnpm-calls"
}
expect "a source newer than the dist rebuilds it" stale_dist_is_built

# A deployed-target run executes the smokes on the operator's machine, which has
# no stack-runner image: it must prepare both itself and drop the venv at exit.
target_prepares_sdks() {
  grep -q 'sdk-prereqs.sh' "$here/verify-target.sh" &&
    grep -q 'build_npm_api_dist' "$here/verify-target.sh" &&
    grep -q 'python_sdk_venv' "$here/verify-target.sh" &&
    grep -q 'rm -rf "$sdk_venv"' "$here/verify-target.sh"
}
expect "a deployed-target run prepares the SDK smokes' prerequisites and removes its venv" target_prepares_sdks

[ "$failures" -eq 0 ] && echo "sdk prerequisite checks: ok"
exit "$failures"
