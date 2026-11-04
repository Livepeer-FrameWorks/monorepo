#!/usr/bin/env bash
# Scoped MCP tokens must be revoked even when the smoke runner is terminated.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/fw-mcp-smoke-test.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/sdk" "$work/bin"
cp "$here/sdk/mcp-smoke.sh" "$work/sdk/"
cat >"$work/lib.sh" <<'EOF'
set -uo pipefail
BRIDGE_URL=http://fixture.invalid
need() { return 0; }
check() { shift; "$@"; }
fail() { echo "$*" >&2; }
json_has() { printf '%s' "$1" | jq -e "$2" >/dev/null; }
gql_session() {
  if [[ "$1" == *createDeveloperToken* ]]; then
    echo '{"data":{"createDeveloperToken":{"id":"scoped-token","tokenValue":"secret"}}}'
  else
    echo revoked >>"$FIXTURE_DIR/revocations"
    echo '{"data":{"revokeDeveloperToken":{"__typename":"DeleteSuccess"}}}'
  fi
}
finish() { exit 0; }
EOF
cat >"$work/bin/node" <<'EOF'
#!/usr/bin/env bash
if [ "$FIXTURE_MODE" = terminate ]; then kill -TERM "$PPID"; fi
exit 1
EOF
chmod +x "$work/bin/node"
failures=0
for mode in failure terminate; do
  : >"$work/revocations"
  PATH="$work/bin:$PATH" FIXTURE_DIR="$work" FIXTURE_MODE="$mode" bash "$work/sdk/mcp-smoke.sh" >/dev/null 2>&1
  if [ "$(wc -l <"$work/revocations" | tr -d ' ')" = 1 ]; then
    printf 'ok    scoped token revoked once after %s\n' "$mode"
  else
    printf 'FAIL  scoped token was not revoked once after %s\n' "$mode"
    failures=$((failures+1))
  fi
done
exit "$failures"
