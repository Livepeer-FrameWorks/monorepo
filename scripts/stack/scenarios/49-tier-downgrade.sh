#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, creates a tenant, assigns its tier through the operator CLI and writes a collection fixture
# A paid tenant schedules a downgrade to Free, sees it in billingLifecycle and
# billingStatus, then cancels it by choosing its current tier. The operator
# recovery surfaces report no held downgrade for it, then name a held one and
# preview what holds it.
. "$(dirname "$0")/../lib.sh"

need jq curl python3 psql openssl || finish
[ -x "$STACK_REPO/bin/cli" ] || { blocked 'operator CLI binary missing; make build-bin-cli'; finish; }
[ "${STACK_TARGET:-stack}" != staging ] || { blocked 'the collection fixture is stack-only'; finish; }
# shellcheck source=scripts/stack/cli-context.sh
. "$STACK_LIB_DIR/cli-context.sh"
CLI="$STACK_REPO/bin/cli"
cli_json() {
  local output
  if output=$("$CLI" "$@" 2>"$STACK_STATE_DIR/downgrade-cli.stderr"); then
    printf '%s' "$output"
  else
    cat "$STACK_STATE_DIR/downgrade-cli.stderr" >&2
    return 1
  fi
}
# Downgrade recovery names the operator in its audit trail, so Purser admits
# only a platform-operator session. The stack's bootstrap fixture owns this one.
operator_jar="$STACK_STATE_DIR/downgrade-operator.cookies"
operator_login() {
  local code
  code=$(jq -cn --arg e operator@stack.test --arg p stack-operator-password --argjson b "$(bot_fields)" '{email:$e,password:$p} + $b' |
    curl -s -m 20 -c "$operator_jar" -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login") || return 1
  [ "$code" = 200 ] || { printf 'operator login failed: HTTP %s\n' "$code" >&2; return 1; }
  operator_jwt=$(awk '$6=="access_token"{print $7}' "$operator_jar" | tail -1)
  [ -n "$operator_jwt" ]
}
operator_cli() { operator_login && FW_USER_TOKEN="$operator_jwt" "$CLI" "$@"; }
operator_json() { operator_login && FW_USER_TOKEN="$operator_jwt" cli_json "$@"; }

email=$(test_email "downgrade-$(date +%s)-$RANDOM")
password="Stack-$(openssl rand -hex 8)"
registration=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" \
  '{email:$e,password:$p,first_name:"Stack",last_name:"Downgrade"} + $b' |
  curl -s -m 20 -w '\n%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/register")
check 'downgrade tenant registration accepted' [ "$(echo "$registration" | tail -1)" = 201 ]
[ "$(echo "$registration" | tail -1)" = 201 ] || finish
eventually 60 'downgrade tenant verification mail delivered' mail_seen "$email"
token=$(mail_token "$email")
verified=$(jq -cn --arg t "$token" '{token:$t}' |
  curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/verify")
check 'downgrade tenant verified' [ "$verified" = 200 ]
cookies="$STACK_STATE_DIR/downgrade-cookies"
login=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" '{email:$e,password:$p} + $b' |
  curl -s -m 20 -c "$cookies" -w '\n%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login")
check 'downgrade tenant login accepted' [ "$(echo "$login" | tail -1)" = 200 ]
jwt=$(awk '$6=="access_token"{print $7}' "$cookies" | tail -1)
[ -n "$jwt" ] || { fail 'downgrade tenant login returned no access token'; finish; }
tenant=$(python3 - "$jwt" <<'PY'
import base64,json,sys
p=sys.argv[1].split('.')[1]; p+='='*(-len(p)%4)
print(json.loads(base64.urlsafe_b64decode(p)).get('tenant_id',''))
PY
)
[ -n "$tenant" ] || { fail 'downgrade tenant JWT has no tenant_id'; finish; }

# Keeping a paid tier needs a complete billing profile and ready collection.
details=$(gql 'mutation($input:UpdateBillingDetailsInput!){updateBillingDetails(input:$input){email}}' \
  "$(jq -cn --arg e "$email" '{input:{email:$e,name:"Stack Downgrade",address:{street:"1 Stack Street",city:"Amsterdam",postalCode:"1011AA",country:"NL"}}}')" "$jwt")
check 'tenant completed its billing profile' json_has "$details" '.errors == null and .data.updateBillingDetails.email != null'
if assigned=$(cli_json admin billing set-tier --tenant-id "$tenant" --tier developer --billing-model postpaid \
  --reason 'stack downgrade test' --output json); then
  pass 'operator assigned the developer tier'
else
  fail 'operator assigned the developer tier'
  finish
fi
check 'developer assignment is postpaid' json_has "$assigned" '.tier_name == "developer" and .billing_model == "postpaid"'
# The stack has no Stripe account: a saved customer stands in for completed
# setup. No provider subscription exists, so no provider price changes.
pg_fixture purser "UPDATE purser.tenant_subscriptions SET payment_method='stripe', stripe_customer_id='cus_stack_downgrade'
  WHERE tenant_id='$tenant'" >/dev/null
check 'collection fixture applied' [ "$(pg purser "SELECT stripe_customer_id FROM purser.tenant_subscriptions WHERE tenant_id='$tenant'")" = cus_stack_downgrade ]

tiers=$(gql 'query{billingTiers{id tierName}}' '{}' "$jwt")
free_id=$(printf '%s' "$tiers" | jq -r '.data.billingTiers[] | select(.tierName=="free") | .id')
developer_id=$(printf '%s' "$tiers" | jq -r '.data.billingTiers[] | select(.tierName=="developer") | .id')
check 'catalog lists Free' [ -n "$free_id" ]
check 'catalog lists Developer' [ -n "$developer_id" ]
{ [ -n "$free_id" ] && [ -n "$developer_id" ]; } || finish

change_tier() {
  gql 'mutation($id:ID!){changeBillingTier(tierId:$id){__typename
    ... on ChangeBillingTierPayload{success message pendingTier{id tierName} effectiveAt}
    ... on ValidationError{message code}}}' "$(jq -cn --arg id "$1" '{id:$id}')" "$jwt"
}
lifecycle() {
  gql 'query{billingLifecycle{pendingTier{id} pendingEffectiveAt pendingReason pendingTierState downgradeEffectiveAt freeMoveFinalAt}}' '{}' "$jwt"
}
status_now() {
  gql 'query{billingStatus{currentTier{tierName} subscription{pendingTier{id} pendingEffectiveAt pendingReason}}}' '{}' "$jwt"
}

scheduled=$(change_tier "$free_id")
echo "    schedule: $scheduled"
check 'choosing Free schedules a downgrade' json_has "$scheduled" \
  ".data.changeBillingTier.__typename == \"ChangeBillingTierPayload\" and .data.changeBillingTier.success and .data.changeBillingTier.pendingTier.id == \"$free_id\" and .data.changeBillingTier.effectiveAt != null"
life=$(lifecycle)
echo "    lifecycle: $life"
check 'billingLifecycle shows the scheduled downgrade' json_has "$life" \
  ".errors == null and .data.billingLifecycle.pendingTier.id == \"$free_id\" and .data.billingLifecycle.pendingReason == \"DOWNGRADE\" and .data.billingLifecycle.pendingTierState == \"SCHEDULED\""
check 'the downgrade takes effect when a downgrade chosen now would' json_has "$life" \
  '.data.billingLifecycle.pendingEffectiveAt != null and .data.billingLifecycle.pendingEffectiveAt == .data.billingLifecycle.downgradeEffectiveAt'
check 'no payment subscription means no Free finality date' json_has "$life" '.data.billingLifecycle.freeMoveFinalAt == null'
state=$(status_now)
check 'billingStatus keeps Developer and stages Free' json_has "$state" \
  ".errors == null and .data.billingStatus.currentTier.tierName == \"developer\" and .data.billingStatus.subscription.pendingTier.id == \"$free_id\" and .data.billingStatus.subscription.pendingReason == \"downgrade\""

cancelled=$(change_tier "$developer_id")
echo "    cancel: $cancelled"
check 'choosing the current tier is accepted' json_has "$cancelled" \
  '.data.changeBillingTier.__typename == "ChangeBillingTierPayload" and .data.changeBillingTier.success and .data.changeBillingTier.pendingTier == null'
life=$(lifecycle)
check 'billingLifecycle no longer shows a downgrade' json_has "$life" \
  '.errors == null and .data.billingLifecycle.pendingTier == null and .data.billingLifecycle.pendingReason == null and .data.billingLifecycle.pendingTierState == null and .data.billingLifecycle.pendingEffectiveAt == null'
check 'a downgrade can still be chosen for the period end' json_has "$life" '.data.billingLifecycle.downgradeEffectiveAt != null'
state=$(status_now)
check 'billingStatus keeps Developer with nothing staged' json_has "$state" \
  '.errors == null and .data.billingStatus.currentTier.tierName == "developer" and .data.billingStatus.subscription.pendingTier == null and .data.billingStatus.subscription.pendingReason == null'
staged=$(pg purser "SELECT COALESCE(pending_tier_id::text,'')||'/'||COALESCE(pending_reason,'') FROM purser.tenant_subscriptions WHERE tenant_id='$tenant'")
check 'Purser cleared the staged downgrade' [ "$staged" = / ]

inventory=$(operator_json admin billing recovery list --tenant-id "$tenant" --output json)
check 'recovery list holds no downgrade for the tenant' json_has "$inventory" '(.held_downgrades // []) | length == 0'
if operator_cli admin billing recovery downgrade --tenant-id "$tenant" --reason 'stack downgrade test' --yes \
  >"$STACK_STATE_DIR/downgrade-recovery.out" 2>&1; then
  fail 'downgrade recovery accepted a tenant with nothing held'
else
  check 'downgrade recovery reports nothing held' grep -q 'no downgrade past its effective time' "$STACK_STATE_DIR/downgrade-recovery.out"
fi

# A downgrade the provider confirmed, past the applier grace, whose closing
# invoice sits in manual review: the case PurserTierDowngradeHeld alerts on.
# The fixture is removed again so the alert does not stay raised on the stack;
# its invoice carries no retention period, which would refuse that delete.
pg_fixture purser "UPDATE purser.tenant_subscriptions SET pending_tier_id='$free_id', pending_reason='downgrade',
    pending_provider_tier_id='$free_id', pending_effective_at=date_trunc('second', NOW() - INTERVAL '27 hours')
  WHERE tenant_id='$tenant';
  INSERT INTO purser.billing_invoices (tenant_id, status, currency, amount, period_start, period_end, due_date, retention_until)
  SELECT tenant_id, 'manual_review', 'EUR', 0, pending_effective_at - INTERVAL '30 days', pending_effective_at, pending_effective_at, NOW()
  FROM purser.tenant_subscriptions WHERE tenant_id='$tenant'" >/dev/null
inventory=$(operator_json admin billing recovery list --tenant-id "$tenant" --output json)
echo "    held inventory: $(printf '%s' "$inventory" | jq -c '.held_downgrades // []')"
check 'recovery list names the held downgrade and its manual_review invoice' json_has "$inventory" \
  "[(.held_downgrades // [])[] | select(.tenant_id == \"$tenant\" and .target_tier_id == \"$free_id\" and .invoice_status == \"manual_review\")] | length == 1"
if printf 'no\n' | operator_cli admin billing recovery downgrade --tenant-id "$tenant" --reason 'stack downgrade test' \
  >"$STACK_STATE_DIR/downgrade-preview.out" 2>&1; then
  fail 'declined downgrade recovery was confirmed'
else
  check 'downgrade recovery preview reports the invoice hold' grep -q ': invoice_held\.' "$STACK_STATE_DIR/downgrade-preview.out"
  check 'downgrade recovery preview names set-cluster-pricing' grep -q 'set-cluster-pricing' "$STACK_STATE_DIR/downgrade-preview.out"
fi
check 'declined preview kept the tier and the staged downgrade' \
  [ "$(pg purser "SELECT t.tier_name||'/'||COALESCE(s.pending_tier_id::text,'') FROM purser.tenant_subscriptions s JOIN purser.billing_tiers t ON t.id=s.tier_id WHERE s.tenant_id='$tenant'")" = "developer/$free_id" ]
pg_fixture purser "DELETE FROM purser.billing_invoices WHERE tenant_id='$tenant' AND status='manual_review';
  UPDATE purser.tenant_subscriptions SET pending_tier_id=NULL, pending_reason=NULL, pending_provider_tier_id=NULL, pending_effective_at=NULL
  WHERE tenant_id='$tenant'" >/dev/null
check 'held downgrade fixture removed' [ "$(pg purser "SELECT COUNT(*) FROM purser.tenant_subscriptions WHERE tenant_id='$tenant' AND pending_tier_id IS NOT NULL")" = 0 ]
finish
