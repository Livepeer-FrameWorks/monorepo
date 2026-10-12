#!/usr/bin/env bash
# stack-scenario: default
# Bridge runs two replicas (docker-compose.stack.yml: bridge-2). An API token
# holds a tenantEvents WebSocket on each; revoking the token through bridge
# closes both with 4403 within two seconds of the revocation's answer, and
# neither delivers the events of a stream created three seconds later. Then a
# demo session holds one on each and signs out through bridge: both close
# with 4403 within two seconds, the signed-out token is refused at
# connection_init, bridge-2 answers it with 401 over HTTP, and a session
# started after the sign-out connects and is served.
# Catches: a revocation closing only the revoking replica's sockets while the
# other replica's keep serving tenant events until its one-minute credential
# re-check (staging: 49-60 s); a sign-out leaving the session's sockets open,
# reconnectable, or served over HTTP until the access token expires (up to
# 15 minutes).
. "$(dirname "$0")/../lib.sh"

REPO=${STACK_REPO:-/repo}
BRIDGE_2_WS_URL=${BRIDGE_2_WS_URL:-ws://bridge-2:18000/graphql/ws}
BOUND_S=2
need jq curl python3 || finish
if ! stack_running bridge-2; then
  blocked "bridge-2 is not running; the stack has a single Bridge replica"
  finish
fi
python3 -c 'import websockets' 2>/dev/null || {
  blocked "the scenario needs python websockets in stack-runner"
  finish
}

log "API token sockets on both Bridge replicas, revoked through bridge"
T=$(gql_session 'mutation($i:CreateDeveloperTokenInput!){createDeveloperToken(input:$i){__typename ... on DeveloperToken{id tokenValue}}}' \
  "$(jq -cn '{i:{name:"stack ws revocation",permissions:"streams:read"}}')")
TID=$(echo "$T" | jq -r '.data.createDeveloperToken.id // empty')
TVAL=$(echo "$T" | jq -r '.data.createDeveloperToken.tokenValue // empty')
[ -n "$TID" ] || {
  fail "createDeveloperToken: $T"
  finish
}
JWT=$(session_token) || {
  fail "demo session login"
  finish
}

OUT=$(python3 "$REPO/scripts/stack/ws-token-revocation.py" "$BRIDGE_URL/graphql" "$JWT" "$TID" "$TVAL" "$BRIDGE_WS_URL" "$BRIDGE_2_WS_URL" 2>&1)
log "result: $OUT"
if ! json_has "$OUT" '.sockets | length == 2'; then
  fail "the WebSocket probe did not finish: $OUT"
  finish
fi
SID=$(echo "$OUT" | jq -r '.stream_id // empty')
[ -n "$SID" ] && delete_stream "$SID"

for i in 0 1; do
  url=$(echo "$OUT" | jq -r ".sockets[$i].url")
  check "socket on $url closed with 4403" json_has "$OUT" ".sockets[$i].code == 4403"
  check "socket on $url closed within ${BOUND_S}s of the revocation" json_has "$OUT" \
    ".sockets[$i].closed_after_s != null and .sockets[$i].closed_after_s <= $BOUND_S"
  check "socket on $url delivered no event after the revocation" json_has "$OUT" ".sockets[$i].events_after_revoke == []"
done

# Runs after the API-token part: signing out ends every session of the demo
# user, so a session token issued before it (this scenario's cached one
# included) no longer opens WebSockets or refreshes. Later logins are new
# sessions and unaffected.
log "session sockets on both Bridge replicas, signed out through bridge"
LOGIN=$(jq -cn --arg e "$STACK_DEMO_EMAIL" --arg p "$STACK_DEMO_PASSWORD" --argjson b "$(bot_fields)" '{email:$e, password:$p} + $b')
OUT=$(python3 "$REPO/scripts/stack/ws-session-signout.py" "$BRIDGE_URL" "$LOGIN" "$BRIDGE_WS_URL" "$BRIDGE_2_WS_URL" 2>&1)
log "result: $OUT"
if ! json_has "$OUT" '.sockets | length == 2'; then
  fail "the sign-out probe did not finish: $OUT"
  finish
fi
SID=$(echo "$OUT" | jq -r '.stream_id // empty')
[ -n "$SID" ] && delete_stream "$SID"

for i in 0 1; do
  url=$(echo "$OUT" | jq -r ".sockets[$i].url")
  check "session socket on $url closed with 4403" json_has "$OUT" ".sockets[$i].code == 4403"
  check "session socket on $url closed within ${BOUND_S}s of the sign-out" json_has "$OUT" \
    ".sockets[$i].closed_after_s != null and .sockets[$i].closed_after_s <= $BOUND_S"
  check "session socket on $url delivered no event after the sign-out" json_has "$OUT" ".sockets[$i].events_after_signout == []"
done
check "the signed-out session token is refused at connection_init with 4403" json_has "$OUT" \
  '.reconnect_signed_out.first == "close" and .reconnect_signed_out.code == 4403'
check "a session started after the sign-out connects" json_has "$OUT" '.reconnect_new_session.first == "connection_ack"'
check "bridge-2, which did not handle the sign-out, answers the signed-out token with 401 over HTTP" json_has "$OUT" '.http_signed_out == 401'
check "bridge-2 serves a session started after the sign-out over HTTP" json_has "$OUT" '.http_new_session == 200'
finish
