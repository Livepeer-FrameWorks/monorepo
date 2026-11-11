#!/usr/bin/env bash
# stack-scenario: default
# staging: safe
# tenant: qa
# A CLI device grant rotates through the JSON native refresh endpoint without
# browser cookies. Catches native refresh being rejected for missing PKCE fields
# or accidentally reading/writing the browser's refresh cookie.
. "$(dirname "$0")/../lib.sh"

need jq curl || finish
WORK=$(mktemp -d)
chmod 700 "$WORK"
trap 'rm -rf "$WORK"' EXIT
SESSION=$(session_token) || { fail "QA session for device approval"; finish; }

code=$(curl -s -m 30 -o "$WORK/start.json" -w '%{http_code}' \
  -H 'Content-Type: application/json' --data '{"client_id":"cli","scope":"account"}' \
  "$BRIDGE_URL/auth/device/start")
check "CLI device grant starts" [ "$code" = 200 ] || finish
jq '{user_code}' "$WORK/start.json" | curl -s -m 30 -o "$WORK/approve.json" \
  -w '%{http_code}' -H 'Content-Type: application/json' -H "Authorization: Bearer $SESSION" \
  --data-binary @- "$BRIDGE_URL/auth/device/approve" >"$WORK/approve.status"
check "QA user approves device grant" [ "$(cat "$WORK/approve.status")" = 200 ] || finish
jq '{device_code,client_id:"cli"}' "$WORK/start.json" | curl -s -m 30 \
  -o "$WORK/pair.json" -w '%{http_code}' -H 'Content-Type: application/json' \
  --data-binary @- "$BRIDGE_URL/auth/device/poll" >"$WORK/poll.status"
check "CLI receives native credentials" [ "$(cat "$WORK/poll.status")" = 200 ] || finish

token_claims() {
  jq '.access_token | split(".")[1] | gsub("-"; "+") | gsub("_"; "/") | @base64d | fromjson' "$1"
}
token_claims "$WORK/pair.json" >"$WORK/initial-claims.json"
check "native sign-in has its own named session and auth_time" jq -e \
  '(.sid | type == "string" and length == 36) and (.auth_time > 0)' "$WORK/initial-claims.json" >/dev/null

for rotation in 1 2; do
  jq '{grant_type:"refresh_token",refresh_token}' "$WORK/pair.json" | curl -s -m 30 \
    -D "$WORK/headers" -o "$WORK/rotated.json" -w '%{http_code}' \
    -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/oauth/token" >"$WORK/refresh.status"
  check "native refresh $rotation accepted without cookies" [ "$(cat "$WORK/refresh.status")" = 200 ] || finish
  check "native refresh returns both tokens" jq -e \
    '(.access_token | length > 0) and (.refresh_token | length > 0) and .token_type == "Bearer"' "$WORK/rotated.json" >/dev/null
  check "refresh token rotates" jq -e --slurpfile old "$WORK/pair.json" \
    '.refresh_token != $old[0].refresh_token' "$WORK/rotated.json" >/dev/null
  if grep -qi '^set-cookie:' "$WORK/headers"; then fail "native refresh changed browser cookies"; else pass "native refresh sets no cookies"; fi
  check "native tokens are not cacheable" grep -qi '^cache-control: no-store' "$WORK/headers"
  token_claims "$WORK/rotated.json" >"$WORK/claims.json"
  check "refresh preserves session and original sign-in time" jq -e --slurpfile initial "$WORK/initial-claims.json" \
    '.sid == $initial[0].sid and .auth_time == $initial[0].auth_time' "$WORK/claims.json" >/dev/null
  jq '{grant_type:"refresh_token",refresh_token}' "$WORK/pair.json" | curl -s -m 30 \
    -o "$WORK/replayed.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    --data-binary @- "$BRIDGE_URL/auth/oauth/token" >"$WORK/replay.status"
  check "grace replay is retryable and does not mint another chain" [ "$(cat "$WORK/replay.status")" = 503 ]
  ACCESS=$(jq -r '.access_token' "$WORK/rotated.json")
  code=$(curl -s -m 30 -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $ACCESS" "$BRIDGE_URL/auth/me")
  check "renewed native token authenticates" [ "$code" = 200 ]
  mv "$WORK/rotated.json" "$WORK/pair.json"
done
code=$(curl -s -m 30 -o "$WORK/logout.json" -w '%{http_code}' -X POST \
  -H "Authorization: Bearer $ACCESS" "$BRIDGE_URL/auth/logout")
check "native logout revokes the presented session" [ "$code" = 200 ]
jq '{grant_type:"refresh_token",refresh_token}' "$WORK/pair.json" | curl -s -m 30 \
  -o "$WORK/signed-out.json" -w '%{http_code}' -H 'Content-Type: application/json' \
  --data-binary @- "$BRIDGE_URL/auth/oauth/token" >"$WORK/signed-out.status"
check "signed-out native credential is invalid_grant" [ "$(cat "$WORK/signed-out.status")" = 400 ]
check "revoked refresh reports definitive rejection" jq -e '.error == "invalid_grant"' "$WORK/signed-out.json" >/dev/null
code=$(curl -s -m 30 -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $SESSION" "$BRIDGE_URL/auth/me")
check "CLI logout preserves the approving browser session" [ "$code" = 200 ]
finish
