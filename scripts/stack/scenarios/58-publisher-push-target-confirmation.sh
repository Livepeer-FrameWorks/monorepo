#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, uses the isolated stack's internal RTMP addresses
# A real restream target must not turn otherwise valid ingest into a denied push.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl timeout || finish
source_id="" destination_id="" publisher=""
cleanup() {
  [ -z "$publisher" ] || kill "$publisher" 2>/dev/null
  [ -z "$source_id" ] || delete_stream "$source_id"
  [ -z "$destination_id" ] || delete_stream "$destination_id"
  return 0
}
trap cleanup EXIT

source=$(create_stream "targets58-source-$(date +%s)" false)
destination=$(create_stream "targets58-destination-$(date +%s)" false)
source_id=$(echo "$source" | jq -r '.id // empty')
destination_id=$(echo "$destination" | jq -r '.id // empty')
source_uuid=$(echo "$source" | jq -r '.streamId // empty')
source_key=$(echo "$source" | jq -r '.streamKey // empty')
destination_key=$(echo "$destination" | jq -r '.streamKey // empty')
destination_playback=$(echo "$destination" | jq -r '.playbackId // empty')
[ -n "$source_id" ] && [ -n "$destination_id" ] || { fail "create restream fixtures"; finish; }
internal=$(internal_name_of_key "$source_key")
destination_internal=$(internal_name_of_key "$destination_key")
publisher=$(publish_until_admitted "$EDGE_B_RTMP" "$source_key" 320x180 10 90 90) || { fail "prepare source authority"; finish; }
kill "$publisher" 2>/dev/null; publisher=""
released() {
  [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name IN ('$internal','live+$internal') AND ended_at IS NULL")" = 0 ]
}
eventually 45 "prewarm generation ended" released || finish
before=$(pg commodore "SELECT authority_version FROM commodore.media_authority_current WHERE tenant_id='$STACK_TENANT_ID' AND authority_kind='media_object' AND authority_id='live_stream:$source_uuid'")
[ -n "$before" ] || { fail "source authority version is missing"; finish; }
target=$(gql 'mutation($id:ID!,$input:CreatePushTargetInput!){createPushTarget(streamId:$id,input:$input){id}}' \
  "$(jq -cn --arg id "$source_id" --arg uri "rtmp://edge:1935/live/$destination_key" '{id:$id,input:{name:"stack58",platform:"custom",targetUri:$uri,videoChoice:"SOURCE_VIDEO"}}')")
check "create cross-cell restream target" json_has "$target" '.data.createPushTarget.id != null'
target_authority_ready() {
  local current
  current=$(pg commodore "SELECT authority_version FROM commodore.media_authority_current WHERE tenant_id='$STACK_TENANT_ID' AND authority_kind='media_object' AND authority_id='live_stream:$source_uuid'")
  [ -n "$current" ] && [ "$current" -gt "$before" ] &&
    [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.media_object_authority_projection WHERE tenant_id='$STACK_TENANT_ID' AND stream_id='$source_uuid' AND authority_version=$current AND local_ingest_ready")" = 1 ]
}
eventually 90 "cell B holds the changed target authority" target_authority_ready || finish

# Admission under test uses one encoder invocation, with no retry helper.
publisher=$(publish "$EDGE_B_RTMP" "$source_key" 320x180 10 90)
confirmed_with_target() {
  generation=$(pg "$FOGHORN_B_DB" "SELECT session.id FROM foghorn.ingest_sessions session JOIN foghorn.admission_push_target_revisions targets ON targets.source_generation=session.id AND targets.tenant_id=session.tenant_id WHERE session.tenant_id='$STACK_TENANT_ID' AND session.stream_internal_name IN ('$internal','live+$internal') AND session.ended_at IS NULL AND session.projection_state='active' AND session.publisher_admission_version=1 AND targets.target_revision>0")
  [ -n "$generation" ]
}
eventually 15 "one encoder confirms v1 admission with a target revision" confirmed_with_target || finish
read -r -a a_bases <<< "$FOGHORN_A_URLS"
target_confirmed_in_a() {
  [ "$(pg "$FOGHORN_A_DB" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name IN ('$destination_internal','live+$destination_internal') AND ended_at IS NULL AND projection_state='active'")" = 1 ]
}
eventually 30 "restream publisher is confirmed in cell A" target_confirmed_in_a
eventually 60 "destination receives real cross-cell restream media" media_at "${a_bases[0]}" "$destination_playback"
decode_target() {
  local location
  location=$(location_at "${a_bases[0]}" "$destination_playback")
  edge_a_location "$location" && timeout 15 ffmpeg -hide_banner -loglevel error -i "$location" -frames:v 1 -f null - >"$STACK_STATE_DIR/target58-decode.log" 2>&1
}
check "cross-cell target decodes a video frame" decode_target
check "exactly one source effect exists" [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.ingest_admission_effects WHERE tenant_id='$STACK_TENANT_ID' AND source_generation='$generation'")" = 1 ]
check "exactly one target-revision identity exists" [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.admission_push_target_revisions WHERE tenant_id='$STACK_TENANT_ID' AND source_generation='$generation'")" = 1 ]
finish
