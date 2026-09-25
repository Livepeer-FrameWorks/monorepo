#!/usr/bin/env bash
# stack-scenario: default
# VOD processing through Livepeer, the CPU fallback with its config persisted,
# and a fail-fast import of a missing source.
# Catches: N2/F4 (processing+ manifest rejected by Livepeer auth), N12 (fallback
# config never persisted: job_id "cache_update:<hash>"), N5 (fallback upscales),
# WS4 (404 import stuck PROCESSING, no upload.failed), N25 (a source longer
# than the processing buffer window READY with renditions missing its start).
. "$(dirname "$0")/../lib.sh"

GATEWAY=${STACK_LIVEPEER_GATEWAY_A:-livepeer-gateway-a}
MISSING_URL=${STACK_MISSING_VOD_URL:-https://test-videos.co.uk/vids/does-not-exist-404.mp4}
need ffmpeg jq curl python3 || finish
SINCE=$(date +%s)
webhook_endpoint >/dev/null || fail "webhook endpoint at $RECEIVER_HOOK_URL"
trap 'stack_ctl start "$GATEWAY" >/dev/null 2>&1' EXIT

LONG=${STACK_LONG_VOD_SECONDS:-150}
render_mp4 "$STACK_STATE_DIR/vod480.mp4" 854x480 15 30 || { fail "render VOD source"; finish; }
render_mp4 "$STACK_STATE_DIR/vod480-long.mp4" 854x480 15 "$LONG" || { fail "render long VOD source"; finish; }

job_processes() { # job_processes <artifact hash>: newest job's processes_json
  pg "$FOGHORN_A_DB" "SELECT COALESCE(processes_json,'') FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1' ORDER BY created_at DESC LIMIT 1"
}
ready_or_failed() { # ready_or_failed <vod id>: 0 ready, 2 failed, 1 pending
  local s
  s=$(vod_status "$1")
  echo "    status: $s"
  [ "$s" = READY ] && return 0
  [ "$s" = FAILED ] && return 2
  return 1
}

log "VOD through Livepeer"
eventually 90 "cell A Livepeer gateway discoverable" gateway_discoverable stack-livepeer-gateway-a
V=$(upload_vod "$STACK_STATE_DIR/vod480.mp4" "stack livepeer vod")
VID=$(echo "$V" | jq -r '.id // empty')
VHASH=$(echo "$V" | jq -r '.artifactHash // empty')
VPB=$(echo "$V" | jq -r '.playbackId // empty')
if [ -n "$VID" ]; then
  # Playback checks only mean something for a READY asset: a FAILED one can
  # still serve its unprocessed source.
  if eventually 300 "Livepeer VOD reaches READY" ready_or_failed "$VID"; then
    P=$(job_processes "$VHASH")
    check "processing used Livepeer (no local fallback)" json_has "$P" 'map(.process) | index("Livepeer") != null'
    every_replica "Livepeer VOD plays" "$VPB" media_at
    full_30() { full_length "$1" "$2" 30; }
    every_replica "Livepeer VOD renditions span the whole 30s source" "$VPB" full_30
  fi
else
  fail "upload: $V"
fi

log "VOD longer than the processing buffer window (${LONG}s)"
L=$(upload_vod "$STACK_STATE_DIR/vod480-long.mp4" "stack long vod")
LID=$(echo "$L" | jq -r '.id // empty')
LPB=$(echo "$L" | jq -r '.playbackId // empty')
if [ -n "$LID" ]; then
  if eventually 600 "long VOD reaches READY" ready_or_failed "$LID"; then
    full_long() { full_length "$1" "$2" "$LONG"; }
    every_replica "long VOD renditions span the whole ${LONG}s source" "$LPB" full_long
  fi
else
  fail "upload: $L"
fi

log "VOD with the gateway down -> CPU fallback"
if stack_ctl stop "$GATEWAY"; then
  F=$(upload_vod "$STACK_STATE_DIR/vod480.mp4" "stack fallback vod")
  FID=$(echo "$F" | jq -r '.id // empty')
  FHASH=$(echo "$F" | jq -r '.artifactHash // empty')
  FPB=$(echo "$F" | jq -r '.playbackId // empty')
  if [ -n "$FID" ]; then
    if eventually 420 "fallback VOD reaches READY" ready_or_failed "$FID"; then
      P=$(job_processes "$FHASH")
      echo "    persisted processes: $(echo "$P" | jq -c 'map({process, resolution, track_inhibit})' 2>/dev/null)"
      check "fallback config persisted on the job (local AV, no Livepeer)" json_has "$P" 'map(.process) | (index("AV") != null and index("Livepeer") == null)'
      check "every persisted AV rendition carries an inhibit (never upscales)" json_has "$P" \
        '[.[] | select(.process=="AV" and ((.resolution // "") != ""))] | length > 0 and all(.track_inhibit // "" | length > 0)'
      every_replica "fallback VOD plays" "$FPB" media_at
      full_30() { full_length "$1" "$2" 30; }
      every_replica "fallback VOD renditions span the whole 30s source" "$FPB" full_30
    fi
  else
    fail "upload: $F"
  fi
  stack_ctl start "$GATEWAY" || true
fi

log "import of a missing source fails fast"
R=$(gql 'mutation($i:ImportVodAssetInput!){importVodAsset(input:$i){__typename ... on VodAsset{id artifactHash status} ... on ValidationError{message}}}' \
  "$(jq -cn --arg u "$MISSING_URL" '{i:{url:$u,filename:"missing.mp4",title:"stack missing"}}')")
MID=$(echo "$R" | jq -r '.data.importVodAsset.id // empty')
if [ -n "$MID" ]; then
  failed_fast() { local s; s=$(vod_status "$MID"); [ "$s" = FAILED ] && return 0; [ "$s" = READY ] && return 2; return 1; }
  eventually 60 "missing-source import reaches FAILED within 60 s" failed_fast
  eventually 60 "upload.failed webhook with SOURCE_UNAVAILABLE" saw_event "$SINCE" upload.failed '.reason == "MEDIA_FAILURE_REASON_SOURCE_UNAVAILABLE"'
elif echo "$R" | grep -qiE "private|resolve|dns|network"; then
  blocked "the stack cannot reach $MISSING_URL (set STACK_MISSING_VOD_URL): $(echo "$R" | jq -c '.data.importVodAsset // .errors')"
else
  fail "importVodAsset: $R"
fi
eventually 60 "upload.ready webhook for the Livepeer VOD" saw_event "$SINCE" upload.ready
finish
