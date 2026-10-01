#!/usr/bin/env bash
# stack-scenario: manual
# Fresh publisher admissions on both cells, followed by an active-session
# check after each publisher exits. A timed-out admission must not leave an
# open generation that later refuses a reconnect as a duplicate.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 || finish
STREAMS=() PUBS=() INTERNALS_A=() INTERNALS_B=()
cleanup() {
  local pid id
  for pid in "${PUBS[@]}"; do kill "$pid" 2>/dev/null; done
  for id in "${STREAMS[@]}"; do delete_stream "$id"; done
}
trap cleanup EXIT
since=$(utc_now)

for n in $(seq 1 20); do
  s=$(create_stream "stack-fresh-ingest-$n-$(date +%s)" false)
  id=$(echo "$s" | jq -r '.id // empty')
  uuid=$(echo "$s" | jq -r '.streamId // empty')
  pb=$(echo "$s" | jq -r '.playbackId // empty')
  key=$(echo "$s" | jq -r '.streamKey // empty')
  [ -n "$id" ] || { fail "ingest $n: createStream: $s"; continue; }
  STREAMS+=("$id")
  internal=$(internal_name_of "$uuid")
  if [ $((n % 2)) = 0 ]; then
    edge=$EDGE_B_RTMP edge_service=$STACK_EDGE_B_SERVICE cell=$FOGHORN_B_DB foghorn=${FOGHORN_B_URLS%% *}
    INTERNALS_B+=("$internal")
  else
    edge=$EDGE_A_RTMP edge_service=$STACK_EDGE_A_SERVICE cell=$FOGHORN_A_DB foghorn=${FOGHORN_A_URLS%% *}
    INTERNALS_A+=("$internal")
  fi
  stream_since=$(utc_now)
  pid=$(publish "$edge" "$key" 320x240 10 90)
  PUBS+=("$pid")
  eventually 60 "ingest $n admitted and serves media on first publisher" media_at "$foghorn" "$pb"
  check "ingest $n publisher remains connected" kill -0 "$pid"
  mist_active() { log_has "$stream_since" "Stream live\\+$internal became active" "$edge_service"; }
  eventually 30 "ingest $n is active in Mist" mist_active
  active() { [ -n "$(open_generation "$cell" "$internal")" ]; }
  eventually 30 "ingest $n has one active generation" active
  count=$(pg "$cell" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID'
    AND stream_internal_name IN ('$internal','live+$internal') AND ended_at IS NULL AND projection_state='active'")
  check "ingest $n has exactly one active session (got $count)" [ "$count" = 1 ]
  kill "$pid" 2>/dev/null
  ended() { [ -z "$(open_generation "$cell" "$internal")" ]; }
  eventually 90 "ingest $n session ends after the publisher disconnects" ended
  mist_inactive() { log_has "$stream_since" "Stream live\\+$internal became inactive" "$edge_service"; }
  eventually 90 "ingest $n is inactive in Mist after disconnect" mist_inactive
  delete_stream "$id"
done

if log_has "$since" 'DUPLICATE_INGEST|duplicate ingest' edge edge-b foghorn foghorn-2 foghorn-b foghorn-b-2; then
  fail "a fresh ingest was refused as a duplicate"
else
  pass "no duplicate-ingest refusal across 20 fresh publishers"
fi
for cell in "$FOGHORN_A_DB" "$FOGHORN_B_DB"; do
  if [ "$cell" = "$FOGHORN_A_DB" ]; then names=("${INTERNALS_A[@]}"); else names=("${INTERNALS_B[@]}"); fi
  for internal in "${names[@]}"; do
    count=$(pg "$cell" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID'
      AND ended_at IS NULL AND projection_state='active' AND stream_internal_name IN ('$internal','live+$internal')")
    check "$cell has no late ghost session for $internal (got $count)" [ "$count" = 0 ]
  done
done
finish
