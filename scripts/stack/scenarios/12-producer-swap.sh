#!/usr/bin/env bash
# stack-scenario: default
# A VOD whose Livepeer gateway dies after the processing output has started:
# the local fallback replaces the Livepeer producer after the recording header
# froze its tracks. That attempt ends PROCESS_TRACKS_CHANGED, Helmsman reports
# it retryable, and Foghorn re-dispatches the job on its persisted local ladder
# to READY at full length.
# Catches: N37 (producer swap mid-job ended the VOD in an EBML
# "track not declared" abort and a terminal FAILED).
. "$(dirname "$0")/../lib.sh"

GATEWAY=${STACK_LIVEPEER_GATEWAY_A:-livepeer-gateway-a}
LEN=${STACK_SWAP_VOD_SECONDS:-90}
need ffmpeg jq curl python3 || finish
trap 'stack_ctl start "$GATEWAY" >/dev/null 2>&1' EXIT

render_mp4 "$STACK_STATE_DIR/vod480-swap.mp4" 854x480 15 "$LEN" || { fail "render VOD source"; finish; }
eventually 90 "cell A Livepeer gateway discoverable" gateway_discoverable stack-livepeer-gateway-a

job_row() { # job_row <hash>: status|retry_count|processes_json|error of the newest job
  pg "$FOGHORN_A_DB" "SELECT status, retry_count, COALESCE(processes_json,'[]'), COALESCE(error_message,'')
    FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1' ORDER BY created_at DESC LIMIT 1"
}

V=$(upload_vod "$STACK_STATE_DIR/vod480-swap.mp4" "stack producer swap vod")
VID=$(echo "$V" | jq -r '.id // empty')
VHASH=$(echo "$V" | jq -r '.artifactHash // empty')
VPB=$(echo "$V" | jq -r '.playbackId // empty')
[ -n "$VID" ] || { fail "upload: $V"; finish; }

log "wait for the first processed output, then kill the gateway"
output_started() {
  local ms
  ms=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(progress_last_ms,0) FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH' ORDER BY created_at DESC LIMIT 1")
  echo "    progress_last_ms: ${ms:-0}"
  [ "${ms:-0}" -gt 0 ]
}
eventually 180 "processing output reports its first segment" output_started
P=$(job_row "$VHASH" | cut -d'|' -f3)
check "the job started on Livepeer" json_has "$P" 'map(.process) | index("Livepeer") != null'
stack_ctl stop "$GATEWAY" || { fail "stop $GATEWAY"; finish; }

ready_or_failed() {
  local s
  s=$(vod_status "$VID")
  echo "    status: $s | job: $(job_row "$VHASH" | cut -d'|' -f1,2,4 | cut -c1-160)"
  [ "$s" = READY ] && return 0
  [ "$s" = FAILED ] && return 2
  return 1
}
eventually 600 "VOD reaches READY after the producer swap" ready_or_failed
ROW=$(job_row "$VHASH")
P=$(echo "$ROW" | cut -d'|' -f3)
ERR=$(echo "$ROW" | cut -d'|' -f4-)
echo "    final job: $(echo "$ROW" | cut -d'|' -f1,2) error: ${ERR:0:200}"
check "the job finished on the persisted local ladder (AV, no Livepeer)" json_has "$P" \
  'map(.process) | (index("AV") != null and index("Livepeer") == null)'
not_ebml_abort() { case "$ERR" in *"was not declared"*) [[ "$ERR" == *PROCESS_TRACKS_CHANGED* ]] ;; *) true ;; esac; }
check "no attempt ended in an unclassified EBML track abort" not_ebml_abort
full_swap() { full_length "$1" "$2" "$LEN"; }
every_replica "swapped VOD renditions span the whole ${LEN}s source" "$VPB" full_swap
stack_ctl start "$GATEWAY" || true
finish
