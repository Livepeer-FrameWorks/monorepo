#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, launches a browser container on this stack's private network
# tenant: test
# A normal external-origin HLS browser follows every Foghorn replica's redirect
# and decodes live frames. CORS on a curl response alone cannot prove this path.
. "$(dirname "$0")/../lib.sh"
need docker jq node timeout || finish
REPO=${STACK_REPO:-/repo}
version=$(node -p "require('$REPO/website_marketing/node_modules/playwright/package.json').version") || { blocked 'Playwright workspace dependency missing'; finish; }
image="mcr.microsoft.com/playwright:v${version}-noble"
source_repo=$(docker inspect "$HOSTNAME" --format '{{range .Mounts}}{{if eq .Destination "/repo"}}{{.Source}}{{end}}{{end}}')
network=$(docker inspect "$HOSTNAME" --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}')
[ -n "$source_repo" ] && [ -n "$network" ] || { blocked 'stack runner mount/network discovery failed'; finish; }
# Pull before publishing so image download time cannot extend the encoder run.
docker pull "$image" >/dev/null || { fail 'pinned Playwright image unavailable'; finish; }
S=$(create_stream "stack-external-player-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail 'create external-player stream'; finish; }
PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$KEY" 640x360 15 180) || { fail 'external-player publisher refused'; delete_stream "$SID"; finish; }
container="fw-stack-${STACK_SLOT}-external-player"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true; kill "$PUB" 2>/dev/null || true; delete_stream "$SID"' EXIT
for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
  eventually 30 "stream available through $base" media_at "$base" "$PB"
  check "external browser decodes media through $base" timeout 50 \
    docker run --rm --name "$container" --network "$network" --cpus 2 --memory 1g --shm-size 256m \
      -v "$source_repo:/repo:ro" -w /repo "$image" node scripts/stack/playback-browser.mjs "$base" "$PB"
done
finish
