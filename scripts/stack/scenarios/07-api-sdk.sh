#!/usr/bin/env bash
# stack-scenario: default
# staging: safe
# tenant: qa
# Public API surface: the TS, Go and Python SDKs against the stack (queries,
# mutations, tenantEvents with a bearer token and no Origin), key rotation and
# API token webhooks, attemptHistory in the delivery list, and webhook
# integrity (valid signatures, unique ids).
# Catches: F5 (backends without Origin refused on the WS), F13 (attemptHistory
# empty in the list query), stream.key_rotated / api_token.* never exercised
# on staging, SDK/contract drift.
. "$(dirname "$0")/../lib.sh"

REPO=${STACK_REPO:-/repo}
need jq curl python3 ffmpeg || finish
SINCE=$(date +%s)
webhook_endpoint >/dev/null || fail "webhook endpoint at $RECEIVER_HOOK_URL"

log "TypeScript SDK"
if command -v node >/dev/null && [ -f "$REPO/npm_api/dist/index.js" ]; then
  if node "$REPO/scripts/stack/sdk/smoke.mjs" "$REPO" "$BRIDGE_URL/graphql" "$BRIDGE_WS_URL" "$STACK_API_TOKEN"; then
    pass "TS SDK smoke"
  else
    fail "TS SDK smoke"
  fi
else
  blocked "TS SDK smoke needs node and a built $REPO/npm_api/dist"
fi

log "Bridge MCP query generation"
if bash "$REPO/scripts/stack/sdk/mcp-smoke.sh"; then
  pass "MCP generate_query -> execute_query"
else
  mcp_result=$?
  if [ "$mcp_result" = 2 ]; then
    blocked "MCP smoke prerequisites unavailable"
  else
    fail "MCP generate_query -> execute_query"
  fi
fi

log "Python SDK"
if PYTHONPATH="$REPO/sdk_python/src" python3 -c 'import livepeer_frameworks' 2>/dev/null; then
  if PYTHONPATH="$REPO/sdk_python/src" python3 "$REPO/scripts/stack/sdk/smoke.py" "$BRIDGE_URL/graphql" "$BRIDGE_WS_URL" "$STACK_API_TOKEN"; then
    pass "Python SDK smoke"
  else
    fail "Python SDK smoke"
  fi
else
  blocked "Python SDK smoke needs livepeer_frameworks' dependencies (httpx, pydantic, websockets) in stack-runner"
fi

log "Go SDK"
if command -v go >/dev/null; then
  # The module's replace paths are relative to its place in the repo; the repo
  # is mounted read-only, so the smoke runs from a copy at the same depth.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/scripts/stack/sdk"
  cp -r "$REPO/scripts/stack/sdk/gosmoke" "$tmp/scripts/stack/sdk/"
  ln -s "$REPO/pkg" "$tmp/pkg"
  ln -s "$REPO/sdk_go" "$tmp/sdk_go"
  if (cd "$tmp/scripts/stack/sdk/gosmoke" && go run . "$BRIDGE_URL/graphql" "$BRIDGE_WS_URL" "$STACK_API_TOKEN"); then
    pass "Go SDK smoke"
  else
    fail "Go SDK smoke"
  fi
  rm -rf "$tmp"
else
  blocked "Go SDK smoke needs a Go toolchain in stack-runner"
fi

log "live ingest controls and key rotation webhooks"
API_PUB='' API_SID=''
cleanup_api_ingest() {
  [ -z "$API_PUB" ] || kill "$API_PUB" 2>/dev/null || true
  [ -z "$API_SID" ] || delete_stream "$API_SID"
}
trap cleanup_api_ingest EXIT
S=$(create_stream "stack-api-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
API_SID=$SID
if [ -n "$SID" ]; then
  vars=$(jq -cn --arg id "$SID" '{id:$id}')
  key=$(echo "$S" | jq -r .streamKey)
  raw_stream=$(echo "$S" | jq -r .streamId)
  ingest() { gql 'query($id:ID!){stream(id:$id){ingest{state isLive sessionId}}}' "$vars"; }
  ingest_live() { json_has "$(ingest)" '.data.stream.ingest.isLive == true and .data.stream.ingest.sessionId != null'; }
  publisher_gone() { ! kill -0 "$API_PUB" 2>/dev/null; }
  before=$(monotonic_ms)
  API_PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$key" 640x360 15 600) || { fail 'publisher admitted for API controls'; finish; }
  eventually 60 'Stream.ingest becomes LIVE' ingest_live || finish
  session=$(ingest | jq -r '.data.stream.ingest.sessionId')
  R=$(gql 'mutation($id:ID!){refreshStreamKey(id:$id){__typename ... on Stream{streamKey}}}' "$vars")
  next_key=$(echo "$R" | jq -r '.data.refreshStreamKey.streamKey // empty')
  check "refreshStreamKey returns a new key" [ -n "$next_key" ]
  check "refreshStreamKey replaces the credential" [ "$key" != "$next_key" ]
  check 'default rotation keeps the current publisher' kill -0 "$API_PUB"
  check 'default rotation keeps the same session' json_has "$(ingest)" ".data.stream.ingest.sessionId == \"$session\" and .data.stream.ingest.isLive == true"
  eventually 60 "stream.key_rotated webhook" saw_event "$SINCE" stream.key_rotated
  R=$(gql 'mutation($id:ID!){disconnectIngest(streamId:$id){__typename ... on IngestDisconnectSuccess{stopRequested}}}' "$vars")
  check 'disconnect persists a stop request' json_has "$R" '.data.disconnectIngest.stopRequested == true'
  eventually 90 'disconnect ends the running publisher before its lifetime expires' publisher_stopped_before_deadline "$((before+585000))" publisher_gone || finish
  eventually 60 'disconnect event carries the stopped session' saw_event "$SINCE" stream.ingest_disconnected ".streamId == \"$raw_stream\" and .sessionId == \"$session\" and .reason == \"requested\""

  before=$(monotonic_ms)
  API_PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$next_key" 640x360 15 600) || { fail 'encoder reconnects with the retained key'; finish; }
  eventually 60 'reconnected publisher becomes LIVE' ingest_live || finish
  session=$(ingest | jq -r '.data.stream.ingest.sessionId')
  R=$(gql 'mutation($id:ID!){refreshStreamKey(id:$id,endActiveSession:true){__typename ... on Stream{streamKey}}}' "$vars")
  newest_key=$(echo "$R" | jq -r '.data.refreshStreamKey.streamKey // empty')
  check 'rotate-and-end returns its new credential' [ -n "$newest_key" ]
  check 'rotate-and-end replaces the retained key' [ "$newest_key" != "$next_key" ]
  eventually 90 'rotate-and-end ends the current publisher' publisher_stopped_before_deadline "$((before+585000))" publisher_gone || finish
  eventually 60 'rotate-and-end event identifies the captured session' saw_event "$SINCE" stream.ingest_disconnected ".streamId == \"$raw_stream\" and .sessionId == \"$session\" and .reason == \"key_rotated\""
  cleanup_api_ingest
  API_PUB='' API_SID=''
else
  fail "createStream: $S"
fi
trap - EXIT

log "API token webhooks"
T=$(gql_session 'mutation($i:CreateDeveloperTokenInput!){createDeveloperToken(input:$i){__typename ... on DeveloperToken{id tokenValue}}}' \
  "$(jq -cn '{i:{name:"stack token",permissions:"streams:read"}}')")
TID=$(echo "$T" | jq -r '.data.createDeveloperToken.id // empty')
TVAL=$(echo "$T" | jq -r '.data.createDeveloperToken.tokenValue // empty')
if [ -n "$TID" ]; then
  check "the new token authenticates" json_has "$(gql '{streamsConnection(page:{first:1}){totalCount}}' '{}' "$TVAL")" '.data.streamsConnection.totalCount >= 0'
  eventually 60 "api_token.created webhook" saw_event "$SINCE" api_token.created ".tokenId == \"$TID\""
  R=$(gql_session 'mutation($id:ID!){revokeDeveloperToken(id:$id){__typename}}' "$(jq -cn --arg id "$TID" '{id:$id}')")
  check "revokeDeveloperToken succeeds" json_has "$R" '.data.revokeDeveloperToken.__typename == "DeleteSuccess"'
  eventually 60 "api_token.revoked webhook" saw_event "$SINCE" api_token.revoked ".tokenId == \"$TID\""
  revoked_refused() { ! json_has "$(gql '{streamsConnection(page:{first:1}){totalCount}}' '{}' "$TVAL")" '.data.streamsConnection.totalCount >= 0'; }
  check "the revoked token is refused" revoked_refused
else
  fail "createDeveloperToken: $T"
fi

log "delivery list carries attempt history; webhook integrity"
EP=$(webhook_endpoint | awk '{print $1}')
D=$(gql_session 'query($e:ID!){webhookDeliveriesConnection(endpointId:$e, page:{first:20}){nodes{id eventType status attempts attemptHistory{attemptNumber statusCode}}}}' "$(jq -cn --arg e "$EP" '{e:$e}')")
check "listed deliveries include attempt history for delivered events" json_has "$D" \
  '[.data.webhookDeliveriesConnection.nodes[] | select(.attempts > 0)] | length > 0 and all(.attemptHistory | length > 0)'
EV=$(webhook_events "$SINCE")
check "every received delivery has a valid signature" bash -c '! grep -q " INVALID " <<<"$0"' "$EV"
dups=$(echo "$EV" | awk '{print $2}' | sort | uniq -d | wc -l | tr -d ' ')
# Retries reuse the webhook-id; a repeat only counts when the receiver answered 2xx both times.
check "no webhook-id delivered twice after a 2xx ($dups repeats)" [ "$dups" = 0 ]
finish
