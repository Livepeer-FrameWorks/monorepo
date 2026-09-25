#!/usr/bin/env bash
# stack-scenario: manual
# Node lifecycle faults (slow and disruptive, so manual only):
#   1. Helmsman restarts with a processing job in flight: the job finishes or is
#      re-dispatched within minutes, never after the 30-min lease.
#   2. Edge auto-update to a newly staged release while a stream is live: the
#      node returns to normal mode (no leftover update fence) and keeps serving.
#   3. Privateer restart while services resolve .internal names.
# Catches: R3 (in-flight jobs orphaned until lease expiry), N3 (update fence
# never lifted, node excluded from later updates), WS3b (Mist API wedged after
# a rolling reload), F12 (.internal leaked to the LAN resolver).
. "$(dirname "$0")/../lib.sh"

EDGE_SERVICE=${STACK_EDGE_A_SERVICE:-edge}
need ffmpeg jq curl || finish

log "1. Helmsman restart with a processing job in flight"
render_mp4 "$STACK_STATE_DIR/vod-restart.mp4" 854x480 15 60 || { fail "render VOD source"; finish; }
V=$(upload_vod "$STACK_STATE_DIR/vod-restart.mp4" "stack restart vod")
VID=$(echo "$V" | jq -r '.id // empty')
VHASH=$(echo "$V" | jq -r '.artifactHash // empty')
if [ -n "$VID" ]; then
  processing() {
    [ "$(pg "$FOGHORN_A_DB" "SELECT status FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH' ORDER BY created_at DESC LIMIT 1")" = processing ]
  }
  if eventually 120 "processing job running on the edge" processing; then
    if stack_exec "$EDGE_SERVICE" sh -c 's6-svc -r /run/service/helmsman 2>/dev/null || pkill -f "/helmsman( |$)"'; then
      pass "Helmsman restarted mid-job"
      terminal() {
        local s
        s=$(vod_status "$VID")
        echo "    status: $s"
        [ "$s" = READY ] && return 0
        [ "$s" = FAILED ] && return 2
        return 1
      }
      eventually 300 "the job completes after the restart (not after the 30-min lease)" terminal
    fi
  fi
else
  fail "upload: $V"
fi

log "2. edge auto-update while live"
if [ -n "${STACK_EDGE_UPDATE_CMD:-}" ]; then
  S=$(create_stream "stack-update-$(date +%s)" false)
  SID=$(echo "$S" | jq -r '.id // empty')
  PB=$(echo "$S" | jq -r '.playbackId // empty')
  PUB=$(publish "$EDGE_A_RTMP" "$(echo "$S" | jq -r '.streamKey')" 640x360 15 600)
  trap 'kill "$PUB" 2>/dev/null; [ -n "${SID:-}" ] && delete_stream "$SID"' EXIT
  eventually 90 "stream live before the update" media_at "${FOGHORN_A_URLS%% *}" "$PB"
  if bash -c "$STACK_EDGE_UPDATE_CMD"; then pass "edge release target staged"; else fail "STACK_EDGE_UPDATE_CMD failed"; fi
  modes_normal() {
    local m
    m=$(gql '{nodesConnection(page:{first:50}){nodes{nodeName nodeType effectiveMode}}}' | jq -r '[.data.nodesConnection.nodes[] | select(.nodeType=="edge") | .effectiveMode] | unique | join(",")')
    echo "    edge modes: $m"
    [ "$m" = NORMAL ]
  }
  eventually 600 "every edge back in NORMAL after the update (no leftover fence)" modes_normal
  check "the live stream still serves media after the update" media_at "${FOGHORN_A_URLS%% *}" "$PB"
else
  blocked "auto-update needs STACK_EDGE_UPDATE_CMD (stages a new Helmsman/Mist release target)"
fi

log "3. Privateer restart while resolving .internal"
if stack_ctl restart privateer 2>/dev/null; then
  leaked=0
  for _ in $(seq 1 40); do
    addr=$(getent hosts quartermaster.internal 2>/dev/null | awk '{print $1}')
    case "$addr" in "" | 10.* | 172.* | 192.168.*) ;; *) leaked=$((leaked + 1)); echo "    quartermaster.internal -> $addr" ;; esac
    sleep 0.5
  done
  check ".internal never resolved outside the mesh during the restart" [ "$leaked" = 0 ]
else
  blocked "the stack has no privateer service; F12 remains a staging check"
fi
finish
