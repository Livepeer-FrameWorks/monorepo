#!/usr/bin/env bash
# stack-scenario: default
# A stored artifact of a tenant on a paid tier shows what keeping it costs: the
# demo tenant's developer tier prices cold storage, so a ready clip's
# storageCost is a positive per-month projection in the tier's currency.
# Catches: F8 (storageCost null for every artifact; never exercised on a
# priced tenant on staging).
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl || finish

S=$(create_stream "stack-storage-cost-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 640x360 15 150)
trap 'kill "$PUB" 2>/dev/null; delete_stream "$SID"' EXIT
eventually 90 "stream live" media_at "${FOGHORN_A_URLS%% *}" "$PB"

create_clip() {
  local now
  now=$(date +%s)
  R=$(gql 'mutation($i:CreateClipInput!){createClip(input:$i){__typename ... on Clip{id clipHash} ... on ValidationError{message}}}' \
    "$(jq -cn --arg s "$SID" --argjson start $((now - 20)) '{i:{streamId:$s,title:"stack storage cost",mode:"DURATION",startUnix:$start,duration:10}}')")
  json_has "$R" '.data.createClip.__typename == "Clip"' && return 0
  echo "$R" | grep -q "no source with at least one whole second" && return 1
  echo "    createClip: $(echo "$R" | jq -c '.data.createClip // .errors')"
  return 2
}
sleep 25
eventually 90 "clip request accepted" create_clip
CHASH=$(echo "$R" | jq -r '.data.createClip.clipHash // empty')
[ -n "$CHASH" ] || finish

cost_projected() {
  # Tier pricing is billing data: a session (or a token with billing:read) sees it.
  C=$(gql_session 'query($i:StorageArtifactsInput){storageArtifactsConnection(input:$i){nodes{status sizeBytes storageCost{perDay perMonth currency}}}}' \
    "$(jq -cn --arg h "$CHASH" '{i:{artifactHash:$h,first:1}}')")
  echo "    clip: $(echo "$C" | jq -c '.data.storageArtifactsConnection.nodes[0] // .errors // .')"
  json_has "$C" '.data.storageArtifactsConnection.nodes[0] | (.sizeBytes // 0) > 0 and .storageCost != null and .storageCost.perMonth > 0 and (.storageCost.currency | length) == 3'
}
eventually 240 "the ready clip projects a storage cost for the paid tier" cost_projected
finish
