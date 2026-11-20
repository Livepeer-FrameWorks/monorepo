#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, stops and restarts the isolated stack's Purser
# tenant: qa
# A manual start may not invent a policy when billing is unavailable. Once the
# owner recovers, it records with explicit chapter and retention settings even
# though the stream's auto-record switch is off.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl psql || finish
PUB='' SID='' PURSER_STOPPED=0
cleanup() {
  [ "$PURSER_STOPPED" = 0 ] || stack_ctl start purser
  [ -z "$PUB" ] || kill "$PUB" 2>/dev/null || true
  [ -z "$SID" ] || delete_stream "$SID"
}
trap cleanup EXIT

require_check() {
  local description=$1
  shift
  if "$@"; then pass "$description"; else fail "$description"; finish; fi
}

retention_policy_ready() {
  local response
  response=$(gql 'mutation($id:ID!){setStreamRetentionOverrides(input:{streamId:$id,dvrRetentionDaysOverride:14}){__typename ... on StreamRetentionOverrides{dvrRetentionDaysOverride}}}' \
    "$(jq -cn --arg id "$SID" '{id:$id}')") || return 1
  if json_has "$response" '.errors==null and .data.setStreamRetentionOverrides.dvrRetentionDaysOverride==14'; then
    return 0
  fi
  log "retention override response: $(jq -c '.data.setStreamRetentionOverrides // .errors' <<<"$response")"
  return 1
}

manual_start() {
  gql 'mutation($id:ID!){startDVR(streamId:$id){__typename ... on DVRRequest{dvrHash} ... on ValidationError{message}}}' \
    "$(jq -cn --arg id "$SID" '{id:$id}')"
}

no_recording_created() {
  [ "$(pg commodore "SELECT count(*) FROM commodore.dvr_recordings
    WHERE tenant_id='$STACK_TENANT_ID' AND stream_id='$UUID'")" = 0 ]
}

manual_snapshot_present() {
  [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.artifacts
    WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$DVR'
      AND dvr_chapter_mode='fixed_interval' AND dvr_chapter_interval=86400 AND dvr_retention_days=14")" = 1 ]
}

has_segments() {
  local count
  count=$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.dvr_segments segment
    JOIN foghorn.artifacts owner ON owner.artifact_hash=segment.artifact_hash
    WHERE owner.tenant_id='$STACK_TENANT_ID' AND segment.artifact_hash='$DVR'") || return 1
  [ "${count:-0}" -gt 0 ]
}

S=$(create_stream "stack-manual-dvr-policy-$(date +%s)" false)
SID=$(jq -r '.id // empty' <<<"$S")
UUID=$(jq -r '.streamId // empty' <<<"$S")
KEY=$(jq -r '.streamKey // empty' <<<"$S")
[ -n "$SID" ] && [ -n "$UUID" ] && [ -n "$KEY" ] || { fail 'create manual-only source'; finish; }
response=$(gql 'mutation($id:ID!){updateStream(id:$id,input:{dvrChapterMode:FIXED_INTERVAL,dvrChapterIntervalSeconds:86400}){__typename ... on Stream{record dvrChapterIntervalSeconds}}}' \
  "$(jq -cn --arg id "$SID" '{id:$id}')")
require_check 'manual source keeps auto-record disabled and saves daily chapters' json_has "$response" \
  '.errors==null and .data.updateStream.record==false and .data.updateStream.dvrChapterIntervalSeconds==86400'
require_check 'manual source saves its retention override' retention_policy_ready
PUB=$(publish_until_admitted "$EDGE_B_RTMP" "$KEY" 640x360 10 90) || { fail 'manual source did not become live'; finish; }
require_check 'publishing without auto-record creates no recording' no_recording_created

stack_ctl stop purser || { fail 'stop owned billing service for the fault'; finish; }
PURSER_STOPPED=1
if response=$(manual_start); then
  :
else
  result=$?
  fail "manual start failed at transport (curl exit $result) instead of returning policy unavailable"
  finish
fi
require_check 'unknown billing cannot start a manual recording' json_has "$response" \
  '(.data.startDVR.__typename // "") as $kind | (.data.startDVR.dvrHash // "")=="" and (((.errors // []) | length)>0 or ($kind!="" and $kind!="DVRRequest"))'
require_check 'failed manual start minted no recording catalog row' no_recording_created
stack_ctl start purser || { fail 'restart owned billing service'; finish; }
PURSER_STOPPED=0
eventually 45 'billing policy is readable again' retention_policy_ready || finish

response=$(manual_start) || { fail 'manual start after policy recovery'; finish; }
DVR=$(jq -r '.data.startDVR.dvrHash // empty' <<<"$response")
[ -n "$DVR" ] || { fail "manual start did not recover: $(jq -c '.data.startDVR // .errors' <<<"$response")"; finish; }
eventually 30 'manual start pins the daily interval and retention override' manual_snapshot_present || finish
eventually 30 'manual recording receives media' has_segments || finish
response=$(gql 'mutation($id:ID!){stopDVR(dvrHash:$id){__typename}}' "$(jq -cn --arg id "$DVR" '{id:$id}')")
require_check 'manual stop is acknowledged' json_has "$response" '.errors==null and .data.stopDVR.__typename=="DeleteSuccess"'
require_check 'manual stop retains the accepted snapshot' manual_snapshot_present
kill "$PUB" 2>/dev/null || true
PUB=''

finish
