#!/usr/bin/env bash
# stack-scenario: default
# tenant: qa
# staging: unsafe, creates a paid fixture tenant and cuts the publishing edge's control connection
# Real live-buffer and rolling-DVR clips stay queued across a control blip,
# then complete and decode through every replica after the edge reconnects.
. "$(dirname "$0")/../lib.sh"
need ffmpeg jq curl python3 docker openssl || finish
[ "${STACK_TARGET:-stack}" != staging ] || { blocked 'requires an isolated edge'; finish; }
. "$STACK_LIB_DIR/cli-context.sh"
. "$STACK_LIB_DIR/tenant-lifecycle.sh"
run="clip-reconnect-$(date +%s)-$RANDOM"

# The stack's developer tier has only 120 seconds of DVR. A separate paid
# tenant retains media beyond the live window without changing shared limits.
email=$(test_email "clip-reconnect-$(date +%s)-$RANDOM")
password="Stack-$(openssl rand -hex 8)"
cookies="$STACK_STATE_DIR/clip-reconnect.cookies"
registered=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" \
  '{email:$e,password:$p,first_name:"Stack",last_name:"Reconnect"}+$b' |
  curl -s -m 30 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/register")
[ "$registered" = 201 ] || { fail 'register clip fixture tenant'; finish; }
eventually 60 'clip fixture verification email arrives' mail_seen "$email" || finish
verification=$(mail_token "$email")
verified=$(jq -cn --arg t "$verification" '{token:$t}' |
  curl -s -m 30 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/verify")
[ "$verified" = 200 ] || { fail 'verify clip fixture tenant'; finish; }
jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" '{email:$e,password:$p}+$b' |
  curl -fsS -m 30 -c "$cookies" -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login" >/dev/null || { fail 'login clip fixture tenant'; finish; }
STACK_API_TOKEN=$(awk '$6=="access_token"{print $7}' "$cookies" | tail -1)
[ -n "$STACK_API_TOKEN" ] || { fail 'clip fixture login returns a session'; finish; }
STACK_TENANT_ID=$(python3 - "$STACK_API_TOKEN" <<'PYTENANT'
import base64,json,sys
payload=sys.argv[1].split('.')[1]
print(json.loads(base64.urlsafe_b64decode(payload+'='*(-len(payload)%4)))['tenant_id'])
PYTENANT
) || { fail 'resolve clip fixture tenant'; finish; }
export STACK_API_TOKEN STACK_TENANT_ID
"$STACK_REPO/bin/cli" admin billing set-tier --tenant-id "$STACK_TENANT_ID" --tier payg --billing-model prepaid \
  --reason 'stack pinned clip reconnect fixture' --output json >/dev/null || { fail 'assign paid DVR fixture tier'; finish; }
"$STACK_REPO/bin/cli" admin billing credit --tenant-id "$STACK_TENANT_ID" --amount 10.00 \
  --reason 'stack pinned clip reconnect fixture credit' --output json >/dev/null || { fail 'fund prepaid clip fixture'; finish; }
paid_tier_visible() {
  gql 'query{billingStatus{currentTier{tierName}} prepaidBalance{availableBalanceCents}}' |
    jq -e '.data.billingStatus.currentTier.tierName=="payg" and .data.prepaidBalance.availableBalanceCents>0' >/dev/null
}
eventually 90 'paid DVR fixture tier is visible' paid_tier_visible || finish

EDGE_SERVICE=${STACK_EDGE_A_SERVICE:-edge}
EDGE_NODE=${STACK_EDGE_A_NODE:-edge-node-1}
CONTROL_PORT=${STACK_FOGHORN_CONTROL_PORT:-18029}
CONTROL_SERVICES=(foghorn foghorn-2)
SID='' PUB=''
ids=() hashes=() playbacks=()
CLIP_HOLD_INSTALLED=0
remove_clip_queue_gate() {
  [ "$CLIP_HOLD_INSTALLED" = 1 ] || return 0
  pg_fixture "$FOGHORN_A_DB" "BEGIN;
    DROP TRIGGER stack85_hold_clip_dispatch ON foghorn.processing_jobs;
    DROP FUNCTION foghorn.stack85_hold_clip_dispatch(); COMMIT;" >/dev/null || return 1
  CLIP_HOLD_INSTALLED=0
}
cleanup() {
  local hash service
  remove_clip_queue_gate || true
  netfault_stop "$EDGE_SERVICE"
  for service in "${CONTROL_SERVICES[@]}"; do netfault_stop "$service"; done
  for hash in "${hashes[@]}"; do
    pg_fixture "$FOGHORN_A_DB" "UPDATE foghorn.processing_jobs SET next_attempt_at=NULL
      WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash' AND status='queued'" >/dev/null || true
  done
  [ -z "$PUB" ] || kill "$PUB" 2>/dev/null || true
  [ -z "$SID" ] || delete_stream "$SID"
}
trap cleanup EXIT
netfault_start "$EDGE_SERVICE" || finish
for service in "${CONTROL_SERVICES[@]}"; do netfault_start "$service" || finish; done
EDGE_IP=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{println .IPAddress}}{{end}}' "$(container_of "$EDGE_SERVICE")" | head -1)
[[ "$EDGE_IP" =~ ^[0-9.]+$ ]] || { fail 'resolve publishing edge address'; finish; }
S=$(create_stream "stack-clip-reconnect-$(date +%s)" true OFF)
SID=$(jq -r '.id // empty' <<<"$S")
UUID=$(jq -r '.streamId // empty' <<<"$S")
PB=$(jq -r '.playbackId // empty' <<<"$S")
KEY=$(jq -r '.streamKey // empty' <<<"$S")
[ -n "$SID" ] && [ -n "$UUID" ] && [ -n "$PB" ] && [ -n "$KEY" ] || { fail 'create recording stream'; finish; }
IN=$(internal_name_of "$UUID")
set_placement PLATFORM_OFFICIAL "$UUID" || { fail 'apply clip fixture placement'; finish; }
eventually 90 'clip fixture placement is effective' policy_effective || finish
eventually 60 'clip fixture has fresh ingest placement evidence' preview_ready rtmp PLATFORM_OFFICIAL '' || finish
# Copied clips begin at the preceding keyframe. Every frame is a keyframe in
# this fixture, so its strict four-second decode check measures the reconnect
# result without including the ordinary two-second GOP's leading media.
publish() {
  ffmpeg -hide_banner -loglevel error -re \
    -f lavfi -i "testsrc2=size=$3:rate=$4" -f lavfi -i 'sine=frequency=440:sample_rate=48000' \
    -t "$5" -c:v libx264 -preset veryfast -g 1 -bf 0 -pix_fmt yuv420p -c:a aac -b:a 96k \
    -flvflags no_metadata -f flv "$1/$2" >"$STACK_STATE_DIR/publish-$2.log" 2>&1 &
  echo $!
}
PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$KEY" 640x360 15 600) || { fail 'admit recording publisher'; finish; }
eventually 90 'recording source serves media' media_at "$FOGHORN_A_URL" "$PB" || finish

# Select bytes actually recorded beyond the live source's 120-second coverage
# window, so the API must choose the rolling DVR rather than another live cut.
recorded_range() {
  DVR_START=$(pg "$FOGHORN_A_DB" "SELECT (CEIL(MIN(segment.media_start_ms)/1000.0)+1)::bigint
    FROM foghorn.dvr_segments segment JOIN foghorn.artifacts owner ON owner.artifact_hash=segment.artifact_hash
    WHERE owner.tenant_id='$STACK_TENANT_ID' AND owner.stream_internal_name='$IN' AND owner.artifact_type='dvr'
      AND segment.status NOT IN ('reclaimed','deleted_local','lost_local')
    GROUP BY owner.artifact_hash
    HAVING MAX(segment.media_end_ms)-MIN(segment.media_start_ms)>10000
      AND MIN(segment.media_start_ms)+10000<EXTRACT(EPOCH FROM NOW())*1000-130000
    ORDER BY MIN(segment.media_start_ms) LIMIT 1") || return 1
  [ -n "$DVR_START" ]
}
eventually 240 'rolling DVR contains a cut outside live coverage' recorded_range || finish

# A queue wake can dispatch the job before CreateClip's HTTP response returns.
# Hold this tenant's clip jobs at insertion, before the dispatcher can claim them.
install_clip_queue_gate() {
  pg_fixture "$FOGHORN_A_DB" "BEGIN;
    CREATE FUNCTION foghorn.stack85_hold_clip_dispatch() RETURNS trigger AS \$hold\$
    BEGIN
      IF NEW.tenant_id='$STACK_TENANT_ID'::uuid AND NEW.job_type='process'
        AND NEW.source_params->>'source_kind' IN ('live','dvr_rolling') THEN
        NEW.next_attempt_at=NOW()+INTERVAL '3 minutes';
      END IF;
      RETURN NEW;
    END;
    \$hold\$ LANGUAGE plpgsql;
    CREATE TRIGGER stack85_hold_clip_dispatch BEFORE INSERT ON foghorn.processing_jobs
      FOR EACH ROW EXECUTE FUNCTION foghorn.stack85_hold_clip_dispatch(); COMMIT;" >/dev/null || return 1
  CLIP_HOLD_INSTALLED=1
}
install_clip_queue_gate || { fail 'install tenant-scoped clip queue gate'; finish; }

DVR_ATTEMPT=0
hold_clip() {
  local kind=$1 start response id hash playback held snapshot
  if [ "$kind" = live ]; then
    start=$(($(date +%s)-8))
  else
    start=$((DVR_START+DVR_ATTEMPT))
    DVR_ATTEMPT=$((DVR_ATTEMPT+1))
  fi
  response=$(gql 'mutation($i:CreateClipInput!){createClip(input:$i){__typename ... on Clip{id clipHash playbackId} ... on ValidationError{message}}}' \
    "$(jq -cn --arg id "$SID" --arg kind "$kind" --argjson start "$start" '{i:{streamId:$id,title:("stack reconnect "+$kind),mode:"DURATION",startUnix:$start,duration:4}}')") || return 1
  id=$(jq -r '.data.createClip.id // empty' <<<"$response")
  hash=$(jq -r '.data.createClip.clipHash // empty' <<<"$response")
  playback=$(jq -r '.data.createClip.playbackId // empty' <<<"$response")
  [ -n "$id" ] && [ -n "$hash" ] && [ -n "$playback" ] || { log "clip response: $response"; return 1; }
  # Hold only a still-queued API-created job. A dispatch that won the race
  # is never rewritten; the next attempt captures another queued request.
  held=$(pg_fixture "$FOGHORN_A_DB" "WITH held AS (
    UPDATE foghorn.processing_jobs SET next_attempt_at=NOW()+INTERVAL '3 minutes'
    WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash' AND status='queued'
      AND preferred_node_id='$EDGE_NODE' AND source_params->>'source_kind'='$kind'
      AND (source_params->>'source_stop_unix')::bigint-(source_params->>'source_start_unix')::bigint=4
    RETURNING job_id) SELECT job_id FROM held") || return 1
  if [ -z "$held" ]; then
    snapshot=$(pg "$FOGHORN_A_DB" "SELECT jsonb_build_object('status',status,'node',preferred_node_id,
      'source_kind',source_params->>'source_kind','seconds',
      (source_params->>'source_stop_unix')::bigint-(source_params->>'source_start_unix')::bigint)
      FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash'
      ORDER BY created_at DESC LIMIT 1") || return 1
    log "clip queue capture: $snapshot"
    return 1
  fi
  ids+=("$id"); hashes+=("$hash"); playbacks+=("$playback")
}
eventually 30 'queue a pinned live clip before the fault' hold_clip live || finish
eventually 30 'queue a pinned DVR clip before the fault' hold_clip dvr_rolling || finish
# Segment rows alone do not prove the source can serve a cut. Establish both
# pinned sources before injecting a reconnect fault, with the same source-only
# selection used by processing's track probe.
probe_clip_source() {
  local hash=$1 params stream start stop credential path
  params=$(pg "$FOGHORN_A_DB" "SELECT source_params FROM foghorn.processing_jobs
    WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash' ORDER BY created_at DESC LIMIT 1") || return 1
  stream=$(jq -r '.source_stream_name' <<<"$params")
  start=$(jq -r '.source_start_unix' <<<"$params")
  stop=$(jq -r '.source_stop_unix' <<<"$params")
  credential=$(jq -r '.source_credential' <<<"$params")
  path="$STACK_STATE_DIR/pinned-source-$hash.mkv"
  curl -fsS -m 20 -G "http://$EDGE_A_HOST:8082/$stream.mkv" \
    --data-urlencode "startunix=$start" --data-urlencode "stopunix=$stop" \
    --data-urlencode "token=$credential" --data-urlencode "tkn=fwproc-$(openssl rand -hex 16)" \
    --data-urlencode 'video=source' --data-urlencode 'audio=source' \
    --data-urlencode 'subtitle=none' --data-urlencode 'meta=none' --data-urlencode 'rate=0' \
    --output "$path" || return 1
  python3 "$STACK_LIB_DIR/decode_asset.py" "$path" 4 15
}
for hash in "${hashes[@]}"; do
  eventually 45 "pinned source $hash decodes before the fault" probe_clip_source "$hash" || finish
done
remove_clip_queue_gate || { fail 'remove clip insertion gate'; finish; }

CUT_TS=$(utc_now)
CUT_START=$(monotonic_ms)
netfault "$EDGE_SERVICE" sh -c "iptables -A FW_FAULT_OUT -p tcp --dport $CONTROL_PORT -j REJECT --reject-with tcp-reset &&
  iptables -A FW_FAULT_IN -p tcp --sport $CONTROL_PORT -j REJECT --reject-with tcp-reset &&
  { ss -K '( dport = :$CONTROL_PORT )' >/dev/null 2>&1 || true; }" || { fail 'cut control connection'; finish; }
for service in "${CONTROL_SERVICES[@]}"; do
  netfault "$service" ss -K "( sport = :$CONTROL_PORT and dst = $EDGE_IP )" >/dev/null || { fail 'close the server control socket'; finish; }
done
lost_control() { logs_since "$CUT_TS" "$EDGE_SERVICE" | grep -q 'Helmsman control client disconnected'; }
eventually 8 'edge observes the control disconnect' lost_control || finish
server_lost_control() { logs_since "$CUT_TS" foghorn foghorn-2 | grep 'Helmsman disconnected' | grep -q "$EDGE_NODE"; }
eventually 8 'Foghorn observes the control disconnect' server_lost_control || finish
for hash in "${hashes[@]}"; do
  pg_fixture "$FOGHORN_A_DB" "UPDATE foghorn.processing_jobs SET next_attempt_at=NULL
    WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash' AND status='queued'" >/dev/null || { fail 'release queued clip'; finish; }
done
clips_waited() {
  local hash rows
  for hash in "${hashes[@]}"; do
    rows=$(pg "$FOGHORN_A_DB" "SELECT jsonb_build_object('status',status,'reason',wait_reason,'retries',retry_count)
      FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash' ORDER BY created_at DESC LIMIT 1") || return 1
    [ -n "$rows" ] || return 1
    jq -e '.status!="failed" and .status!="completed"' <<<"$rows" >/dev/null || { log "clip during fault: $rows"; return 2; }
    jq -e '.status=="queued" and .reason=="fitting_node_unavailable" and .retries==0' <<<"$rows" >/dev/null || return 1
  done
}
eventually 10 'both pinned clips wait without terminal failure or retry cost' clips_waited || finish
while [ "$(($(monotonic_ms)-CUT_START))" -lt 10000 ]; do sleep 1; done
RESTORE_TS=$(utc_now)
netfault_clear "$EDGE_SERVICE" || { fail 'restore control connection'; finish; }
check 'control blip stays below thirty seconds' test "$(($(monotonic_ms)-CUT_START))" -lt 30000
reconnected() {
  logs_since "$RESTORE_TS" foghorn foghorn-2 | grep 'Reconciled node ingest sessions' | grep -q "$EDGE_NODE"
}
eventually 30 'edge reconnects within the source grace period' reconnected || finish
check 'publisher stays alive through the control blip' kill -0 "$PUB"

clip_ready() {
  local response status
  response=$(gql 'query($id:ID!){node(id:$id){... on Clip{status}}}' "$(jq -cn --arg id "$1" '{id:$id}')") || return 1
  status=$(jq -r '.data.node.status // empty' <<<"$response")
  case "$status" in ready|READY|done|DONE) return 0 ;; failed|FAILED) log "$response"; return 2 ;; esac
  return 1
}
clip_decodes() {
  local url
  full_length "$1" "$2" 4 1 || return 1
  url=$(location_at "$1" "$2") || return 1
  [ -n "$url" ] || return 1
  python3 "$STACK_LIB_DIR/decode_asset.py" "$url" 4 15
}
for index in "${!ids[@]}"; do
  eventually 240 "reconnected clip ${hashes[$index]} reaches ready" clip_ready "${ids[$index]}" || finish
  for replica in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
    eventually 60 "reconnected clip ${hashes[$index]} decodes its requested frames via $replica" clip_decodes "$replica" "${playbacks[$index]}"
  done
  check "reconnected clip ${hashes[$index]} spends no retries" test "$(pg "$FOGHORN_A_DB" "SELECT retry_count FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='${hashes[$index]}' ORDER BY created_at DESC LIMIT 1")" = 0
done
finish
