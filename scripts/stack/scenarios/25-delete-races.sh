#!/usr/bin/env bash
# stack-scenario: manual
# A clip is deleted about two seconds after creation, then a late proactive
# freeze is delivered for that deleted file to reproduce the queued command
# race. Twenty ready child clips exercise the asynchronous cleanup path.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 || finish
since=$(utc_now)
S=$(create_stream "stack-delete-race-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
INTERNAL=$(internal_name_of "$UUID")
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 640x360 15 600 metadata)
trap 'kill "$PUB" 2>/dev/null; delete_stream "$SID"' EXIT
eventually 90 "source stream live" media_at "${FOGHORN_A_URLS%% *}" "$PB"
sleep 25

make_clip() {
  local n=$1 start
  start=$(($(date +%s) - 20))
  gql 'mutation($i:CreateClipInput!){createClip(input:$i){__typename ... on Clip{id clipHash status} ... on ValidationError{message}}}' \
    "$(jq -cn --arg s "$SID" --argjson start "$start" --argjson n "$n" \
      '{i:{streamId:$s,title:("stack delete " + ($n|tostring)),mode:"DURATION",startUnix:$start,duration:10}}')"
}
delete_clip() {
  gql 'mutation($id:ID!){deleteClip(id:$id){__typename ... on DeleteSuccess{success deletedId}}}' \
    "$(jq -cn --arg id "$1" '{id:$id}')"
}

race_one() {
  local n=$1 id
  make_clip "$n" >"$STACK_STATE_DIR/delete-race-create-$n.json"
  id=$(jq -r '.data.createClip.id // empty' "$STACK_STATE_DIR/delete-race-create-$n.json")
  [ -n "$id" ] || return 1
  sleep 2
  delete_clip "$id" >"$STACK_STATE_DIR/delete-race-delete-$n.json"
}
race_one 1
race_deleted() {
  [ "$(pg "$FOGHORN_A_DB" "SELECT COALESCE(status,'') FROM foghorn.artifacts
    WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1'")" = deleted ]
}
for n in $(seq 1 1); do
  race=$(cat "$STACK_STATE_DIR/delete-race-create-$n.json")
  race_hash=$(echo "$race" | jq -r '.data.createClip.clipHash // empty')
  if [ -z "$race_hash" ] || [ ! -f "$STACK_STATE_DIR/delete-race-delete-$n.json" ]; then
    fail "race clip $n was not created and deleted: $race"
    continue
  fi
  deleted=$(cat "$STACK_STATE_DIR/delete-race-delete-$n.json")
  check "race clip $n deleted about 2 s after creation" json_has "$deleted" '.data.deleteClip.success == true'
  eventually 120 "race clip $n reaches clean deleted state" race_deleted "$race_hash"
done

hashes=()
all_ready_and_synced() {
  local hash row pending=0
  for hash in "${hashes[@]}"; do
    row=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(status,''), COALESCE(sync_status,'')
      FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash'")
    case "$row" in
      ready\|synced) ;;
      failed\|* | deleted\|*) echo "    $hash: $row"; return 2 ;;
      *) pending=$((pending + 1)) ;;
    esac
  done
  echo "    $pending child clips still processing or syncing"
  [ "$pending" = 0 ]
}
for start in 1 5 9 13 17; do
  end=$((start + 3))
  for n in $(seq "$start" "$end"); do
    (make_clip "$n" >"$STACK_STATE_DIR/delete-child-$n.json") &
  done
  wait
  for n in $(seq "$start" "$end"); do
    r=$(cat "$STACK_STATE_DIR/delete-child-$n.json")
    hash=$(echo "$r" | jq -r '.data.createClip.clipHash // empty')
    if [ -n "$hash" ]; then hashes+=("$hash"); else fail "child clip $n rejected: $r"; fi
  done
  [ "${#hashes[@]}" = "$end" ] || finish
  eventually 240 "child clips 1-$end are READY and synced" all_ready_and_synced
done
check "stream owns 20 ready child clips before deletion" [ "${#hashes[@]}" = 20 ]
denials=$(logs_since "$since" foghorn foghorn-2 | grep 'Thumbnail upload denied' || true)
for hash in "${hashes[@]}"; do
  check "ready clip $hash had no thumbnail upload denial" bash -c '[[ "$1" != *"$2"* ]]' _ "$denials" "$hash"
done

started=$(date +%s)
response=$(gql 'mutation($id:ID!){deleteStream(id:$id){__typename ... on DeleteSuccess{success pending}}}' \
  "$(jq -cn --arg id "$SID" '{id:$id}')")
elapsed=$(($(date +%s) - started))
check "deleteStream returned within 10 s (took ${elapsed}s)" [ "$elapsed" -le 10 ]
check "deleteStream accepted an asynchronous cascade" json_has "$response" \
  '.data.deleteStream.__typename == "DeleteSuccess" and .data.deleteStream.success == true and .data.deleteStream.pending == true'

children_gone() {
  local count
  count=$(pg commodore "SELECT count(*) FROM commodore.clips WHERE tenant_id='$STACK_TENANT_ID' AND stream_id='$UUID'")
  echo "    remaining clip catalog rows: $count"
  [ "$count" = 0 ]
}
eventually 300 "all 20 child clips leave the catalog" children_gone
all_deleted() {
  local hash state
  for hash in "${hashes[@]}"; do
    state=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(status,'') FROM foghorn.artifacts
      WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash'")
    [ "$state" = deleted ] || { echo "    $hash: ${state:-missing}"; return 1; }
  done
}
eventually 300 "all 20 child artifacts are deleted" all_deleted
# The first quick-delete clip can disappear before processing ever puts it on
# the node. A ready child is guaranteed to have a node delete command; send a
# late freeze for that exact deleted artifact to exercise the queued race.
deleted_hash=${hashes[0]}
edge_deleted_child() {
  [ -n "$(log_json "$since" \
    "select(.msg == \"Clip files delete pass complete\" and .clip_hash == \"$deleted_hash\") | .time" edge)" ]
}
eventually 60 "edge completed the ready child clip's delete command" edge_deleted_child
late_freeze_since=$(utc_now)
late_freeze=$(jq -cn --arg node "${STACK_EDGE_A_NODE:-edge-node-1}" --arg hash "$deleted_hash" \
  --arg tenant "$STACK_TENANT_ID" --arg internal "$INTERNAL" \
  --arg path "/data/storage/clips/$INTERNAL/$deleted_hash.mkv" \
  --arg request "stack-late-freeze-$deleted_hash" \
  '{targetNodeId:$node,freeze:{requestId:$request,assetType:"clip",assetHash:$hash,
    tenantId:$tenant,internalName:$internal,localPath:$path}}')
late_delivered=false
for svc in foghorn foghorn-2; do
  result=$(printf '%s' "$late_freeze" | stack_exec "$svc" sh -c \
    'grpcurl -plaintext -max-time 8 -H "authorization: Bearer $SERVICE_TOKEN" -d @ "localhost:$FOGHORN_INTERNAL_GRPC_PORT" foghorn_relay.FoghornRelay/ForwardCommand' 2>/dev/null) || continue
  if json_has "$result" '.delivered == true'; then late_delivered=true; break; fi
done
check 'late freeze request reached the edge through Foghorn relay' [ "$late_delivered" = true ]
late_freeze_handled() {
  [ -n "$(log_json "$late_freeze_since" \
    "select(.msg == \"Freeze ended: the artifact was deleted on this node\" and .asset_hash == \"$deleted_hash\") | .time" edge)" ]
}
[ "$late_delivered" = true ] && eventually 30 'late freeze treated the clip as deleted, not locally lost' late_freeze_handled

storage_empty() {
  local hash
  for hash in "${hashes[@]}"; do
    python3 "$STACK_LIB_DIR/s3-prefix-empty.py" fw-stack-central-primary \
      "clips/$STACK_TENANT_ID/$INTERNAL/$hash" || return 1
    python3 "$STACK_LIB_DIR/s3-prefix-empty.py" fw-stack-central-primary \
      "thumbnails/$hash/" || return 1
  done
}
# Objects known at deletion drain from the cleanup queue within about a minute. A .dtsh attempt dispatched
# in the same instant as the delete can upload and promote after the deletion enqueued its keys; the
# publication ledger collects those after its 15-minute grace, on a sweep that runs every 5 minutes, and the
# cleanup queue drains them on its next 1-minute tick: at most about 21 minutes.
eventually 1320 "all 20 clips' media and thumbnail S3 prefixes are empty" storage_empty
finish
