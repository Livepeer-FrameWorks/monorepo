#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, inspects isolated cell configuration and durable session state
# Placement admits the source in B. Gateway discovery must follow that job's
# owner, while playback is free to federate through A.
. "$(dirname "$0")/../lib.sh"

need ffmpeg ffprobe jq curl python3 timeout || finish
since=$(utc_now)
sid="" pub=""
cleanup() {
  [ -z "$pub" ] || kill "$pub" 2>/dev/null || true
  [ -z "$sid" ] || delete_stream "$sid"
}
trap cleanup EXIT

cell=$(stack_exec foghorn-b sh -c 'printf "%s" "${MEDIA_AUTHORITY_CELL_ID:-$CLUSTER_ID}"')
[ -n "$cell" ] || { fail 'cell B has no configured job owner'; finish; }
eventually 90 'cell B Livepeer gateway is discoverable' gateway_discoverable stack-livepeer-gateway-b
s=$(create_stream "stack-job-cell-$(date +%s)" false)
sid=$(echo "$s" | jq -r '.id // empty')
uuid=$(echo "$s" | jq -r '.streamId // empty')
pb=$(echo "$s" | jq -r '.playbackId // empty')
key=$(echo "$s" | jq -r '.streamKey // empty')
[ -n "$sid" ] && [ -n "$uuid" ] || { fail 'create isolated job-cell stream'; finish; }
internal=$(internal_name_of "$uuid")
pub=$(publish "$EDGE_B_RTMP" "$key" 854x480 15 180)

job_uses_owner_gateway() {
  local config
  config=$(pg "$FOGHORN_B_DB" "SELECT processes_json FROM foghorn.ingest_sessions
    WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name='$internal'
      AND ended_at IS NULL AND projection_state='active'
    ORDER BY started_at DESC LIMIT 1") || return 1
  echo "$config" | jq -e --arg cell "$cell" '
    [.[] | select(.process == "Livepeer")] |
    length > 0 and all(.[];
      (.frameworks_gateway_cluster_ids | length > 0) and
      all(.frameworks_gateway_cluster_ids[]; . == $cell))' >/dev/null
}
eventually 60 'admitted B session uses only gateways authorized by B' job_uses_owner_gateway
eventually 90 'B gateway transcoded this exact stream' log_has "$since" \
  "live\\+$internal.*Successfully transcoded segment" livepeer-gateway-b

decode_360p_through_a() {
  local location variant dimensions
  location=$(location_at "${FOGHORN_A_URLS%% *}" "$pb") || return 1
  [ -n "$location" ] || return 1
  edge_a_location "$location" || return 1
  variant=$(python3 - "$location" <<'PY'
import re, sys, urllib.parse, urllib.request
with urllib.request.urlopen(sys.argv[1], timeout=8) as response:
    base = response.url
    lines = response.read().decode().splitlines()
for i, line in enumerate(lines):
    if not line.startswith('#EXT-X-STREAM-INF:'):
        continue
    if not re.search(r'RESOLUTION=\d+x360(?:,|$)', line):
        continue
    child = next((s.strip() for s in lines[i+1:] if s.strip() and not s.startswith('#')), '')
    if child:
        print(urllib.parse.urljoin(base, child))
        break
else:
    sys.exit(1)
PY
  ) || return 1
  dimensions=$(timeout 15 ffprobe -v error -select_streams v:0 -show_entries stream=height \
    -of json "$variant" 2>/dev/null | jq -er '.streams[0].height') || return 1
  [ "$dimensions" = 360 ] || return 1
  timeout 18 ffmpeg -hide_banner -loglevel error -i "$variant" -frames:v 3 -an -f null - \
    >"$STACK_STATE_DIR/job-cell-decode.log" 2>&1
}
eventually 90 'distinct 360p Livepeer rendition decodes through cell A' decode_360p_through_a
check 'publisher remains connected without an auth-induced reconnect' kill -0 "$pub"
every_replica 'job-cell stream plays across both cells' "$pb" media_at
kill "$pub" 2>/dev/null || true
pub=""
session_closed() {
  [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.ingest_sessions
    WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name='$internal'
      AND ended_at IS NULL AND projection_state='active'")" = 0 ]
}
eventually 90 'owned session closes after the publisher stops' session_closed
finish
