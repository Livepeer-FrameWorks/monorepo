#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, restarts and kills the Mist angel on the isolated edge
# tenant: qa
. "$(dirname "$0")/../lib.sh"
need ffmpeg jq python3 docker || finish
[ "${STACK_TARGET:-stack}" != staging ] || { blocked 'requires an isolated edge'; finish; }
SID='' PUB=''
cleanup() {
  [ -z "$PUB" ] || kill "$PUB" 2>/dev/null
  [ -z "$SID" ] || delete_stream "$SID"
}
trap cleanup EXIT
S=$(create_stream "stack-edge-lifecycle-$(date +%s)" false OFF)
SID=$(jq -r '.id // empty' <<<"$S")
KEY=$(jq -r '.streamKey // empty' <<<"$S")
UUID=$(jq -r '.streamId // empty' <<<"$S")
[ -n "$SID" ] && [ -n "$KEY" ] && [ -n "$UUID" ] || { fail 'create edge lifecycle stream'; finish; }
IN=$(internal_name_of "$UUID")
for action in reload restart crash; do
  PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$KEY" 640x360 24 1800) || { fail "admit $action lifecycle publisher"; finish; }
  if python3 "$STACK_LIB_DIR/edge_group_lifecycle.py" "$(container_of "$STACK_EDGE_A_SERVICE")" "$IN" "$action"; then
    pass "Mist $action preserves or cleans its process group and sockets"
  else
    fail "edge process-group lifecycle: $action"
  fi
  kill "$PUB" 2>/dev/null || true
  PUB=''
done
finish
