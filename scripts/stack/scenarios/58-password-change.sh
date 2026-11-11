#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, creates a disposable password user and session fixtures
# Password change keeps the caller's credentials usable and ends the other
# device through the real RPC and native refresh transports.
. "$(dirname "$0")/../lib.sh"

need jq curl python3 psql || finish
WORK=$(mktemp -d)
chmod 700 "$WORK"
tenant="" user="" descriptor=""
cleanup() {
  if [ -n "$user" ]; then
    pg_fixture commodore "DELETE FROM commodore.refresh_tokens WHERE tenant_id='$tenant' AND user_id='$user';
      DELETE FROM commodore.auth_sessions WHERE tenant_id='$tenant' AND user_id='$user';
      DELETE FROM commodore.users WHERE tenant_id='$tenant' AND id='$user'" >/dev/null || fail 'password fixture cleanup'
  fi
  if [ -n "$descriptor" ]; then
    for service in commodore commodore-2; do
      stack_exec "$service" rm -f "$descriptor" >/dev/null 2>&1 || fail "descriptor cleanup on $service"
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
python3 - "$WORK" <<'PY' || { fail 'password session fixtures'; finish; }
import base64, hashlib, hmac, json, os, pathlib, sys, time, uuid
work = pathlib.Path(sys.argv[1])
base = json.loads(base64.urlsafe_b64decode((work / 'base.jwt').read_text().strip().split('.')[1] + '=='))
tenant, owner = str(uuid.UUID(base['tenant_id'])), str(uuid.UUID(base['user_id']))
user, current, other = (str(uuid.uuid4()) for _ in range(3))
def enc(value):
    return base64.urlsafe_b64encode(value).rstrip(b'=')
for name, sid in (('current', current), ('other', other)):
    payload = dict(base, user_id=user, email=user+'@stack.test', role='member', roles=[], sid=sid,
                   auth_time=int(time.time()), exp=int(time.time())+600)
    body = enc(b'{"alg":"HS256","typ":"JWT"}') + b'.' + enc(json.dumps(payload).encode())
    signed = body + b'.' + enc(hmac.new(os.environ['JWT_SECRET'].encode(), body, hashlib.sha256).digest())
    (work / (name+'.jwt')).write_bytes(signed)
    refresh = str(uuid.uuid4())
    (work / (name+'.refresh')).write_text(refresh)
    (work / (name+'.hash')).write_text(hashlib.sha256(refresh.encode()).hexdigest())
(work / 'owner').write_text('\n'.join((tenant, owner, user, current, other))+'\n')
PY
{ read -r tenant; read -r owner; read -r user; read -r current; read -r other; } <"$WORK/owner"
descriptor="/tmp/stack-password-$user.pb"
pg_fixture commodore "INSERT INTO commodore.users(id,tenant_id,email,password_hash,role,is_active,verified)
  SELECT '$user',tenant_id,'$user@stack.test',password_hash,'member',true,true FROM commodore.users WHERE tenant_id='$tenant' AND id='$owner';
  INSERT INTO commodore.auth_sessions(id,tenant_id,user_id,auth_method,client_id,device_label,idle_expires_at,absolute_expires_at)
  VALUES ('$current','$tenant','$user','password','web','Password current',NOW()+INTERVAL '1 hour',NOW()+INTERVAL '2 hours'),
         ('$other','$tenant','$user','password','web','Password other',NOW()+INTERVAL '1 hour',NOW()+INTERVAL '2 hours');
  INSERT INTO commodore.refresh_tokens(tenant_id,user_id,session_id,token_hash,expires_at)
  VALUES ('$tenant','$user','$current','$(cat "$WORK/current.hash")',NOW()+INTERVAL '1 hour'),
         ('$tenant','$user','$other','$(cat "$WORK/other.hash")',NOW()+INTERVAL '1 hour')" >/dev/null || { fail 'insert password fixtures'; finish; }
for service in commodore commodore-2; do
  { cat "$WORK/current.jwt"; echo; } | stack_exec "$service" sh -c '
    read -r token
    grpcurl -plaintext -max-time 10 -protoset-out "$1" -H "authorization: Bearer $token" \
      "localhost:$GRPC_PORT" describe commodore.UserService >/dev/null
  ' sh "$descriptor" || { fail "RPC descriptor on $service"; finish; }
done
rpc() {
  local service=$1 credential=$2 method=$3 body=$4
  { cat "$credential"; echo; } | stack_exec "$service" sh -c '
    read -r token
    grpcurl -plaintext -max-time 10 -protoset "$1" -H "authorization: Bearer $token" \
      -d "$3" "localhost:$GRPC_PORT" "$2"
  ' sh "$descriptor" "$method" "$body"
}
rpc commodore "$WORK/current.jwt" commodore.UserService/ChangePassword \
  '{"currentPassword":"wrong-password","newPassword":"stack-new-password"}' >"$WORK/wrong.json" 2>&1
check 'wrong current password is rejected' grep -q 'Code: Unauthenticated' "$WORK/wrong.json"
rpc commodore "$WORK/current.jwt" commodore.UserService/ChangePassword \
  '{"currentPassword":"stack-operator-password","newPassword":"stack-new-password"}' >"$WORK/changed.json" 2>&1
check 'password change revokes exactly the other device' jq -e '.success == true and (.revokedCount | tonumber) == 1' "$WORK/changed.json" >/dev/null || finish
for service in commodore commodore-2; do
  rpc "$service" "$WORK/current.jwt" commodore.UserService/GetMe '{}' >"$WORK/current.json" 2>&1
  check "current access survives on $service" jq -e --arg user "$user" '.id == $user' "$WORK/current.json" >/dev/null
  rpc "$service" "$WORK/other.jwt" commodore.UserService/GetMe '{}' >"$WORK/other.json" 2>&1
  check "other access is rejected on $service" grep -q 'Code: Unauthenticated' "$WORK/other.json"
done
for device in current other; do
  code=$(jq -cn --arg refresh "$(cat "$WORK/$device.refresh")" '{grant_type:"refresh_token",refresh_token:$refresh}' |
    curl -s -m 30 -o "$WORK/$device.rotation.json" -w '%{http_code}' \
      -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/oauth/token")
  if [ "$device" = current ]; then
    check 'current refresh survives password change' [ "$code" = 200 ]
  else
    check 'other refresh is definitively rejected' [ "$code" = 400 ]
    check 'other refresh reports invalid_grant' jq -e '.error == "invalid_grant"' "$WORK/$device.rotation.json" >/dev/null
  fi
done
cleanup
trap - EXIT
finish
