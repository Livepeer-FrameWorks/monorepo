#!/usr/bin/env bash
# stack-scenario: manual
# Two response-header stalls on one thumbnail key reproduce the historic
# sequential-batch deadline. The other files must land independently, and
# a retry of the stalled file must complete the same publication attempt.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 psql || finish
[ -n "${STACK_THUMBNAIL_FAULT_URL:-}" ] && [ -n "${SERVICE_TOKEN:-}" ] || {
  blocked 'a tenant-isolated S3 fault fixture and its control credential are required'
  finish
}
fault() {
  curl -fsS -m 10 -H "Authorization: Bearer $SERVICE_TOKEN" \
    -H 'Content-Type: application/json' "$@" "$STACK_THUMBNAIL_FAULT_URL"
}
fault -d '{"artifact":""}' >/dev/null || { blocked 'thumbnail fault fixture is unreachable'; finish; }
S=$(create_stream "stack-thumbnail-stall-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 640x360 15 300 metadata)
trap 'fault -d '\''{"artifact":""}'\'' >/dev/null 2>&1; kill "$PUB" 2>/dev/null; delete_stream "$SID"' EXIT
eventually 90 'thumbnail fixture source live' media_at "${FOGHORN_A_URLS%% *}" "$PB"
sleep 25
start=$(($(date +%s) - 20))
R=$(gql 'mutation($i:CreateClipInput!){createClip(input:$i){__typename ... on Clip{id clipHash playbackId} ... on ValidationError{message}}}' \
  "$(jq -cn --arg s "$SID" --argjson start "$start" '{i:{streamId:$s,title:"thumbnail stall",mode:"DURATION",startUnix:$start,duration:10}}')")
CID=$(echo "$R" | jq -r '.data.createClip.id // empty')
CHASH=$(echo "$R" | jq -r '.data.createClip.clipHash // empty')
CPB=$(echo "$R" | jq -r '.data.createClip.playbackId // empty')
[ -n "$CID" ] && [ -n "$CHASH" ] || { fail "createClip: $R"; finish; }
check 'thumbnail fault armed for this artifact only' fault -d "$(jq -cn --arg h "$CHASH" '{artifact:$h}')"

stalls_seen() {
  fault >"$STACK_STATE_DIR/thumbnail-fault.json" || return 2
  jq -e '[.events[] | select(.event == "stalled" and .file == "sprite.jpg")] | length == 2' \
    "$STACK_STATE_DIR/thumbnail-fault.json" >/dev/null
}
eventually 180 'two real sprite PUT attempts stalled awaiting response headers' stalls_seen
uploads_completed() {
  fault >"$STACK_STATE_DIR/thumbnail-fault.json" || return 2
  jq -e '[.events[] | select(.event == "accepted") | .file] | unique | sort == ["poster.jpg","sprite.jpg","sprite.vtt"]' \
    "$STACK_STATE_DIR/thumbnail-fault.json" >/dev/null
}
eventually 45 'all thumbnail files accepted by real S3 after retry' uploads_completed
check 'siblings completed before the retried sprite and within its deadline' \
  python3 - "$STACK_STATE_DIR/thumbnail-fault.json" <<'PY'
import json,sys
events=json.load(open(sys.argv[1]))['events']
stalled=[e['at'] for e in events if e['event']=='stalled' and e['file']=='sprite.jpg']
accepted={e['file']:e['at'] for e in events if e['event']=='accepted'}
assert len(stalled)==2, events
assert accepted['sprite.jpg']-stalled[0] < 30, events
for name in ('poster.jpg','sprite.vtt'):
    assert accepted[name] < accepted['sprite.jpg'], events
    assert accepted[name]-stalled[0] < 15, events
PY
published() {
  local row
  row=$(pg "$FOGHORN_A_DB" "SELECT status,has_thumbnails,sync_status FROM foghorn.artifacts
    WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$CHASH'")
  echo "    artifact publication: $row"
  [ "$row" = 'ready|t|synced' ]
}
eventually 180 'retried thumbnail attempt published alongside the ready clip' published
every_replica 'clip with retried thumbnails plays' "$CPB" media_at
finish
