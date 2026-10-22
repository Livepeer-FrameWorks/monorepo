#!/usr/bin/env bash
# What scenario 07's SDK smokes need outside the stack-runner image. Sourced by
# verify.sh (the compose stack builds npm_api on the host) and verify-target.sh
# (a deployed target runs every smoke from the operator's checkout).

# build_npm_api_dist <repo>: the TS SDK smoke imports npm_api's built dist.
build_npm_api_dist() {
  local repo=$1
  if [ -f "$repo/npm_api/dist/index.js" ] && [ -z "$(find "$repo/npm_api/src" -newer "$repo/npm_api/dist/index.js" -print -quit 2>/dev/null)" ]; then
    return 0
  fi
  echo "building npm_api dist for the TS SDK smoke"
  # A fresh checkout has no node_modules; install only what the API package needs.
  if [ ! -d "$repo/npm_api/node_modules" ]; then
    (cd "$repo" && pnpm install --frozen-lockfile --filter '@livepeer-frameworks/api...' 2>&1 | tail -3) ||
      echo "WARN: npm_api dependency install failed"
  fi
  (cd "$repo" && pnpm --filter @livepeer-frameworks/api build 2>&1 | tail -5) ||
    echo "WARN: npm_api build failed; the TS SDK smoke will report BLOCKED"
}

# python_sdk_venv <repo> <dir>: a venv with livepeer_frameworks' runtime
# dependencies as sdk_python/pyproject.toml declares them; the smoke still
# imports the SDK itself from the checkout. Prints the venv's bin directory.
python_sdk_venv() {
  local repo=$1 dir=$2
  python3 -m venv "$dir" >&2 || return 1
  "$dir/bin/pip" install --quiet --disable-pip-version-check "$repo/sdk_python" >&2 || return 1
  printf '%s\n' "$dir/bin"
}
