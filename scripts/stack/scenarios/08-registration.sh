#!/usr/bin/env bash
# stack-scenario: default
# Self-service signup end to end through the stack's Mailpit SMTP: register ->
# verification email -> verify -> login -> first stream, plus delivery through
# the account-email outbox when the first SMTP attempt fails.
# Catches: N7 (verification email sent once, a failed send left the account
# unverifiable), the register flow never exercised end to end on staging.
. "$(dirname "$0")/../lib.sh"

need jq curl python3 || finish
COOKIES="$STACK_STATE_DIR/register-cookies"

register() { # register <email> <password>
  jq -cn --arg e "$1" --arg p "$2" --argjson b "$(bot_fields)" \
    '{email:$e,password:$p,first_name:"Stack",last_name:"Tester"} + $b' |
    curl -s -m 20 -w '\n%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/register"
}
log "register -> email -> verify -> login -> first stream"
EMAIL=$(test_email "$(date +%s)-$RANDOM")
PASSWORD="Stack-$(openssl rand -hex 8 2>/dev/null || echo $RANDOM$RANDOM)"
R=$(register "$EMAIL" "$PASSWORD")
code=$(echo "$R" | tail -1)
if [ "$code" = 201 ]; then
  pass "register accepted"
elif echo "$R" | grep -qi "bot verification"; then
  blocked "the stack's bot check refused the scenario (configure the Turnstile test secret or leave Turnstile unset): $R"
  finish
else
  fail "register: $R"
  finish
fi
eventually 60 "verification email delivered" mail_seen "$EMAIL"
TOKEN=$(mail_token "$EMAIL")
check "email carries a verify link" [ -n "$TOKEN" ]
V=$(jq -cn --arg t "$TOKEN" '{token:$t}' | curl -s -m 20 -w '\n%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/verify")
check "verify link accepted ($(echo "$V" | tail -1))" [ "$(echo "$V" | tail -1)" = 200 ]
L=$(jq -cn --arg e "$EMAIL" --arg p "$PASSWORD" --argjson b "$(bot_fields)" '{email:$e,password:$p} + $b' |
  curl -s -m 20 -c "$COOKIES" -w '\n%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login")
check "login after verification ($(echo "$L" | tail -1))" [ "$(echo "$L" | tail -1)" = 200 ]
ACCESS=$(awk '$6=="access_token"{print $7}' "$COOKIES" 2>/dev/null)
if [ -n "$ACCESS" ]; then
  S=$(gql 'mutation($i:CreateStreamInput!){createStream(input:$i){__typename ... on Stream{id playbackId}}}' '{"i":{"name":"first stream"}}' "$ACCESS")
  check "the new tenant creates its first stream" json_has "$S" '.data.createStream.__typename == "Stream"'
else
  fail "login set no access_token cookie"
fi

log "outbox: first SMTP attempt fails, a retry delivers"
if [ "${STACK_TARGET:-stack}" = staging ] && declare -F mail_fault_start >/dev/null; then
  EMAIL2=$(test_email "retry-$(date +%s)-$RANDOM")
  mail_fault_start || { blocked 'staging SMTP fault could not be installed'; finish; }
  trap 'mail_fault_stop' EXIT
  R=$(register "$EMAIL2" "$PASSWORD")
  check "register accepted while SMTP refuses" [ "$(echo "$R" | tail -1)" = 201 ]
  sleep 8
  no_mail() { ! mail_seen "$1"; }
  check "nothing delivered while SMTP refuses" no_mail "$EMAIL2"
  mail_fault_stop
  trap - EXIT
  eventually 120 "outbox retry delivers the verification email" mail_seen "$EMAIL2"
  T2=$(mail_token "$EMAIL2")
  V2=$(jq -cn --arg t "$T2" '{token:$t}' | curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/verify")
  check "the retried link verifies ($V2)" [ "$V2" = 200 ]
elif curl -s -m 5 -o /dev/null -w '%{http_code}' "$MAILPIT_API/api/v1/chaos" | grep -q 200; then
  EMAIL2=$(test_email "retry-$(date +%s)-$RANDOM")
  curl -s -m 5 -X PUT -H 'Content-Type: application/json' "$MAILPIT_API/api/v1/chaos" \
    --data '{"Recipient":{"ErrorCode":451,"Probability":100}}' >/dev/null
  R=$(register "$EMAIL2" "$PASSWORD")
  check "register accepted while SMTP refuses" [ "$(echo "$R" | tail -1)" = 201 ]
  sleep 8
  no_mail() { ! mail_seen "$1"; }
  check "nothing delivered while SMTP refuses" no_mail "$EMAIL2"
  curl -s -m 5 -X PUT -H 'Content-Type: application/json' "$MAILPIT_API/api/v1/chaos" \
    --data '{"Recipient":{"ErrorCode":451,"Probability":0}}' >/dev/null
  # The outbox backs off from 30 s; allow two retries.
  eventually 120 "outbox retry delivers the verification email" mail_seen "$EMAIL2"
  T2=$(mail_token "$EMAIL2")
  V2=$(jq -cn --arg t "$T2" '{token:$t}' | curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/verify")
  check "the retried link verifies ($V2)" [ "$V2" = 200 ]
else
  blocked "outbox retry needs Mailpit with --enable-chaos"
fi
finish
