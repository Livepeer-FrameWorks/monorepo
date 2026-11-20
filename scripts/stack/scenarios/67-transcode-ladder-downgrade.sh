#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, creates a tenant and changes its billing tier through the operator CLI
# A downgrade skips a disallowed saved ladder without erasing it; an upgrade restores it.
. "$(dirname "$0")/../lib.sh"
need jq curl python3 openssl || finish
[ "${STACK_TARGET:-stack}" != staging ] || { blocked 'isolated-stack billing fixture only'; finish; }
. "$STACK_LIB_DIR/cli-context.sh"
email=$(test_email "ladder-downgrade-$(date +%s)-$RANDOM")
password="Stack-$(openssl rand -hex 8)"
registration=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" '{email:$e,password:$p,first_name:"Stack",last_name:"Ladder"}+$b' | curl -s -m 30 -w '\n%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/register")
[ "$(echo "$registration" | tail -1)" = 201 ] || { fail 'register ladder test tenant'; finish; }
eventually 60 'verification mail arrives' mail_seen "$email" || finish
token=$(mail_token "$email")
verified=$(jq -cn --arg t "$token" '{token:$t}' | curl -s -m 30 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/verify")
[ "$verified" = 200 ] || { fail 'verify ladder test tenant'; finish; }
cookies="$STACK_STATE_DIR/ladder-downgrade-cookies"
jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" '{email:$e,password:$p}+$b' | curl -fsS -m 30 -c "$cookies" -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login" >/dev/null || { fail 'login ladder test tenant'; finish; }
jwt=$(awk '$6=="access_token"{print $7}' "$cookies" | tail -1)
tenant=$(python3 - "$jwt" <<'PY'
import base64,json,sys
p=sys.argv[1].split('.')[1]
print(json.loads(base64.urlsafe_b64decode(p+'='*(-len(p)%4)))['tenant_id'])
PY
)
set_tier() { "$STACK_REPO/bin/cli" admin billing set-tier --tenant-id "$tenant" --tier "$1" --reason 'stack ladder downgrade regression' --output json >/dev/null; }
policy_is() {
  gql 'query{transcodeLadderPolicy{tenantLadder effectiveLadder}}' '{}' "$jwt" | jq -e --arg effective "$1" '.data.transcodeLadderPolicy.tenantLadder=="uhd" and .data.transcodeLadderPolicy.effectiveLadder==$effective' >/dev/null
}
set_tier payg || { fail 'set paid tier'; finish; }
chosen=$(gql 'mutation{setTranscodeLadderPolicy(input:{ladder:"uhd"}){__typename ... on TranscodeLadderPolicy{effectiveLadder}}}' '{}' "$jwt")
check 'paid tenant stores uhd' json_has "$chosen" '.data.setTranscodeLadderPolicy.effectiveLadder=="uhd"'
set_tier free || { fail 'downgrade tenant'; finish; }
eventually 90 'downgrade preserves uhd but resolves standard' policy_is standard
refused=$(gql 'mutation{createVodUpload(input:{filename:"valid.mp4",sizeBytes:1024,transcodeLadder:"uhd"}){__typename ... on ValidationError{field}}}' '{}' "$jwt")
check 'free tenant cannot request uhd' json_has "$refused" '.data.createVodUpload.__typename=="ValidationError" and .data.createVodUpload.field=="transcodeLadder"'
refused_import=$(gql 'mutation{importVodAsset(input:{url:"https://example.com/video.mp4",transcodeLadder:"uhd"}){__typename ... on ValidationError{field}}}' '{}' "$jwt")
check 'free tenant cannot request uhd through import' json_has "$refused_import" '.data.importVodAsset.__typename=="ValidationError" and .data.importVodAsset.field=="transcodeLadder"'
check 'refusal leaves no upload asset' [ "$(pg commodore "SELECT count(*) FROM commodore.vod_assets WHERE tenant_id='$tenant'")" = 0 ]
set_tier payg || { fail 'upgrade tenant'; finish; }
eventually 90 'upgrade restores saved uhd choice' policy_is uhd
finish
