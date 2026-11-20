#!/usr/bin/env bash
# stack-scenario: default
# tenant: qa
# staging: unsafe, changes the account ladder default and publishes a stream
. "$(dirname "$0")/../lib.sh"
need ffmpeg jq curl python3 || finish
SID='' PUB=''
policy=$(gql 'query{transcodeLadderPolicy{tenantLadder effectiveLadder ladders{name}}}')
prior=$(echo "$policy" | jq -r '.data.transcodeLadderPolicy.tenantLadder // ""')
set_default() { gql 'mutation($ladder:String!){setTranscodeLadderPolicy(input:{ladder:$ladder}){__typename}}' "$(jq -cn --arg ladder "$1" '{ladder:$ladder}')"; }
cleanup() { [ -z "$PUB" ] || kill "$PUB" 2>/dev/null; [ -z "$SID" ] || delete_stream "$SID"; set_default "$prior" >/dev/null; }
trap cleanup EXIT
set_default '' | jq -e '.data.setTranscodeLadderPolicy.__typename=="TranscodeLadderPolicy"' >/dev/null || { fail 'inherit the plan ladder for this test'; finish; }

set_ladder() { gql 'mutation($name:String!){setTranscodeLadderPolicy(input:{ladder:$name}){__typename ... on TranscodeLadderPolicy{effectiveLadder}}}' "$(jq -cn --arg name "$1" '{name:$name}')"; }
cleanup() { [ -z "$PUB" ] || kill "$PUB" 2>/dev/null; [ -z "$SID" ] || delete_stream "$SID"; set_ladder "$prior" >/dev/null; }
trap cleanup EXIT
set_ladder '' | jq -e '.data.setTranscodeLadderPolicy.effectiveLadder=="standard"' >/dev/null || { fail 'restore standard inheritance'; finish; }
ready() { [ "$(vod_status "$1")" = READY ]; }
recorded_ceiling() {
  pg "$FOGHORN_A_DB" "SELECT tracks::text FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1'" |
    jq -e --argjson height "$2" '[.[] | select((.track_type // .trackType // .type)=="video" and (.codec|ascii_downcase)!="jpeg") | ([.width,.height]|min)] | length>0 and (max-$height|fabs)<=32' >/dev/null
}
for entry in '2560x1440 qhd 1440' '3840x2160 uhd 2160' '3840x2160 inherit 1080'; do
  read -r geometry ladder ceiling <<<"$entry"
  file="$STACK_STATE_DIR/ladder-$ladder.mp4"
  render_mp4 "$file" "$geometry" 10 8 || { fail "render $geometry"; continue; }
  options='{"keepOriginal":false}'
  [ "$ladder" = inherit ] || options=$(jq -cn --arg ladder "$ladder" '{transcodeLadder:$ladder,keepOriginal:false}')
  result=$(upload_vod "$file" "stack named ladder $ladder" "$options")
  id=$(echo "$result" | jq -r '.id // empty'); hash=$(echo "$result" | jq -r '.artifactHash // empty')
  [ -n "$id" ] || { fail "upload $ladder: $result"; continue; }
  eventually 900 "$ladder upload completes" ready "$id" && check "$ladder recorded ceiling is $ceiling" recorded_ceiling "$hash" "$ceiling"
done
before=$(pg commodore "SELECT count(*) FROM commodore.vod_assets WHERE tenant_id='$STACK_TENANT_ID' AND title='stack refused ladder'")
refused=$(gql 'mutation{createVodUpload(input:{filename:"valid.mp4",sizeBytes:1024,title:"stack refused ladder",transcodeLadder:"unknown"}){__typename ... on ValidationError{field}}}')
check 'unknown ladder is refused on the ladder field' json_has "$refused" '.data.createVodUpload.__typename=="ValidationError" and .data.createVodUpload.field=="transcodeLadder"'
after=$(pg commodore "SELECT count(*) FROM commodore.vod_assets WHERE tenant_id='$STACK_TENANT_ID' AND title='stack refused ladder'")
check 'refused upload registers no asset' [ "$before" = "$after" ]

stream=$(create_stream "stack live ladder change" false)
SID=$(echo "$stream" | jq -r '.id'); uuid=$(echo "$stream" | jq -r '.streamId'); key=$(echo "$stream" | jq -r '.streamKey'); internal=$(internal_name_of "$uuid")
session_ceiling() {
  pg "$FOGHORN_A_DB" "SELECT processes_json FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name='$internal' AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1" |
    jq -e --argjson height "$1" '[.[] | select(.process=="Livepeer") | .target_profiles[].height] | length>0 and max==$height' >/dev/null
}
ended() { [ "$(pg "$FOGHORN_A_DB" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name='$internal' AND ended_at IS NULL")" = 0 ]; }
PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$key" 1280x720 10 240) || { fail 'publish standard stream'; finish; }
eventually 60 'first broadcast snapshots standard' session_ceiling 1080
updated=$(gql 'mutation($id:ID!){updateStream(id:$id,input:{transcodeLadder:"uhd"}){__typename}}' "$(jq -cn --arg id "$SID" '{id:$id}')")
check 'stream ladder change accepted' json_has "$updated" '.data.updateStream.__typename=="Stream"'
check 'running broadcast retains its bound ladder' session_ceiling 1080
kill "$PUB" 2>/dev/null; PUB=''
eventually 90 'old broadcast ends' ended
PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$key" 1280x720 10 60) || { fail 'republish changed stream'; finish; }
eventually 90 'next broadcast snapshots uhd' session_ceiling 2160
finish
