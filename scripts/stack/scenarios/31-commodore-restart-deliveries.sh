#!/usr/bin/env bash
# stack-scenario: manual
# Both Commodore replicas are killed while a new stream's authority delivery to
# cell A is in flight: cell A's Foghorns are paused, so the claimed attempt is
# held open. The deliveries whose holders died must be made again within about
# a minute of their claim, and a single plain publish into cell A, started once
# Commodore serves again, must be served within 60 s.
# Catches: a lost delivery claim (a claim that timed out after the database
# committed it, or a Commodore that stopped mid-attempt) stranding the delivery
# for a two-minute lease, which delayed the cell's placement capability
# attestation and kept Foghorn refusing ingest with "placement authority is not
# ready".
# Manual: pausing both Foghorns of cell A interrupts every other scenario.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl psql || finish
CELL_A_FOGHORNS=(foghorn foghorn-2)
PAUSED=0 PUB="" SID=""
cleanup() {
  [ -n "$PUB" ] && kill "$PUB" 2>/dev/null
  [ "$PAUSED" = 1 ] && stack_ctl unpause "${CELL_A_FOGHORNS[@]}"
  stack_ctl start commodore commodore-2
  [ -n "$SID" ] && delete_stream "$SID"
}
trap cleanup EXIT

streams_listed() { json_has "$(gql '{streamsConnection(page:{first:1}){totalCount}}')" '.data.streamsConnection.totalCount >= 0'; }
# deliveries <status list>: the stream authority's deliveries in those states.
deliveries() {
  local n
  n=$(pg commodore "SELECT count(*) FROM commodore.media_authority_deliveries WHERE authority_kind='media_object' AND authority_id='$AUTHORITY' AND status IN ($1)")
  echo "${n:-0}"
}
in_flight() { [ "$(deliveries "'delivering'")" -ge 1 ]; }
settled() { [ "$(deliveries "'pending','delivering'")" -eq 0 ]; }

stack_ctl pause "${CELL_A_FOGHORNS[@]}" || finish
PAUSED=1
S=$(create_stream "stack-commodore-restart-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream with cell A's Foghorns paused: $S"; finish; }
AUTHORITY="live_stream:$UUID"

eventually 30 "the stream's authority delivery to cell A is in flight" in_flight || finish
CLAIMED=$(date +%s)
stack_ctl kill commodore commodore-2 || finish
stack_ctl unpause "${CELL_A_FOGHORNS[@]}" || finish
PAUSED=0
stack_ctl start commodore commodore-2 || finish
eventually 90 "Commodore serves again" streams_listed || finish

# The lease is about one attempt (under a minute) from the claim; the restart
# and the redelivery itself fit in the rest.
remaining=$((CLAIMED + 75 - $(date +%s)))
[ "$remaining" -gt 0 ] || remaining=1
eventually "$remaining" "the deliveries left by the killed replicas are made within 75 s of their claim" settled

PUB=$(publish "$EDGE_A_RTMP" "$KEY" 640x360 15 120)
eventually 60 "a single plain publish into cell A is served within 60 s" media_at "${FOGHORN_A_URLS%% *}" "$PB"
finish
