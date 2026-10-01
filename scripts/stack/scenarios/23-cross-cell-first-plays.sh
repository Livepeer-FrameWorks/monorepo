#!/usr/bin/env bash
# stack-scenario: manual
# A new stream forces a new origin-pull arrangement. Twenty distinct streams
# per direction make the first cross-cell segment, not a warmed viewer, the
# assertion. This is manual because forty publishers and cleanup take time.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 || finish
since=$(utc_now)
STREAMS=() PUBS=()
cleanup() {
  local pid id
  for pid in "${PUBS[@]}"; do kill "$pid" 2>/dev/null; done
  for id in "${STREAMS[@]}"; do delete_stream "$id"; done
}
trap cleanup EXIT

read -r -a A_BASES <<<"$FOGHORN_A_URLS"
read -r -a B_BASES <<<"$FOGHORN_B_URLS"

for n in $(seq 1 20); do
  for direction in a-to-b b-to-a; do
    s=$(create_stream "stack-first-$direction-$n-$(date +%s)" false)
    id=$(echo "$s" | jq -r '.id // empty')
    uuid=$(echo "$s" | jq -r '.streamId // empty')
    pb=$(echo "$s" | jq -r '.playbackId // empty')
    key=$(echo "$s" | jq -r '.streamKey // empty')
    [ -n "$id" ] || { fail "$direction $n: createStream: $s"; continue; }
    STREAMS+=("$id")
    internal=$(internal_name_of "$uuid")
    if [ "$direction" = a-to-b ]; then
      source_edge=$EDGE_A_RTMP
      source_foghorn=${A_BASES[0]}
      dest=${B_BASES[$(( (n - 1) % ${#B_BASES[@]} ))]}
    else
      source_edge=$EDGE_B_RTMP
      source_foghorn=${B_BASES[0]}
      dest=${A_BASES[$(( (n - 1) % ${#A_BASES[@]} ))]}
    fi
    stream_since=$(utc_now)
    pid=$(publish "$source_edge" "$key" 320x240 10 90)
    PUBS+=("$pid")
    if ! eventually 60 "$direction $n: source has media" media_at "$source_foghorn" "$pb"; then
      continue
    fi
    loc=$(location_at "$dest" "$pb")
    if [ -z "$loc" ]; then
      fail "$direction $n: first cross-cell resolve via $dest returned no redirect"
      continue
    fi
    got=$(segment_fetch "$loc")
    case "$got" in
      200\ *)
        if [ "${got#200 }" -gt 0 ]; then
          pass "$direction $n: first cross-cell segment via $dest is 200 with TS bytes"
        else
          fail "$direction $n: first cross-cell segment via $dest has no TS bytes ($got)"
        fi ;;
      *) fail "$direction $n: first cross-cell segment via $dest returned $got" ;;
    esac
    sleep 0.25
    if [ "$direction" = a-to-b ]; then
      destination_services=(foghorn-b foghorn-b-2)
    else
      destination_services=(foghorn foghorn-2)
    fi
    cleared=$(log_json "$stream_since" \
      "select(.msg == \"Node lifecycle cleared stale replicated stream\" and .internal_name == \"$internal\") | .time" \
      "${destination_services[@]}")
    check "$direction $n: no lifecycle eviction of the arranged live pull" [ -z "$cleared" ]
    kill "$pid" 2>/dev/null
    delete_stream "$id"
  done
done
check "cross-cell origin pull arrangements were logged" log_has "$since" 'Origin-pull arranged' foghorn foghorn-2 foghorn-b foghorn-b-2
finish
