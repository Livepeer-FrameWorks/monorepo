#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, stops destination Foghorn replicas to prove durable revocation
# Deletion closes a real encoder, including when its owning cell is temporarily
# unreachable. The catalog identity must survive until the stop is confirmed.
. "$(dirname "$0")/../lib.sh"
need ffmpeg jq psql docker || finish
[ "${STACK_TARGET:-stack}" != staging ] || { blocked 'requires an isolated stack'; finish; }
PUB='' SID='' STOPPED=''
cleanup() {
  local service
  for service in $STOPPED; do stack_ctl start "$service" || fail "restore $service"; done
  [ -z "$PUB" ] || kill "$PUB" 2>/dev/null || true
  [ -z "$SID" ] || delete_stream "$SID"
}
trap cleanup EXIT
publisher_ended() { ! kill -0 "$PUB" 2>/dev/null; }
stop_settled() {
  local pending
  pending=$(pg commodore "SELECT count(*) FROM commodore.media_authority_refresh_obligations
    WHERE tenant_id='$STACK_TENANT_ID' AND target_kind='live_stream' AND target_key='media_object:live_stream:$UUID'
      AND lane='ingest_revocation' AND status<>'completed'") || return 1
  [ "$pending" = 0 ] && publisher_ended
}
for mode in reachable unavailable; do
  S=$(create_stream "stack-delete-live-$mode-$(date +%s)" false OFF)
  SID=$(jq -r '.id // empty' <<<"$S")
  UUID=$(jq -r '.streamId // empty' <<<"$S")
  KEY=$(jq -r '.streamKey // empty' <<<"$S")
  [ -n "$SID" ] && [ -n "$UUID" ] && [ -n "$KEY" ] || { fail 'create deletion fixture'; finish; }
  INTERNAL=$(internal_name_of "$UUID")
  if [ "$mode" = reachable ]; then EDGE=$EDGE_A_RTMP; DB=$FOGHORN_A_DB; else EDGE=$EDGE_B_RTMP; DB=$FOGHORN_B_DB; fi
  started=$(date +%s)
  ffmpeg -hide_banner -loglevel error -re -f lavfi -i 'testsrc2=size=640x360:rate=15' \
    -f lavfi -i 'sine=frequency=440:sample_rate=48000' -t 600 -c:v libx264 -preset ultrafast \
    -threads 1 -g 30 -pix_fmt yuv420p -c:a aac -flvflags no_metadata -f flv "$EDGE/$KEY" \
    >"$STACK_STATE_DIR/delete-live-$mode.log" 2>&1 &
  PUB=$!
  admitted() {
    kill -0 "$PUB" 2>/dev/null || return 2
    [ -n "$(admitted_generation "$DB" "$INTERNAL" "$started")" ]
  }
  eventually 60 "$mode publisher admitted on its single connection" admitted || finish
  GENERATION=$(open_generation "$DB" "$INTERNAL")
  [ -n "$GENERATION" ] || { fail 'publisher generation missing'; finish; }
  if [ "$mode" = unavailable ]; then
    STOPPED='foghorn-b foghorn-b-2'
    for service in $STOPPED; do stack_ctl stop "$service" || finish; done
    check 'publisher stays connected while its owning cell is unreachable' kill -0 "$PUB"
  fi
  sweep_ledger deleted "$SID" ''
  response=$(gql 'mutation($id:ID!){deleteStream(id:$id){__typename ... on DeleteSuccess{success pending}}}' "$(jq -cn --arg id "$SID" '{id:$id}')")
  check "$mode deletion accepted" json_has "$response" '.data.deleteStream.__typename=="DeleteSuccess" and .data.deleteStream.success==true'
  if [ "$mode" = unavailable ]; then
    stop_pending() {
      [ "$(pg commodore "SELECT count(*) FROM commodore.streams s WHERE s.tenant_id='$STACK_TENANT_ID'
        AND s.id='$UUID' AND s.deleted_at IS NOT NULL AND EXISTS (
          SELECT 1 FROM commodore.media_authority_refresh_obligations o WHERE o.tenant_id=s.tenant_id
            AND o.target_kind='live_stream' AND o.target_key='media_object:live_stream:'||s.id::text
            AND o.lane='ingest_revocation' AND o.status<>'completed')")" = 1 ]
    }
    eventually 15 'unreachable cell keeps the deleted identity and durable stop pending' stop_pending || finish
    check 'unreachable cell has not falsely confirmed the encoder stop' kill -0 "$PUB"
    for service in $STOPPED; do stack_ctl start "$service" || finish; done
    STOPPED=''
  fi
  eventually 120 "$mode deletion ends the actual encoder" publisher_ended || finish
  eventually 120 "$mode durable revocation confirms the encoder stop" stop_settled || finish
  session_ended() {
    [ "$(pg "$DB" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID'
      AND id='$GENERATION' AND ended_at IS NOT NULL")" = 1 ]
  }
  eventually 60 "$mode retired publisher generation is ended" session_ended || finish
  catalog_deleted() { [ "$(pg commodore "SELECT count(*) FROM commodore.streams WHERE tenant_id='$STACK_TENANT_ID' AND id='$UUID'")" = 0 ]; }
  eventually 120 "$mode catalog finalizes after the confirmed stop" catalog_deleted || finish
  wait "$PUB" 2>/dev/null || true
  PUB='' SID=''
done
finish
