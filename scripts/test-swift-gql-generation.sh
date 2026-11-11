#!/usr/bin/env bash
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/scripts" "$fixture/pkg/graphql/operations/queries" "$fixture/app_mac/Sources/Gateway" "$fixture/bin"
cp "$repo/scripts/generate-swift-gql.sh" "$fixture/scripts/"
printf 'query Example { serverInfo { version } }\n' >"$fixture/pkg/graphql/operations/queries/Example.gql"
output="$fixture/app_mac/Sources/Gateway/GeneratedQueries.swift"
printf 'complete previous generation\n' >"$output"
cat >"$fixture/bin/sed" <<'SH'
#!/usr/bin/env bash
exit 1
SH
chmod +x "$fixture/bin/sed"
if PATH="$fixture/bin:$PATH" bash "$fixture/scripts/generate-swift-gql.sh" >/dev/null 2>&1; then
  echo 'FAIL: generator ignored an operation read failure' >&2
  exit 1
fi
if [ "$(cat "$output")" != 'complete previous generation' ]; then
  echo 'FAIL: failed generation exposed a partial Swift file to concurrent readers' >&2
  exit 1
fi
bash "$fixture/scripts/generate-swift-gql.sh" >/dev/null
if ! grep -q 'static let Example' "$output" || [ "$(tail -1 "$output")" != '}' ]; then
  echo 'FAIL: successful generation did not publish a complete Swift file' >&2
  exit 1
fi
echo 'Swift operation generation publishes complete files only'
