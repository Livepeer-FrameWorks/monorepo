#!/usr/bin/env bash
# stack-scenario: default
# Clip from a live buffer: create -> ready -> plays through every replica of
# both cells, clip.* webhooks signed, bytes and thumbnails in the origin cell.
# Catches: F8 (clip assigned another cell's durable storage, thumbnails
# dropped cross-cell), N8 (cross-cell artifact refused on non-leaders), clip
# webhooks never exercised on staging.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 || finish
SINCE=$(date +%s)
webhook_endpoint >/dev/null || fail "webhook endpoint at $RECEIVER_HOOK_URL"

S=$(create_stream "stack-clip-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 640x360 15 240)
trap 'kill "$PUB" 2>/dev/null; delete_stream "$SID"' EXIT
eventually 90 "stream live" media_at "${FOGHORN_A_URLS%% *}" "$PB"

# Coverage of a just-started buffer is reported a little behind real time: only
# that refusal is retried.
create_clip() {
  local now
  now=$(date +%s)
  R=$(gql 'mutation($i:CreateClipInput!){createClip(input:$i){__typename ... on Clip{id clipHash playbackId status} ... on ValidationError{message}}}' \
    "$(jq -cn --arg s "$SID" --argjson start $((now - 20)) '{i:{streamId:$s,title:"stack clip",mode:"DURATION",startUnix:$start,duration:10}}')")
  json_has "$R" '.data.createClip.__typename == "Clip"' && return 0
  echo "$R" | grep -q "no source with at least one whole second" && return 1
  echo "    createClip: $(echo "$R" | jq -c '.data.createClip // .errors')"
  return 2
}
sleep 25
eventually 90 "clip request accepted" create_clip
CID=$(echo "$R" | jq -r '.data.createClip.id // empty')
CHASH=$(echo "$R" | jq -r '.data.createClip.clipHash // empty')
CPB=$(echo "$R" | jq -r '.data.createClip.playbackId // empty')
[ -n "$CID" ] || finish

clip_done() {
  local s
  s=$(gql 'query($id:ID!){node(id:$id){... on Clip{status}}}' "$(jq -cn --arg id "$CID" '{id:$id}')" | jq -r '.data.node.status // "?"')
  echo "    clip status: $s"
  case "$s" in done | ready | READY | DONE) return 0 ;; failed | FAILED) return 2 ;; esac
  return 1
}
eventually 240 "clip reaches ready" clip_done
eventually 60 "clip.requested webhook" saw_event "$SINCE" clip.requested
eventually 120 "clip.ready webhook" saw_event "$SINCE" clip.ready
every_replica "clip plays" "$CPB" media_at

cells() {
  pg "$FOGHORN_A_DB" "SELECT COALESCE(has_thumbnails,false),
      COALESCE(origin_cluster_id,'local'),
      COALESCE(storage_cluster_id, origin_cluster_id, 'local'),
      COALESCE(thumbnail_serving_cluster_id, storage_cluster_id, origin_cluster_id, 'local'),
      COALESCE(sync_status,'')
    FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$CHASH'"
}
settled() {
  local row
  row=$(cells)
  echo "    thumbs|origin|storage|thumbnail-serving|sync: $row"
  IFS='|' read -r thumbs origin storage serving sync <<<"$row"
  [ "$thumbs" = t ] && [ "$sync" = synced ] && [ "$storage" = "$origin" ] && [ "$serving" = "$origin" ]
}
eventually 240 "clip bytes and thumbnails stored and served by its origin cell" settled
finish
