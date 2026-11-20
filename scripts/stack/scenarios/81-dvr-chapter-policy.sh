#!/usr/bin/env bash
# stack-scenario: default
# staging: safe
# tenant: qa
# Save a daily chapter interval immediately after creation and record briefly
# on each cell. Every accepted recording must pin the saved interval, even
# when the live rewind window is much smaller. A later save cannot rewrite it.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl psql || finish
PUB='' SID=''
cleanup() {
  [ -z "$PUB" ] || kill "$PUB" 2>/dev/null || true
  [ -z "$SID" ] || delete_stream "$SID"
}
trap cleanup EXIT

require_check() {
  local description=$1
  shift
  if "$@"; then pass "$description"; else fail "$description"; finish; fi
}

set_interval() {
  local response
  response=$(gql 'mutation($id:ID!,$seconds:Int!){updateStream(id:$id,input:{dvrChapterMode:FIXED_INTERVAL,dvrChapterIntervalSeconds:$seconds}){__typename ... on Stream{dvrChapterMode dvrChapterIntervalSeconds}}}' \
    "$(jq -cn --arg id "$SID" --argjson seconds "$1" '{id:$id,seconds:$seconds}')") || return 1
  json_has "$response" ".errors==null and .data.updateStream.dvrChapterMode==\"FIXED_INTERVAL\" and .data.updateStream.dvrChapterIntervalSeconds==$1"
}

recording_is_daily() {
  DVR=$(pg commodore "SELECT dvr_hash FROM commodore.dvr_recordings
    WHERE tenant_id='$STACK_TENANT_ID' AND stream_id='$UUID' ORDER BY created_at DESC LIMIT 1") || return 1
  [ -n "$DVR" ] || return 1
  [ "$(pg "$DB" "SELECT count(*) FROM foghorn.artifacts
    WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$DVR'
      AND dvr_chapter_mode='fixed_interval' AND dvr_chapter_interval=86400")" = 1 ]
}

no_obsolete_recording() {
  [ "$(pg "$DB" "SELECT count(*) FROM foghorn.artifacts
    WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name='$INTERNAL'
      AND artifact_type='dvr' AND (dvr_chapter_mode IS DISTINCT FROM 'fixed_interval'
        OR dvr_chapter_interval IS DISTINCT FROM 86400)")" = 0 ]
}

has_segments() {
  local count
  count=$(pg "$DB" "SELECT count(*) FROM foghorn.dvr_segments segment
    JOIN foghorn.artifacts owner ON owner.artifact_hash=segment.artifact_hash
    WHERE owner.tenant_id='$STACK_TENANT_ID' AND segment.artifact_hash='$DVR'") || return 1
  [ "${count:-0}" -gt 0 ]
}

daily_chapter_ready() {
  local count
  count=$(pg "$DB" "SELECT count(*) FROM foghorn.dvr_chapters chapter
    JOIN foghorn.artifacts owner ON owner.artifact_hash=chapter.artifact_hash
    WHERE owner.tenant_id='$STACK_TENANT_ID' AND chapter.artifact_hash='$DVR'
      AND chapter.mode='fixed_interval' AND chapter.interval_seconds=86400
      AND chapter.state IN ('finalized','frozen','reclaimed') AND chapter.playback_id IS NOT NULL") || return 1
  [ "${count:-0}" -gt 0 ]
}

for cell in a b; do
  EDGE=$EDGE_A_RTMP DB=$FOGHORN_A_DB
  [ "$cell" != b ] || { EDGE=$EDGE_B_RTMP; DB=$FOGHORN_B_DB; }
  S=$(create_stream "stack-daily-chapters-$cell-$(date +%s)" true)
  SID=$(jq -r '.id // empty' <<<"$S")
  UUID=$(jq -r '.streamId // empty' <<<"$S")
  KEY=$(jq -r '.streamKey // empty' <<<"$S")
  [ -n "$SID" ] && [ -n "$UUID" ] && [ -n "$KEY" ] || { fail "create recording stream in cell $cell"; finish; }
  require_check "cell $cell saves the daily interval before ingest" set_interval 86400
  INTERNAL=$(internal_name_of "$UUID")
  [ -n "$INTERNAL" ] || { fail "resolve owned stream identity"; finish; }
  # Pending publication may refuse a setup connection. The real-engine lease
  # contract asserts that refusal; here every recording from every connection
  # is inspected so a retry cannot hide an earlier wrong-policy admission.
  PUB=$(publish_until_admitted "$EDGE" "$KEY" 640x360 10 30) || { fail "publisher setup in cell $cell"; finish; }
  eventually 30 "cell $cell pins the saved daily chapter policy" recording_is_daily || finish
  require_check "cell $cell created no recording with obsolete settings" no_obsolete_recording
  eventually 30 "cell $cell records source segments" has_segments || finish
  require_check "cell $cell saves a different policy for the next recording" set_interval 7200
  require_check "cell $cell keeps the accepted recording's daily policy" recording_is_daily
  wait_publisher "$PUB" 60 || { fail "bounded publisher did not stop"; finish; }
  PUB=''
  eventually 120 "cell $cell finalizes the short daily chapter" daily_chapter_ready || finish
  require_check "cell $cell retained its policy through finalization" recording_is_daily
  delete_stream "$SID"
  SID=''
done

finish
