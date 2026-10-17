#!/usr/bin/env bash
# stack-scenario: manual
# A published clip whose one processing run left no thumbnail attempt (failed
# Thumbs process, refused mint, upload that never landed) is ready without
# thumbnails. The backfill must queue a thumbnail job for it, Helmsman must run
# the stored clip through Mist with only the Thumbs process, and the thumbnails
# Mist announces for that staged file must be uploaded and published before the
# job completes. Manual: the backfill passes every 10 minutes.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl psql || finish
S=$(create_stream "stack-thumbnail-backfill-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 640x360 15 180 metadata)
trap 'kill "$PUB" 2>/dev/null; delete_stream "$SID"' EXIT
eventually 90 'backfill source live' media_at "${FOGHORN_A_URLS%% *}" "$PB"
sleep 25
start=$(($(date +%s) - 20))
R=$(gql 'mutation($i:CreateClipInput!){createClip(input:$i){__typename ... on Clip{id clipHash playbackId} ... on ValidationError{message}}}' \
  "$(jq -cn --arg s "$SID" --argjson start "$start" '{i:{streamId:$s,title:"thumbnail backfill",mode:"DURATION",startUnix:$start,duration:10}}')")
CHASH=$(echo "$R" | jq -r '.data.createClip.clipHash // empty')
CPB=$(echo "$R" | jq -r '.data.createClip.playbackId // empty')
[ -n "$CHASH" ] || { fail "createClip: $R"; finish; }

artifact_row() {
  pg "$FOGHORN_A_DB" "SELECT status,has_thumbnails,sync_status FROM foghorn.artifacts
    WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$CHASH'"
}
published() {
  local row
  row=$(artifact_row)
  echo "    artifact publication: $row"
  [ "$row" = 'ready|t|synced' ]
}
eventually 180 'clip ready with its first thumbnails' published
kill "$PUB" 2>/dev/null

# Lose the clip's thumbnails: no attempt is left, has_thumbnails is false, and
# the clip is past the age a first publication could still be in flight.
lose_thumbnails() {
  pg "$FOGHORN_A_DB" "DELETE FROM foghorn.thumbnail_task_assignment
      WHERE tenant_id='$STACK_TENANT_ID' AND asset_key='$CHASH';
    UPDATE foghorn.artifacts SET has_thumbnails=false, created_at=NOW() - INTERVAL '2 hours'
      WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$CHASH'" >/dev/null &&
    [ "$(artifact_row)" = 'ready|f|synced' ]
}
check 'clip thumbnails lost (no attempt, has_thumbnails false, aged 2h)' lose_thumbnails
lost_at=$(utc_now)

thumbnail_job() { # thumbnail_job: "status|error" of the clip's newest thumbnail job
  pg "$FOGHORN_A_DB" "SELECT status, COALESCE(error_message,'') FROM foghorn.processing_jobs
    WHERE artifact_hash='$CHASH' AND job_type='thumbnail' ORDER BY created_at DESC LIMIT 1"
}
job_queued() {
  local row
  row=$(thumbnail_job)
  echo "    thumbnail job: ${row:-none}"
  [ -n "$row" ]
}
eventually 720 'backfill queued a thumbnail job for the clip' job_queued
job_completed() {
  local row
  row=$(thumbnail_job)
  echo "    thumbnail job: $row"
  case "$row" in
  completed\|*) return 0 ;;
  failed\|*) return 2 ;;
  esac
  return 1
}
eventually 360 'thumbnail job completed on a processing node' job_completed

# The job completes only once Mist announced thumbnails for the staged file
# after the job started and Helmsman's upload of them was confirmed.
announced() {
  log_json "$lost_at" "select(.msg == \"Thumbnail processing completed; thumbnails uploaded\" and .artifact_hash == \"$CHASH\") | .job_id" \
    "${STACK_EDGE_A_SERVICE:-edge}" edge-b | grep -q .
}
check 'Helmsman saw the thumbnails of the staged clip uploaded' announced
republished() {
  local n
  n=$(pg "$FOGHORN_A_DB" "SELECT count(*) FROM foghorn.thumbnail_task_assignment
    WHERE tenant_id='$STACK_TENANT_ID' AND asset_key='$CHASH' AND status='published'
      AND deterministic_projected_at IS NOT NULL")
  [ "${n:-0}" -ge 1 ] && published
}
eventually 120 'a fresh thumbnail attempt published and projected; has_thumbnails is back' republished
check 'clip media untouched by the thumbnail job' test "$(artifact_row)" = 'ready|t|synced'
every_replica 'clip still plays after the backfill' "$CPB" media_at
finish
