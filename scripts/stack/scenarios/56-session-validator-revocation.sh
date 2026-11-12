#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, inserts disposable session fixtures and restarts Bridge replicas
# Named revocation reaches every JWT ingress, without rejecting another device
# of the same operator. Direct state fixtures isolate validation from issuance.
. "$(dirname "$0")/../lib.sh"

need jq curl python3 psql || finish
WORK=$(mktemp -d)
chmod 700 "$WORK"
tenant="" user="" revoked="" other="" extra=""
descriptor=""
FAULTS=()
must_check() {
  local before=$FAIL
  check "$@"
  [ "$FAIL" = "$before" ]
}
profile_matches() { jq -e --arg user "$user" '.id == $user' "$1" >/dev/null; }
rpc_error() { sed -n '1,5p' "$1"; }
cleanup() {
  local fault
  for fault in "${FAULTS[@]}"; do netfault_stop "$fault"; done
  FAULTS=()
  if [ -n "$tenant" ] && [ -n "$revoked" ]; then
    pg_fixture commodore "DELETE FROM commodore.auth_sessions WHERE tenant_id='$tenant' AND user_id='$user' AND id IN ('$revoked','$other','$extra')" >/dev/null || fail 'session fixture cleanup'
  fi
  if [ -n "$descriptor" ]; then
    for service in commodore commodore-2; do
      stack_exec "$service" sh -c 'rm -f "$1" "$1"-*' sh "$descriptor" >/dev/null 2>&1 || fail "descriptor cleanup on $service"
    done
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

code=$(jq -cn --argjson bot "$(bot_fields)" '{email:"operator@stack.test",password:"stack-operator-password"} + $bot' |
  curl -s -m 30 -c "$WORK/operator.cookies" -o /dev/null -w '%{http_code}' \
    -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login")
check 'operator fixture signs in' [ "$code" = 200 ] || finish
awk '$6=="access_token"{print $7}' "$WORK/operator.cookies" | tail -1 >"$WORK/base.jwt"

# Only the disposable stack exposes its signing key. Preserve the real login's
# owner/privileges while assigning two fixture rows independent session IDs.
python3 - "$WORK" <<'PY' || { fail 'create signed session fixtures'; finish; }
import base64, hashlib, hmac, json, os, pathlib, sys, time, uuid
work = pathlib.Path(sys.argv[1])
token = (work / 'base.jwt').read_text().strip()
claims = json.loads(base64.urlsafe_b64decode(token.split('.')[1] + '=='))
tenant, user = str(uuid.UUID(claims['tenant_id'])), str(uuid.UUID(claims['user_id']))
key = os.environ['JWT_SECRET'].encode()
assert key and 'platform_operator' in claims['roles']
def enc(value):
    return base64.urlsafe_b64encode(value).rstrip(b'=')
ids = [str(uuid.uuid4()), str(uuid.uuid4()), str(uuid.uuid4())]
for name, sid in zip(('revoked', 'other', 'extra'), ids):
    payload = dict(claims, sid=sid, exp=int(time.time()) + 600)
    body = enc(b'{"alg":"HS256","typ":"JWT"}') + b'.' + enc(json.dumps(payload).encode())
    signed = body + b'.' + enc(hmac.new(key, body, hashlib.sha256).digest())
    (work / (name + '.jwt')).write_bytes(signed)
(work / 'owner').write_text('\n'.join((tenant, user, *ids)) + '\n')
PY
{ read -r tenant; read -r user; read -r revoked; read -r other; read -r extra; } <"$WORK/owner"
descriptor="/tmp/stack-session-$revoked.pb"
pg_fixture commodore "INSERT INTO commodore.auth_sessions
  (id,tenant_id,user_id,auth_method,client_id,device_label,idle_expires_at,absolute_expires_at)
  VALUES ('$revoked','$tenant','$user','password','web','Stack validator',NOW()+INTERVAL '1 hour',NOW()+INTERVAL '2 hours'),
         ('$other','$tenant','$user','password','web','Stack other device',NOW()+INTERVAL '1 hour',NOW()+INTERVAL '2 hours'),
         ('$extra','$tenant','$user','password','web','Stack third device',NOW()+INTERVAL '1 hour',NOW()+INTERVAL '2 hours')" >/dev/null || { fail 'insert session fixtures'; finish; }

for service in commodore commodore-2; do
  { cat "$WORK/revoked.jwt"; echo; } | stack_exec "$service" sh -c '
    read -r token
    grpcurl -plaintext -max-time 10 -protoset-out "$1" -H "authorization: Bearer $token" \
      "localhost:$GRPC_PORT" describe commodore.UserService >/dev/null || exit 1
    grpcurl -plaintext -max-time 10 -protoset "$1" -H "authorization: Bearer $token" \
      -d "{}" "localhost:$GRPC_PORT" commodore.UserService/GetMe
  ' sh "$descriptor" >"$WORK/$service.initial" 2>&1
  must_check "named session authenticates directly to $service" profile_matches "$WORK/$service.initial" || { rpc_error "$WORK/$service.initial"; finish; }
  must_check "RPC schema is retained on $service" stack_exec "$service" test -s "$descriptor" || finish
done

ingress_specs() {
  printf '%s\n' \
    "quartermaster|$STACK_QUARTERMASTER_GRPC_ADDR|quartermaster.TenantService/GetTenant|{\"tenantId\":\"$tenant\"}" \
    "purser|$STACK_PURSER_GRPC_ADDR|purser.PaymentService/GetBillingStatus|{\"tenantId\":\"$tenant\"}" \
    "periscope|$STACK_PERISCOPE_GRPC_ADDR|periscope.PlatformAnalyticsService/GetNetworkLiveStats|{}" \
    "signalman|$STACK_SIGNALMAN_GRPC_ADDR|signalman.SignalmanService/GetHubStats|{}" \
    "bosun|$STACK_BOSUN_GRPC_ADDR|bosun.BosunService/ListWebhookEndpoints|{}" \
    "lookout|$STACK_LOOKOUT_GRPC_ADDR|lookout.LookoutService/ListIncidents|{\"scope\":\"INCIDENT_SCOPE_PLATFORM\"}"
}
ingress_rpc() {
  local service=$1 address=$2 method=$3 body=$4 credential=$5
  { cat "$credential"; echo; } | stack_exec commodore sh -c '
    read -r token
    grpcurl -plaintext -max-time 10 -protoset "$1" -H "authorization: Bearer $token" \
      -d "$4" "$2" "$3"
  ' sh "$descriptor-$service" "$address" "$method" "$body"
}
while IFS='|' read -r service address method body; do
  { cat "$WORK/revoked.jwt"; echo; } | stack_exec commodore sh -c '
    read -r token
    grpcurl -plaintext -max-time 10 -protoset-out "$1" -H "authorization: Bearer $token" \
      "$2" describe "$3" >/dev/null
  ' sh "$descriptor-$service" "$address" "${method%/*}" >"$WORK/$service.schema" 2>&1
  must_check "retain $service schema before revocation" stack_exec commodore test -s "$descriptor-$service" || { rpc_error "$WORK/$service.schema"; finish; }
  ingress_rpc "$service" "$address" "$method" "$body" "$WORK/revoked.jwt" >"$WORK/$service.initial" 2>&1
  initial_status=$?
  must_check "live named session reaches $service directly" [ "$initial_status" = 0 ] || { rpc_error "$WORK/$service.initial"; finish; }
done < <(ingress_specs)

session_rpc() {
  local service=$1 credential=$2 method=$3 body=$4
  { cat "$credential"; echo; } | stack_exec "$service" sh -c '
    read -r token
    grpcurl -plaintext -max-time 10 -protoset "$1" -H "authorization: Bearer $token" \
      -d "$3" "localhost:$GRPC_PORT" "$2"
  ' sh "$descriptor" "$method" "$body"
}
session_rpc commodore "$WORK/other.jwt" commodore.UserService/ListSessions '{"pagination":{"first":100}}' >"$WORK/list.json" 2>&1
listed_current() { jq -e --arg current "$other" '[.sessions[] | select(.current == true)] | length == 1 and .[0].id == $current' "$WORK/list.json" >/dev/null; }
must_check 'self-service list marks the presented session current' listed_current || { rpc_error "$WORK/list.json"; finish; }
session_rpc commodore "$WORK/other.jwt" commodore.UserService/RenameSession \
  "$(jq -cn --arg id "$revoked" '{sessionId:$id,label:"Stack renamed device"}')" >"$WORK/renamed.json" 2>&1
renamed() { jq -e --arg id "$revoked" '.id == $id and .deviceLabel == "Stack renamed device" and (.current // false | not)' "$WORK/renamed.json" >/dev/null; }
must_check 'owner renames only the selected device' renamed || { rpc_error "$WORK/renamed.json"; finish; }

http_code() { curl -s -m 15 -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(cat "$1")" "$2"; }
mcp_code() {
  curl -s -m 15 -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(cat "$1")" \
    -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"stack-revocation","version":"1"}}}' \
    http://skipper:18018/mcp/notify
}
for url in $FOGHORN_A_INTERNAL_URLS $FOGHORN_B_INTERNAL_URLS; do
  check "named operator session reaches $url" [ "$(http_code "$WORK/revoked.jwt" "$url/nodes/overview")" = 200 ] || finish
done
check 'named session opens Skipper MCP' [ "$(mcp_code "$WORK/revoked.jwt")" = 200 ] || finish
check 'named operator session reaches network diagnostics' [ "$(http_code "$WORK/revoked.jwt" "$BRIDGE_URL/v1/network/livepeer/diagnostics")" = 200 ] || finish

# Keep Commodore's loopback API and event relay available while every peer's
# validation/backfill RPC is refused. Data requests must use local authority.
for authority in commodore commodore-2; do
  netfault_start "$authority" || finish
  FAULTS+=("$authority")
  authority_port=$(stack_exec "$authority" sh -c 'printf "%s" "$GRPC_PORT"')
  case "$authority_port" in ''|*[!0-9]*) fail 'invalid Commodore gRPC port'; finish ;; esac
  netfault "$authority" sh -c 'iptables -A FW_FAULT_IN ! -i lo -p tcp --dport "$1" -j REJECT --reject-with tcp-reset' sh "$authority_port" || {
    fail "isolate $authority RPC ingress"; finish;
  }
  probe=commodore
  [ "$authority" != commodore ] || probe=commodore-2
  # commodore is a shared replica alias; it may resolve to the probing peer.
  authority_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' "$(container_of "$authority")" | awk '{print $1}')
  [ -n "$authority_ip" ] || { fail "resolve exact container IP of $authority"; finish; }
  stack_exec "$probe" sh -c 'grpcurl -plaintext -max-time 2 "$1:$2" list' sh "$authority_ip" "$authority_port" >"$WORK/$authority.unreachable" 2>&1
  must_check "$authority RPC is actually unreachable from peers" grep -Eq 'Failed to dial|failed to dial|connection refused|context deadline|Unavailable' "$WORK/$authority.unreachable" || {
    rpc_error "$WORK/$authority.unreachable"; finish;
  }
done
while IFS='|' read -r service address method body; do
  ingress_rpc "$service" "$address" "$method" "$body" "$WORK/other.jwt" >"$WORK/$service.outage" 2>&1
  outage_status=$?
  must_check "valid session reaches $service while Commodore RPC is unreachable" [ "$outage_status" = 0 ] || { rpc_error "$WORK/$service.outage"; finish; }
done < <(ingress_specs)
for url in $FOGHORN_A_INTERNAL_URLS $FOGHORN_B_INTERNAL_URLS; do
  check "valid operator reaches $url while Commodore RPC is unreachable" [ "$(http_code "$WORK/other.jwt" "$url/nodes/overview")" = 200 ] || finish
done
check 'valid session reaches Skipper MCP while Commodore RPC is unreachable' [ "$(mcp_code "$WORK/other.jwt")" = 200 ] || finish

for attempt in 1 2; do
  session_rpc commodore "$WORK/other.jwt" commodore.UserService/RevokeSession \
    "$(jq -cn --arg id "$revoked" '{sessionId:$id}')" >"$WORK/revoke.json" 2>&1
  revoked_once() { jq -e '.success == true and (.current // false | not)' "$WORK/revoke.json" >/dev/null; }
  must_check "single-session revoke attempt $attempt succeeds idempotently" revoked_once || { rpc_error "$WORK/revoke.json"; finish; }
  if [ "$attempt" = 1 ]; then revocation_started=$(monotonic_ms); fi
done
all_ingresses_reject() {
  local service address method body
  while IFS='|' read -r service address method body; do
    ingress_rpc "$service" "$address" "$method" "$body" "$WORK/revoked.jwt" >"$WORK/$service.revoked" 2>&1
    grep -q 'Code: Unauthenticated' "$WORK/$service.revoked" || return 1
  done < <(ingress_specs)
}
eventually 5 'revocation event reaches every raw-JWT ingress while Commodore RPC is unreachable' all_ingresses_reject
revocation_elapsed=$(($(monotonic_ms) - revocation_started))
check "all six ingresses refuse the revoked sid within five seconds (${revocation_elapsed}ms)" [ "$revocation_elapsed" -le 5000 ]

for service in commodore commodore-2; do
  { cat "$WORK/revoked.jwt"; echo; } | stack_exec "$service" sh -c '
    read -r token
    grpcurl -plaintext -max-time 10 -protoset "$1" -H "authorization: Bearer $token" \
      -d "{}" "localhost:$GRPC_PORT" commodore.UserService/GetMe
  ' sh "$descriptor" >"$WORK/$service.revoked" 2>&1
  must_check "revoked session refused by $service unary auth" grep -q 'Code: Unauthenticated' "$WORK/$service.revoked" || rpc_error "$WORK/$service.revoked"
  { cat "$WORK/other.jwt"; echo; } | stack_exec "$service" sh -c '
    read -r token
    grpcurl -plaintext -max-time 10 -protoset "$1" -H "authorization: Bearer $token" \
      -d "{}" "localhost:$GRPC_PORT" commodore.UserService/GetMe
  ' sh "$descriptor" >"$WORK/$service.other" 2>&1
  must_check "other device still authenticates to $service" profile_matches "$WORK/$service.other" || rpc_error "$WORK/$service.other"
done
while IFS='|' read -r service address method body; do
  ingress_rpc "$service" "$address" "$method" "$body" "$WORK/revoked.jwt" >"$WORK/$service.revoked" 2>&1
  must_check "revoked session refused by $service raw-JWT ingress" grep -q 'Code: Unauthenticated' "$WORK/$service.revoked" || rpc_error "$WORK/$service.revoked"
  ingress_rpc "$service" "$address" "$method" "$body" "$WORK/other.jwt" >"$WORK/$service.other" 2>&1
  other_status=$?
  must_check "other device still authenticates directly to $service" [ "$other_status" = 0 ] || rpc_error "$WORK/$service.other"
done < <(ingress_specs)
for url in $FOGHORN_A_INTERNAL_URLS $FOGHORN_B_INTERNAL_URLS; do
  check "revoked session refused by $url" [ "$(http_code "$WORK/revoked.jwt" "$url/nodes/overview")" = 403 ]
  check "other device still reaches $url" [ "$(http_code "$WORK/other.jwt" "$url/nodes/overview")" = 200 ]
done
skipper_refuses_revoked() { [ "$(mcp_code "$WORK/revoked.jwt")" = 401 ]; }
eventually 5 'revoked session refused by Skipper MCP' skipper_refuses_revoked
check 'other device still reaches Skipper MCP' [ "$(mcp_code "$WORK/other.jwt")" = 200 ]

diagnostics_revoked() { [ "$(http_code "$WORK/revoked.jwt" "$BRIDGE_URL/v1/network/livepeer/diagnostics")" = 401 ]; }
eventually 2 'named revocation event refuses network diagnostics on Bridge' diagnostics_revoked
session_rpc commodore "$WORK/other.jwt" commodore.UserService/RevokeOtherSessions '{}' >"$WORK/others.json" 2>&1
others_revoked() { jq -e '(.revokedCount | tonumber) >= 1' "$WORK/others.json" >/dev/null; }
must_check 'sign out other sessions preserves the presented device' others_revoked || { rpc_error "$WORK/others.json"; finish; }
skipper_refuses_extra() { [ "$(mcp_code "$WORK/extra.jwt")" = 401 ]; }
eventually 5 'third device is refused after sign out others' skipper_refuses_extra
check 'current device survives sign out others' [ "$(mcp_code "$WORK/other.jwt")" = 200 ]

for authority in "${FAULTS[@]}"; do netfault_stop "$authority"; done
FAULTS=()

# A fresh replica must also recover the named revocation from durable state.
stack_ctl restart bridge bridge-2 || { fail 'restart Bridge replicas'; finish; }
eventually 45 'Bridge backfill refuses revoked network diagnostics session' diagnostics_revoked
check 'other device still reaches network diagnostics' [ "$(http_code "$WORK/other.jwt" "$BRIDGE_URL/v1/network/livepeer/diagnostics")" = 200 ]
cleanup
trap - EXIT
finish
