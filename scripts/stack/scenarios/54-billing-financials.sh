#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, creates isolated billing tenants and provider payments
# Stripe/VIES/chain use a deployed simulator; Mollie is covered by real-PostgreSQL tests, without a deployed Mollie fake.
. "$(dirname "$0")/../lib.sh"
need jq curl python3 openssl || finish
[ "${STACK_TARGET:-stack}" != staging ] || { blocked 'billing fixtures are stack-only'; finish; }
[ -x "$STACK_REPO/bin/cli" ] || { blocked 'operator CLI missing'; finish; }
# shellcheck source=scripts/stack/cli-context.sh
. "$STACK_LIB_DIR/cli-context.sh"
CLI="$STACK_REPO/bin/cli"
email=$(test_email "finance-$(date +%s)-$RANDOM")
password="Stack-$(openssl rand -hex 8)"
registration=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" '{email:$e,password:$p,first_name:"Stack",last_name:"Finance"}+$b' |
  curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/register")
check 'finance tenant registration' [ "$registration" = 201 ]
[ "$registration" = 201 ] || finish
eventually 60 'finance verification mail delivered' mail_seen "$email"
token=$(mail_token "$email")
verified=$(jq -cn --arg t "$token" '{token:$t}' | curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/verify")
check 'finance tenant verified' [ "$verified" = 200 ]
login_session() {
  local who=$1 secret=$2 jar=$3 code
  code=$(jq -cn --arg e "$who" --arg p "$secret" --argjson b "$(bot_fields)" '{email:$e,password:$p}+$b' |
    curl -s -m 20 -c "$jar" -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login") || return 1
  [ "$code" = 200 ] || return 1
  awk '$6=="access_token"{print $7}' "$jar" | tail -1
}
jwt=$(login_session "$email" "$password" "$STACK_STATE_DIR/finance-user.cookies")
[ -n "$jwt" ] || { fail 'finance user login'; finish; }
tenant=$(python3 - "$jwt" <<'PY'
import base64,json,sys
p=sys.argv[1].split('.')[1];p+='='*(-len(p)%4)
print(json.loads(base64.urlsafe_b64decode(p))['tenant_id'])
PY
)
profile() {
 gql 'mutation($input:UpdateBillingDetailsInput!){updateBillingDetails(input:$input){email customerType company vatNumber}}' \
  "$(jq -cn --arg e "$email" --arg type "$1" --arg country "${2:-US}" --arg vat "${3:-}" '{input:{email:$e,name:"Stack Buyer",customerType:$type,vatNumber:$vat,address:{street:"1 Main Street",city:"Stack City",postalCode:"1000",country:$country}}}')" "$jwt"
}
check 'consumer profile requires no business identifiers' json_has "$(profile consumer)" '.errors == null and .data.updateBillingDetails.customerType == "consumer" and (.data.updateBillingDetails.vatNumber // "") == ""'
check 'business profile can omit VAT number' json_has "$(profile business)" '.errors == null and .data.updateBillingDetails.customerType == "business"'
opjwt=$(login_session operator@stack.test stack-operator-password "$STACK_STATE_DIR/finance-operator.cookies")
[ -n "$opjwt" ] || { fail 'finance operator login'; finish; }
operator_cli() { FW_USER_TOKEN="$opjwt" "$CLI" "$@"; }
if FW_USER_TOKEN="$jwt" "$CLI" admin billing finance reviews list --tenant-id "$tenant" >"$STACK_STATE_DIR/finance-denied.out" 2>&1; then
 fail 'customer inspected operator tax reviews'
else
 check 'customer tax review access denied by authorization' grep -qi 'PermissionDenied\|platform.operator' "$STACK_STATE_DIR/finance-denied.out"
fi
# Deployed Purser talks to isolated external provider simulators. This driver
# uses public RPCs and real request signatures; it cannot issue invoices itself.
[ -x "$STACK_REPO/bin/stack-billing-runner" ] || { blocked 'billing test driver missing'; finish; }
STACK_BILLING_DATABASE_URL=$(PG_HOST="$PG_HOST" PG_PORT="$PG_PORT" python3 - <<'PYDSN'
import os,urllib.parse
print('postgresql://purser:'+urllib.parse.quote(os.environ.get('POSTGRES_PASSWORD',''),safe='')+'@'+os.environ['PG_HOST']+':'+os.environ['PG_PORT']+'/purser?sslmode=disable')
PYDSN
)
deployed_crypto() {
 STACK_BILLING_TENANT_ID="$tenant" STACK_BILLING_DATABASE_URL="$STACK_BILLING_DATABASE_URL" STACK_BILLING_DEPLOYED=1 STACK_BILLING_RATE_BPS="$1" \
  "$STACK_REPO/bin/stack-billing-runner" -test.run '^TestStackDeployedCryptoTax$' -test.v -test.timeout 120s
}
check 'deployed non-EU business settles exactly one zero-tax invoice and net credit' deployed_crypto 0
check 'NL consumer profile saved' json_has "$(profile consumer NL)" '.errors == null'
check 'deployed NL consumer settles one invoice with 21% VAT on top and net credit' deployed_crypto 2100
deployed_stripe() {
 local timeout=120s
 # Reconciliation follows the deployed worker's real five-minute cadence.
 [ "$1" != TestStackDeployedStripeNoHoldRenewalOutcomes ] || timeout=16m
 STACK_BILLING_TENANT_ID="$tenant" STACK_BILLING_STALE_RATE_TENANT_ID="${STACK_BILLING_STALE_RATE_TENANT_ID:-}" STACK_BILLING_FALLBACK_TENANT_IDS="${STACK_BILLING_FALLBACK_TENANT_IDS:-}" STACK_BILLING_DATABASE_URL="$STACK_BILLING_DATABASE_URL" STACK_BILLING_DEPLOYED=1 \
  "$STACK_REPO/bin/stack-billing-runner" -test.run "^$1$" -test.v -test.timeout "$timeout"
}
check 'deployed NL card top-up collects gross and credits net once' deployed_stripe TestStackDeployedCardTopupTax
check 'deployed native USD settlement values EUR without duplicating credit' deployed_stripe TestStackDeployedNativeUSDSettlement
check 'deployed Stripe renewal retains exactly one original VAT invoice and PDF' deployed_stripe TestStackDeployedStripeRenewalTax
check 'EU business profile saved' json_has "$(profile business AT ATU12345678)" '.errors == null'
check 'deployed VIES-validated EU business settles one reverse-charge invoice' deployed_crypto 0
archive="$STACK_STATE_DIR/finance-$tenant.zip"
if operator_cli admin billing finance audit --provider none --output "$archive"; then
 pass 'operator financial archive generated'
 check 'archive retains proof, hashes and exception detail' python3 - "$archive" "$tenant" <<'PY'
import csv,hashlib,io,json,os,sys,zipfile
path,tenant=sys.argv[1:]
assert os.stat(path).st_mode & 0o777 == 0o600
with zipfile.ZipFile(path) as z:
 m=json.loads(z.read('manifest.json'))
 assert m['schema_version']==2 and m['posting_ready'] is False
 for name,digest in m['sha256'].items():assert hashlib.sha256(z.read(name)).hexdigest()==digest,name
 assert 'evidence/billing_tax_reviews.jsonl' in z.namelist()
 assert 'evidence/provider_tax_profiles.jsonl' in z.namelist()
 assert 'exception_records.csv' in z.namelist() and 'exceptions.csv' in z.namelist()
 assert m['occurrence_through_exclusive'] and len(m['page_observations'])>1
PY
else
 fail 'operator financial archive generated'
fi

fresh_invoice_tenant() {
 email=$(test_email "finance-$1-$(date +%s)-$RANDOM")
 password="Stack-$(openssl rand -hex 8)"
 local code token
 code=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" '{email:$e,password:$p,first_name:"Stack",last_name:"Invoice"}+$b' |
  curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/register")
 [ "$code" = 201 ] || return 1
 eventually 60 'invoice fixture verification mail delivered' mail_seen "$email"
 token=$(mail_token "$email")
 code=$(jq -cn --arg t "$token" '{token:$t}' | curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/verify")
 [ "$code" = 200 ] || return 1
 jwt=$(login_session "$email" "$password" "$STACK_STATE_DIR/finance-$1.cookies") || return 1
 tenant=$(python3 - "$jwt" <<'PY'
import base64,json,sys
p=sys.argv[1].split('.')[1];p+='='*(-len(p)%4)
print(json.loads(base64.urlsafe_b64decode(p))['tenant_id'])
PY
 )
 [ -n "$tenant" ]
}
grant_notice_tenants=()
grant_notice_recipients=()
regular_invoice() {
 local country=$1 type=$2 vat=$3 rate=$4 result
 fresh_invoice_tenant "$country" || return 1
 json_has "$(profile "$type" "$country" "$vat")" '.errors == null' || return 1
 # A large invoice-only fixture fee makes two seconds of real proration worth
 # enough cents to verify VAT rounding, without rewriting issued documents.
 operator_cli admin billing grant set --tenant-id "$tenant" --tier production --base-fee 1000000.00 --collection invoice --reason 'stack tax invoice matrix' --output json >/dev/null || return 1
 sleep 2
 operator_cli admin billing set-tier --tenant-id "$tenant" --tier payg --billing-model prepaid --reason 'stack closes taxed invoice phase' --output json >/dev/null || return 1
 result=$(pg purser "SELECT json_build_object('count',count(*),'net',min(net_eur_cents),'vat',min(vat_eur_cents),'gross',min(gross_eur_cents),'rate',min((tax_summary->0->>'rate_bps')::int),'treatment',min(tax_summary->0->>'treatment')) FROM purser.billing_invoices WHERE tenant_id='$tenant' AND document_kind='invoice' AND usage_details->>'closes_postpaid_phase'='true' AND net_eur_cents>0") || return 1
 python3 - "$result" "$rate" "$country" <<'PY' || return 1
import json,sys
r=json.loads(sys.argv[1]);rate=int(sys.argv[2])
assert r['count']==1 and r['net']>0 and r['rate']==rate,r
assert r['vat']==(r['net']*rate+5000)//10000 and r['gross']==r['net']+r['vat'],r
if rate:assert r['vat']>0,r
if sys.argv[3]=='AT':assert 'reverse' in r['treatment'],r
print('    deployed phase invoice:',r)
PY
 operator_cli admin billing grant revoke --tenant-id "$tenant" --reason 'stack tax invoice matrix complete' --output json >/dev/null || return 1
 grant_notice_tenants+=("$tenant")
 grant_notice_recipients+=("$email")
}
check 'deployed regular NL consumer invoice has 21% VAT on top' regular_invoice NL consumer '' 2100
check 'deployed regular EU B2B invoice reverses charge with VIES proof' regular_invoice AT business ATU12345678 0
check 'deployed regular non-EU business invoice has zero Dutch VAT' regular_invoice US business '' 0
paid_stripe_upgrade() {
 fresh_invoice_tenant stripe-upgrade || return 1
 json_has "$(profile business AT ATU12345678)" '.errors == null' || return 1
 deployed_stripe TestStackDeployedStripePaidUpgradeOriginalTax
}
check 'deployed paid Stripe upgrade credits original zero VAT and charges new 21 percent fee' paid_stripe_upgrade
nohold_stripe_renewal() {
 local fallback_tenants="" stale_tenant label
 for label in business eu-consumer foreign-consumer invalid-claim; do
  fresh_invoice_tenant "stripe-fallback-$label" || return 1
  json_has "$(profile business AT ATU12345678)" '.errors == null' || return 1
  fallback_tenants="${fallback_tenants:+$fallback_tenants,}$tenant"
 done
 fresh_invoice_tenant stripe-stale-rate || return 1
 json_has "$(profile business AT ATU12345678)" '.errors == null' || return 1
 stale_tenant=$tenant
 fresh_invoice_tenant stripe-nohold || return 1
 json_has "$(profile consumer NL)" '.errors == null' || return 1
 STACK_BILLING_STALE_RATE_TENANT_ID="$stale_tenant" STACK_BILLING_FALLBACK_TENANT_IDS="$fallback_tenants" deployed_stripe TestStackDeployedStripeNoHoldRenewalOutcomes || return 1
 operator_cli admin billing finance reviews list --tenant-id "$stale_tenant" --provider stripe >"$STACK_STATE_DIR/finance-stale-reviews.jsonl" || return 1
 jq -se 'any(.[]; .kind == "provider_tax_document_review" and .status == "open" and (.evidence_sha256|length)==64)' "$STACK_STATE_DIR/finance-stale-reviews.jsonl" >/dev/null || return 1
 local stale_archive="$STACK_STATE_DIR/finance-stale-$stale_tenant.zip"
 operator_cli admin billing finance audit --provider none --output "$stale_archive" || return 1
 python3 - "$stale_archive" "$stale_tenant" <<'PYPROOF'
import json,sys,zipfile
with zipfile.ZipFile(sys.argv[1]) as z:
 reviews=[json.loads(x) for x in z.read('evidence/billing_tax_reviews.jsonl').splitlines()]
 mine=[r for r in reviews if r['tenant_id']==sys.argv[2] and r['kind']=='provider_tax_document_review']
 assert mine and all(r['status']=='open' for r in mine),mine
 expectations=[json.loads(x) for x in z.read('evidence/provider_tax_assessments.jsonl').splitlines()]
 assert any(r['tenant_id']==sys.argv[2] and r['assessment']['rate_bps']==2100 for r in expectations),expectations
PYPROOF
}
check 'deployed no-hold renewals preserve declines, SCA, processing, overdue debt and fallback reviews' nohold_stripe_renewal
purser_grant_notices_delivered() {
 local index result messages recipient
 [ "${#grant_notice_tenants[@]}" = 3 ] && [ "${#grant_notice_recipients[@]}" = 3 ] || return 1
 for index in "${!grant_notice_tenants[@]}"; do
  recipient=${grant_notice_recipients[$index]}
  result=$(pg purser "SELECT count(*)=1 AND bool_and(notified_at IS NOT NULL AND error IS NULL AND abandoned_at IS NULL) FROM purser.operator_grant_notices WHERE tenant_id='${grant_notice_tenants[$index]}' AND kind='ended'") || return 1
  [ "$result" = t ] || { printf 'Purser grant-ended notice not acknowledged for tenant %s\n' "${grant_notice_tenants[$index]}" >&2; return 1; }
  messages=$(curl -fsS -m 10 --get --data-urlencode "query=to:$recipient" "$MAILPIT_API/api/v1/search") || return 1
  jq -e --arg recipient "$recipient" --arg subject 'Your FrameWorks arrangement has ended' \
   'any(.messages[]?; .Subject == $subject and any(.To[]?; .Address == $recipient))' <<<"$messages" >/dev/null || return 1
 done
}
check 'deployed Purser grant-ended notices reach all three invoice customers and retain acknowledged delivery' purser_grant_notices_delivered
finish
