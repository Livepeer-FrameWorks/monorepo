#!/usr/bin/env bash
# stack-scenario: default
# Ten clips contend for the same live buffer and processing node. The source
# stays live until each clip is ready and its thumbnail is published.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 || finish
S=$(create_stream "stack-clip-burst-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 640x360 15 420 metadata)
trap 'kill "$PUB" 2>/dev/null; delete_stream "$SID"' EXIT
eventually 90 "source stream live" media_at "${FOGHORN_A_URLS%% *}" "$PB"
sleep 25

start=$(($(date +%s) - 20))
for i in $(seq 1 10); do
  (
    gql 'mutation($i:CreateClipInput!){createClip(input:$i){__typename ... on Clip{id clipHash status} ... on ValidationError{message}}}' \
      "$(jq -cn --arg s "$SID" --argjson start "$start" --argjson n "$i" \
        '{i:{streamId:$s,title:("stack burst " + ($n|tostring)),mode:"DURATION",startUnix:$start,duration:10}}')" \
      >"$STACK_STATE_DIR/burst-$i.json"
  ) &
done
wait

ids=() hashes=()
for i in $(seq 1 10); do
  r=$(cat "$STACK_STATE_DIR/burst-$i.json")
  id=$(echo "$r" | jq -r '.data.createClip.id // empty')
  hash=$(echo "$r" | jq -r '.data.createClip.clipHash // empty')
  if [ -z "$id" ] || [ -z "$hash" ]; then
    fail "clip $i rejected: $r"
  else
    ids+=("$id")
    hashes+=("$hash")
  fi
done
check "all 10 simultaneous clip requests accepted" [ "${#ids[@]}" = 10 ]

all_ready() {
  local id r status pending=0
  for id in "${ids[@]}"; do
    r=$(gql 'query($id:ID!){node(id:$id){... on Clip{status}}}' "$(jq -cn --arg id "$id" '{id:$id}')")
    status=$(echo "$r" | jq -r '.data.node.status // "?"')
    case "$status" in
      ready | READY | done | DONE) ;;
      failed | FAILED) echo "    $id: $status"; return 2 ;;
      *) pending=$((pending + 1)) ;;
    esac
  done
  echo "    ${#ids[@]} clips, $pending pending"
  [ "$pending" = 0 ]
}
[ "${#ids[@]}" = 10 ] && eventually 420 "all 10 clips reach READY" all_ready

all_thumbnails() {
  local hash row
  for hash in "${hashes[@]}"; do
    row=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(has_thumbnails,false), COALESCE(sync_status,'')
      FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash'")
    [ "$row" = 't|synced' ] || { echo "    $hash: ${row:-missing}"; return 1; }
  done
}
[ "${#hashes[@]}" = 10 ] && eventually 240 "all 10 clips have synced thumbnails" all_thumbnails

for hash in "${hashes[@]}"; do
  retries=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(max(retry_count),0) FROM foghorn.processing_jobs
    WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash'")
  check "clip $hash needed at most one processing retry (got ${retries:-?})" [ "${retries:-999}" -le 1 ]
done
finish
