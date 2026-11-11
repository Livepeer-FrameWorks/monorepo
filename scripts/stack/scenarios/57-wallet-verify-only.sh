#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, uses the disposable stack's well-known wallet fixture key
# tenant: qa
# Real SIWE headers and the MCP bearer handoff must never create sessions or
# refresh credentials. An explicit wallet login remains refreshable.
. "$(dirname "$0")/../lib.sh"

need jq curl python3 psql || finish
WORK=$(mktemp -d)
chmod 700 "$WORK"
trap 'rm -rf "$WORK"' EXIT
export STACK_WALLET_SIGNER="${STACK_REPO:-/repo}/bin/stack-walletsign"
export STACK_WALLET_KEY_FILE="$WORK/wallet.key"
[ -x "$STACK_WALLET_SIGNER" ] || { blocked 'make build-stack-walletsign is required'; finish; }
printf '%064x' 1 >"$STACK_WALLET_KEY_FILE"
user=$(pg commodore "SELECT id FROM commodore.users WHERE tenant_id='$STACK_TENANT_ID' AND email='qa@stack.test'")
check 'QA wallet owner exists' [ -n "$user" ] || finish
since=$(pg commodore 'SELECT clock_timestamp()')

rpc_body='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"stack-wallet","version":"1"}}}'
header_value() { awk -v name="$2:" 'tolower($1)==name{gsub("\r", "", $2);print $2}' "$1" | tail -1; }
for proof in 1 2 3; do
  bash "$STACK_LIB_DIR/wallet-login.sh" body >"$WORK/proof.json" || { fail 'obtain signed wallet challenge'; finish; }
  address=$(jq -r '.body.address' "$WORK/proof.json")
  signature=$(jq -r '.body.signature' "$WORK/proof.json")
  message=$(jq -r '.body.message | @base64' "$WORK/proof.json")
  code=$(curl -s -m 30 -D "$WORK/headers" -o "$WORK/initialize" -w '%{http_code}' \
    -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    -H "X-Wallet-Address: $address" -H "X-Wallet-Signature: $signature" -H "X-Wallet-Message: $message" \
    --data "$rpc_body" "$BRIDGE_URL/mcp")
  check "multiline SIWE proof $proof authenticates MCP over HTTP" [ "$code" = 200 ] || finish
  access=$(header_value "$WORK/headers" x-access-token)
  session=$(header_value "$WORK/headers" mcp-session-id)
  check 'MCP returns bearer and transport session ID' test -n "$access" -a -n "$session" || finish
  printf '%s' "$access" >"$WORK/bearer.jwt"
  check 'wallet bearer has a 15-minute lifetime and no interactive session ID' python3 - "$WORK/bearer.jwt" <<'PY'
import base64, json, pathlib, sys
claims = json.loads(base64.urlsafe_b64decode(pathlib.Path(sys.argv[1]).read_text().split('.')[1] + '=='))
assert claims['auth_type'] == 'wallet' and claims['aud'] == ['bridge']
assert not claims.get('sid') and not claims.get('roles')
assert 0 < claims['exp'] - claims['iat'] <= 900
PY
  code=$(curl -s -m 30 -o /dev/null -w '%{http_code}' \
    -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    -H "Authorization: Bearer $access" -H "Mcp-Session-Id: $session" \
    --data '{"jsonrpc":"2.0","method":"notifications/initialized"}' "$BRIDGE_URL/mcp")
  check 'MCP accepts bearer initialization notification' [ "$code" = 202 ] || finish
  code=$(curl -s -m 30 -o "$WORK/tool" -w '%{http_code}' \
    -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    -H "Authorization: Bearer $access" -H "Mcp-Session-Id: $session" \
    --data '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_linked_wallets","arguments":{}}}' "$BRIDGE_URL/mcp")
  check 'handed-off bearer authenticates subsequent MCP requests' [ "$code" = 200 ] || finish
  if grep -q '^data: ' "$WORK/tool"; then
    sed -n 's/^data: //p' "$WORK/tool" | tail -1 >"$WORK/tool.json"
  else
    cp "$WORK/tool" "$WORK/tool.json"
  fi
  check 'wallet identity reaches Commodore through its RPC assertion' jq -e \
    '.result != null and (.result.isError // false | not) and .error == null' "$WORK/tool.json" >/dev/null
done
check 'wallet headers create zero auth_sessions rows' [ "$(pg commodore "SELECT count(*) FROM commodore.auth_sessions WHERE tenant_id='$STACK_TENANT_ID' AND user_id='$user' AND created_at >= '$since'")" = 0 ]
check 'wallet headers create zero refresh rows' [ "$(pg commodore "SELECT count(*) FROM commodore.refresh_tokens WHERE tenant_id='$STACK_TENANT_ID' AND user_id='$user' AND created_at >= '$since'")" = 0 ]

bash "$STACK_LIB_DIR/wallet-login.sh" jar "$WORK/interactive.cookies" "$WORK/interactive.json" || { fail 'explicit wallet login'; finish; }
check 'explicit wallet login remains refreshable' [ "$(pg commodore "SELECT count(*) FROM commodore.refresh_tokens WHERE tenant_id='$STACK_TENANT_ID' AND user_id='$user' AND created_at >= '$since'")" = 1 ]
finish
