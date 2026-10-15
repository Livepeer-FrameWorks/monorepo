#!/usr/bin/env bash
# stack-scenario: manual
# The object store applies copies to one key in order and finishes a copy its
# client gave up on. A live stream projects every thumbnail publication to the
# same served key, so once one copy to that key stalls, the copies of later
# publications and their retries must not pile up behind it: the key has to keep
# up with the stream again once the stall clears (staging: one served
# sprite.jpg failed 159 times over 50 minutes while the stream kept publishing).
. "$(dirname "$0")/../lib.sh"

HOLD=${STACK_SERVED_KEY_HOLD:-45}
SERVICE=${STACK_SERVED_KEY_SERVICE:-3}
WINDOW=${STACK_SERVED_KEY_WINDOW:-150}
need ffmpeg jq curl python3 psql || finish
[ -n "${STACK_THUMBNAIL_FAULT_URL:-}" ] && [ -n "${SERVICE_TOKEN:-}" ] || {
  blocked 'a tenant-isolated S3 fault fixture and its control credential are required'
  finish
}
fault() {
  curl -fsS -m 10 -H "Authorization: Bearer $SERVICE_TOKEN" \
    -H 'Content-Type: application/json' "$@" "$STACK_THUMBNAIL_FAULT_URL"
}
fault -d '{"copy_order":{"asset":""}}' >/dev/null || { blocked 'thumbnail fault fixture is unreachable'; finish; }
S=$(create_stream "stack-served-key-order-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] && [ -n "$UUID" ] || { fail "createStream: $S"; finish; }
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 640x360 15 $((WINDOW + 240)) metadata)
trap 'fault -d '\''{"copy_order":{"asset":""}}'\'' >/dev/null 2>&1; kill "$PUB" 2>/dev/null; delete_stream "$SID"' EXIT
eventually 90 'stream live' media_at "${FOGHORN_A_URLS%% *}" "$PB"

projected_since() { # projected_since <unix seconds>: a publication of this stream reached its served keys after then
  local n
  n=$(pg "$FOGHORN_A_DB" "SELECT count(*) FROM foghorn.thumbnail_task_assignment
    WHERE asset_key='$UUID' AND deterministic_projected_at > to_timestamp($1)")
  [ "${n:-0}" -gt 0 ] 2>/dev/null
}
eventually 120 'live thumbnails projected to the served keys' projected_since 0

armed_at=$(date +%s)
arm_copy_order() {
  fault -d "$(jq -cn --arg a "$UUID" --argjson h "$HOLD" --argjson s "$SERVICE" \
    '{copy_order:{asset:$a,file:"sprite.jpg",hold:$h,service:$s}}')" >/dev/null
}
check 'served sprite.jpg copies armed: first one held, each one slow' arm_copy_order
log "stream keeps publishing for ${WINDOW}s; the first served sprite copy is held ${HOLD}s, each copy takes ${SERVICE}s"
sleep "$WINDOW"
fault >"$STACK_STATE_DIR/served-key-order.json" || fail 'read the copy-order fixture'
echo "    copy order: $(jq -c '.copy_order | {max_queued, queued, copies: (.copies | length)}' "$STACK_STATE_DIR/served-key-order.json")"

# One copy at a time per served key, none retried after a timeout: copies the
# stall strands accumulate at most once per deterministic copy timeout (30 s).
check "copies queued on the served key at once stay bounded by the stall, not by publications" \
  jq -e --argjson bound $((HOLD / 30 + 2)) '.copy_order.max_queued <= $bound' "$STACK_STATE_DIR/served-key-order.json"
check 'the served key drained once the stall cleared' \
  jq -e '.copy_order.queued <= 1' "$STACK_STATE_DIR/served-key-order.json"
check 'copies issued in the last 60 s landed without waiting behind a backlog' \
  jq -e '[.copy_order | .now as $now | .copies[] | select(.queued > $now - 60 and has("landed"))
    | (.started - .queued)] | length > 0 and max < 10' "$STACK_STATE_DIR/served-key-order.json"
eventually 60 'a publication after the stall reached the served keys' projected_since $((armed_at + HOLD + 5))
finish
