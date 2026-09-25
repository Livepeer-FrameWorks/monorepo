#!/usr/bin/env bash
# stack-scenario: default
# Placement stays correct across a Foghorn rehydrate tick: every viewer and
# ingest resolve through every Foghorn replica of both cells succeeds on the
# first attempt for longer than one 180 s reconcile interval.
# Catches: F1 intermittent 503s, N6 (rehydrate zeroed OutputsObservedAt, every
# node stale for ~10 s each tick), N8 on the playback path (non-leader replicas).
. "$(dirname "$0")/../lib.sh"

WINDOW=${STACK_PLACEMENT_SECONDS:-200}
need ffmpeg jq curl || finish

log "stream + publisher on cell A"
S=$(create_stream "stack-placement-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 640x360 15 $((WINDOW + 120)))
trap 'kill "$PUB" 2>/dev/null; delete_stream "$SID"' EXIT

first_replica=${FOGHORN_A_URLS%% *}
eventually 90 "stream live and resolvable at $first_replica" media_at "$first_replica" "$PB"

log "strict resolves for ${WINDOW}s through every replica (no retries)"
declare -A ok_count bad_count
deadline=$(($(date +%s) + WINDOW))
round=0
while [ "$(date +%s)" -lt "$deadline" ]; do
  round=$((round + 1))
  for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
    code=$(play_status "$base" "$PB")
    if [ "$code" = 307 ] || [ "$code" = 302 ]; then
      ok_count[$base]=$((${ok_count[$base]:-0} + 1))
    else
      bad_count[$base]=$((${bad_count[$base]:-0} + 1))
      body=$(curl -s -m 10 "$base/play/$PB/hls" | head -c 200)
      printf '  round %d %s -> %s %s\n' "$round" "$base" "$code" "$body"
    fi
  done
  for base in $FOGHORN_A_URLS; do
    ing=$(curl -s -m 10 -w ' %{http_code}' "$base/ingest/$KEY?protocol=rtmp")
    case "$ing" in
      *' 200') echo "${ing% 200}" | jq -e '.primary.rtmpUrl' >/dev/null 2>&1 || bad_count[ingest:$base]=$((${bad_count[ingest:$base]:-0} + 1)) ;;
      *) bad_count[ingest:$base]=$((${bad_count[ingest:$base]:-0} + 1)); printf '  round %d ingest %s -> %s\n' "$round" "$base" "${ing##* }" ;;
    esac
  done
  sleep 1
done
for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
  if [ "${bad_count[$base]:-0}" = 0 ] && [ "${ok_count[$base]:-0}" -gt 0 ]; then
    pass "viewer resolve via $base: ${ok_count[$base]} of ${ok_count[$base]} over ${WINDOW}s"
  else
    fail "viewer resolve via $base: ${bad_count[$base]:-0} refused, ${ok_count[$base]:-0} ok"
  fi
done
for base in $FOGHORN_A_URLS; do
  check "ingest resolve via $base never refused (rounds: $round)" [ "${bad_count[ingest:$base]:-0}" = 0 ]
done

log "a resolved session serves real media from both cells"
for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
  check "real TS segment via $base" media_at "$base" "$PB"
done
finish
