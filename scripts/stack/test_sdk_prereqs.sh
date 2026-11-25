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

# Scenario 62's browser imports Playwright and hls.js from the mounted checkout;
# a fresh checkout (a remote-dev clone) has no node_modules, so the run installs
# the fixture package's lockfile closure, and only that.
fresh_checkout_installs_browser_deps() {
  : >"$work/pnpm-calls"
  rm -rf "$work/repo/scripts/stack/node_modules"
  run_helper install_stack_browser_deps "$work/repo" >/dev/null &&
    [ "$(cat "$work/pnpm-calls")" = "install --frozen-lockfile --filter frameworks-stack-fixtures" ]
}
expect "a checkout without the browser fixture's packages installs only that package's closure" fresh_checkout_installs_browser_deps

installed_browser_deps_are_kept() {
  : >"$work/pnpm-calls"
  mkdir -p "$work/repo/scripts/stack/node_modules/playwright" "$work/repo/scripts/stack/node_modules/hls.js"
  echo '{}' >"$work/repo/scripts/stack/node_modules/playwright/package.json"
  echo '{}' >"$work/repo/scripts/stack/node_modules/hls.js/package.json"
  run_helper install_stack_browser_deps "$work/repo" >/dev/null && [ ! -s "$work/pnpm-calls" ]
}
expect "installed browser fixture packages are not reinstalled" installed_browser_deps_are_kept

# The fixture package is the one the run installs and the one the browser and
# scenario 62 resolve from; the marketing site's node_modules is never assumed.
browser_resolves_from_fixture_package() {
  local repo_root
  repo_root=$(cd "$here/../.." && pwd)
  [ "$(node -p "require('$here/package.json').name" 2>/dev/null)" = frameworks-stack-fixtures ] &&
    grep -qx '  - scripts/stack' "$repo_root/pnpm-workspace.yaml" &&
    grep -q '^  scripts/stack:$' "$repo_root/pnpm-lock.yaml" &&
    grep -q 'install_stack_browser_deps "$stack_repo_root"' "$here/verify.sh" &&
    grep -q 'scripts/stack/node_modules/playwright/package.json' "$here/scenarios/62-external-player.sh" &&
    ! grep -q 'website_marketing\|npm_player' "$here/playback-browser.mjs" "$here/test_playback_browser_context.mjs" "$here/scenarios/62-external-player.sh"
}
expect "verify.sh installs the fixture package scenario 62's browser resolves Playwright and hls.js from" browser_resolves_from_fixture_package

# A deployed-target run executes the smokes on the operator's machine, which has
# no stack-runner image: it must prepare both itself and drop the venv at exit.
target_prepares_sdks() {
  grep -q 'sdk-prereqs.sh' "$here/verify-target.sh" &&
    grep -q 'build_npm_api_dist' "$here/verify-target.sh" &&
    grep -q 'python_sdk_venv' "$here/verify-target.sh" &&
    grep -q 'rm -rf "$sdk_venv"' "$here/verify-target.sh"
}
expect "a deployed-target run prepares the SDK smokes' prerequisites and removes its venv" target_prepares_sdks

# Scenario 37's WebSocket probes import websockets, which the same venv carries.
target_prepares_ws_probes() { grep -q "grep -qE '/(07|37)-'" "$here/verify-target.sh"; }
expect "a deployed-target run running scenario 37 prepares the venv its WebSocket probes need" target_prepares_ws_probes

# Scenario 37 signs out every session of the operator account the run signed
# in with; the exit cleanup must sign in again to revoke the run's token.
cleanup_signs_in_again() {
  local fn
  fn=$(sed -n '/^operator_session_jwt() {/,/^}/p' "$here/verify-target.sh")
  [ -n "$fn" ] && grep -q 'refresh_session_token' <<<"$fn" && grep -q 'operator_login' <<<"$fn" &&
    [ "$(grep -c 'jwt=$(operator_session_jwt)' "$here/verify-target.sh")" -ge 2 ] &&
    ! grep -q 'jwt=$(refresh_session_token "$jar"' "$here/verify-target.sh"
}
expect "the run's exit cleanup signs in again when the operator session was signed out" cleanup_signs_in_again

[ "$failures" -eq 0 ] && echo "sdk prerequisite checks: ok"
exit "$failures"
