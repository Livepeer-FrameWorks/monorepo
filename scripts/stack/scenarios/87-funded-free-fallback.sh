#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, creates tenants and changes their billing through the operator CLI
# Settled funding activates PAYG; an eligible exhausted account returns to
# Free without losing its funding preference. Explicit Free retains credit
# across later top-ups until the tenant enables PAYG again.
. "$(dirname "$0")/../lib.sh"

need jq curl python3 psql || finish
[ -x "$STACK_REPO/bin/cli" ] || { blocked 'operator CLI binary missing; make build-bin-cli'; finish; }
if [ "${STACK_TARGET:-stack}" = staging ]; then
  [ -n "${STACK_CLI_CONTEXT:-}" ] || { blocked 'staging CLI context is missing'; finish; }
else
  # shellcheck source=scripts/stack/cli-context.sh
  . "$STACK_LIB_DIR/cli-context.sh"
fi
CLI="$STACK_REPO/bin/cli"
cli_json() {
  local output
  local context=()
  [ "${STACK_TARGET:-stack}" = staging ] && context=(--context "$STACK_CLI_CONTEXT")
  if output=$("$CLI" "${context[@]}" "$@" 2>"$STACK_STATE_DIR/billing-cli.stderr"); then
    printf '%s' "$output"
  else
    cat "$STACK_STATE_DIR/billing-cli.stderr" >&2
    return 1
  fi
}

email=$(test_email "funded-free-$(date +%s)-$RANDOM")
password="Stack-$(openssl rand -hex 8)"
registration=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" \
  '{email:$e,password:$p,first_name:"Stack",last_name:"Billing"} + $b' |
  curl -s -m 20 -w '\n%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/register")
check 'billing tenant registration accepted' [ "$(echo "$registration" | tail -1)" = 201 ]
[ "$(echo "$registration" | tail -1)" = 201 ] || finish
eventually 60 'billing tenant verification mail delivered' mail_seen "$email"
token=$(mail_token "$email")
check 'billing tenant verification token present' [ -n "$token" ]
verified=$(jq -cn --arg t "$token" '{token:$t}' |
  curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/verify")
check 'billing tenant verified' [ "$verified" = 200 ]
cookies="$STACK_STATE_DIR/billing-cookies"
login=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" '{email:$e,password:$p} + $b' |
  curl -s -m 20 -c "$cookies" -w '\n%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login")
check 'billing tenant login accepted' [ "$(echo "$login" | tail -1)" = 200 ]
jwt=$(awk '$6=="access_token"{print $7}' "$cookies" | tail -1)
[ -n "$jwt" ] || { fail 'billing tenant login returned no access token'; finish; }
tenant=$(python3 - "$jwt" <<'PY'
import base64,json,sys
p=sys.argv[1].split('.')[1]; p+='='*(-len(p)%4)
print(json.loads(base64.urlsafe_b64decode(p)).get('tenant_id',''))
PY
)
[ -n "$tenant" ] || { fail 'billing tenant JWT has no tenant_id'; finish; }

# The phase-close invoice needs the same complete legal buyer as a normal
# purchase. The US business fixture preserves this scenario's zero Dutch VAT.
details=$(gql 'mutation($input:UpdateBillingDetailsInput!){updateBillingDetails(input:$input){email customerType}}' \
  "$(jq -cn --arg email "$email" '{input:{email:$email,name:"Stack Billing Buyer",company:"Stack Billing LLC",customerType:"business",vatNumber:"",address:{street:"1 Main Street",city:"Seattle",postalCode:"98101",country:"US"}}}')" "$jwt")
if json_has "$details" '.errors == null and .data.updateBillingDetails.customerType == "business" and .data.updateBillingDetails.email != null'; then
  pass 'billing tenant completed its legal billing profile'
else
  fail 'billing tenant completed its legal billing profile'
  finish
fi
initial_tier=$(pg purser "SELECT tier.tier_name FROM purser.tenant_subscriptions sub JOIN purser.billing_tiers tier ON tier.id=sub.tier_id WHERE sub.tenant_id='$tenant'")
check 'new verified account starts with Free limits' [ "$initial_tier" = free ]
credit_reference=$(python3 -c 'import uuid; print(uuid.uuid4())')
if credit=$(cli_json admin billing credit --tenant-id "$tenant" --amount 10.00 \
  --reference-id "$credit_reference" --reason 'stack automatic Free fallback credit' --output json); then
  pass 'operator credited Free balance'
else
  fail "operator credit failed: $credit"
fi
funded_usage_active() {
  local canonical
  canonical=$(pg purser "SELECT tier.tier_name || '/' || sub.billing_model FROM purser.tenant_subscriptions sub JOIN purser.billing_tiers tier ON tier.id=sub.tier_id WHERE sub.tenant_id='$tenant'")
  [ "$canonical" = payg/prepaid ]
}
eventually 90 'settled Free funding activates canonical PAYG without operator assignment' funded_usage_active
replayed_credit=$(cli_json admin billing credit --tenant-id "$tenant" --amount 10.00 \
  --reference-id "$credit_reference" --reason 'stack automatic Free fallback credit' --output json)
check 'same credit retry returns the original receipt' [ "$(printf '%s' "$credit" | jq -r .id)" = "$(printf '%s' "$replayed_credit" | jq -r .id)" ]

balance_before=$(pg purser "SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id='$tenant' AND currency='EUR'")
check 'prepaid credit is exactly EUR 10' [ "$balance_before" = 1000 ]

log 'eligible exhausted PAYG returns to Free and funding restores PAYG'
remaining=$(pg purser "SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id='$tenant' AND currency='EUR'")
remaining_amount=$(python3 - "$remaining" <<'PYAMOUNT'
from decimal import Decimal
import sys
print(Decimal(sys.argv[1])/100)
PYAMOUNT
)
cli_json admin billing credit --tenant-id "$tenant" --amount="-$remaining_amount" --debit \
  --reason 'stack verified PAYG exhaustion' --output json >/dev/null || { fail 'exhaust remaining PAYG credit'; finish; }
free_usage_active() {
  [ "$(pg purser "SELECT tier.tier_name || '/' || sub.billing_model FROM purser.tenant_subscriptions sub JOIN purser.billing_tiers tier ON tier.id=sub.tier_id WHERE sub.tenant_id='$tenant'")" = free/postpaid ]
}
eventually 120 'verified exhausted PAYG returns to Free' free_usage_active
check 'automatic fallback retains funding preference' [ "$(pg purser "SELECT automatic_prepaid_usage::int FROM purser.tenant_subscriptions WHERE tenant_id='$tenant'")" = 1 ]
cli_json admin billing credit --tenant-id "$tenant" --amount 10.00 \
  --reason 'stack funding after automatic Free fallback' --output json >/dev/null || { fail 'fund after automatic fallback'; finish; }
eventually 90 'next settled funding restores canonical PAYG' funded_usage_active

cli_json admin billing set-tier --tenant-id "$tenant" --tier free --billing-model postpaid --reason 'stack deliberate Free choice' --output json >/dev/null || { fail 'choose Free explicitly'; finish; }
cli_json admin billing credit --tenant-id "$tenant" --amount 1.00 --reason 'stack credit after explicit Free choice' --output json >/dev/null || { fail 'credit explicit Free account'; finish; }
check 'explicit Free remains active after later funding' free_usage_active
check 'explicit Free retains wallet credit' [ "$(pg purser "SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id='$tenant' AND currency='EUR'")" = 1100 ]
check 'explicit Free disables automatic funded usage' [ "$(pg purser "SELECT automatic_prepaid_usage::int FROM purser.tenant_subscriptions WHERE tenant_id='$tenant'")" = 0 ]
opted_in=$(gql 'mutation{enablePrepaidUsage{activated tierName billingModel}}' '{}' "$jwt")
check 'tenant can enable retained credit for PAYG without a second deposit' json_has "$opted_in" '.errors == null and .data.enablePrepaidUsage.activated and .data.enablePrepaidUsage.billingModel == "prepaid"'
eventually 90 'tenant opt-in restores PAYG using retained credit' funded_usage_active
finish
