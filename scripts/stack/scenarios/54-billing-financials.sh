#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, creates a tenant and records establishment review fixtures
. "$(dirname "$0")/../lib.sh"
need jq curl python3 openssl || finish
[ "${STACK_TARGET:-stack}" != staging ] || { blocked 'establishment fixtures are stack-only'; finish; }
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
if FW_USER_TOKEN="$jwt" "$CLI" admin billing finance tax-profile --tenant-id "$tenant" >"$STACK_STATE_DIR/finance-denied.out" 2>&1; then
 fail 'customer inspected operator tax evidence'
else
 check 'customer evidence access denied by authorization' grep -qi 'PermissionDenied\|platform.operator' "$STACK_STATE_DIR/finance-denied.out"
fi
inspection=$(operator_cli admin billing finance tax-profile --tenant-id "$tenant")
check 'operator sees reviewed identity and revision' json_has "$inspection" '.profile.name == "Stack Buyer" and (.profile_sha256|length)==64 and .evidence_revision==0'
fingerprint=$(printf '%s' "$inspection" | jq -r .profile_sha256)
expiry=$(python3 -c 'from datetime import datetime,timedelta,timezone;print((datetime.now(timezone.utc)+timedelta(hours=1)).isoformat())')
approved=$(operator_cli admin billing finance record-business-evidence --tenant-id "$tenant" --profile-sha256 "$fingerprint" --expected-revision 0 --reference stack-establishment-proof --valid-until "$expiry")
check 'operator approval retained against exact profile' json_has "$approved" '.country=="US" and .revision=="1" and .reference=="stack-establishment-proof"'
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
revoked=$(operator_cli admin billing finance record-business-evidence --tenant-id "$tenant" --revoke --reference stack-proof-revoked)
check 'operator revocation has next revision' json_has "$revoked" '.revoked==true and .revision=="2"'
if operator_cli admin billing finance record-business-evidence --tenant-id "$tenant" --profile-sha256 "$fingerprint" --expected-revision 0 --reference stale-proof --valid-until "$expiry" >"$STACK_STATE_DIR/finance-stale.out" 2>&1; then
 fail 'stale approval revived revoked proof'
else
 check 'stale approval rejected' grep -q Aborted "$STACK_STATE_DIR/finance-stale.out"
fi
check 'NL consumer profile saved' json_has "$(profile consumer NL)" '.errors == null'
check 'deployed NL consumer settles one invoice with 21% VAT on top and net credit' deployed_crypto 2100
deployed_stripe() {
 STACK_BILLING_TENANT_ID="$tenant" STACK_BILLING_DATABASE_URL="$STACK_BILLING_DATABASE_URL" STACK_BILLING_DEPLOYED=1 \
  "$STACK_REPO/bin/stack-billing-runner" -test.run "^$1$" -test.v -test.timeout 120s
}
check 'deployed NL card top-up collects gross and credits net once' deployed_stripe TestStackDeployedCardTopupTax
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
 rows=[json.loads(line) for line in z.read('evidence/business_establishment_reviews.jsonl').splitlines()]
 mine=[r for r in rows if r['tenant_id']==tenant]
 assert len(mine)==2 and {r['revision'] for r in mine}=={1,2},mine
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
regular_invoice() {
 local country=$1 type=$2 vat=$3 rate=$4 result proof inspection fingerprint
 fresh_invoice_tenant "$country" || return 1
 json_has "$(profile "$type" "$country" "$vat")" '.errors == null' || return 1
 if [ "$country" = US ]; then
  inspection=$(operator_cli admin billing finance tax-profile --tenant-id "$tenant") || return 1
  fingerprint=$(printf '%s' "$inspection" | jq -r .profile_sha256)
  proof=$(operator_cli admin billing finance record-business-evidence --tenant-id "$tenant" --profile-sha256 "$fingerprint" --expected-revision 0 --reference stack-invoice-establishment --valid-until "$expiry") || return 1
  json_has "$proof" '.revision=="1"' || return 1
 fi
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
 operator_cli admin billing grant revoke --tenant-id "$tenant" --reason 'stack tax invoice matrix complete' --output json >/dev/null
}
check 'deployed regular NL consumer invoice has 21% VAT on top' regular_invoice NL consumer '' 2100
check 'deployed regular EU B2B invoice reverses charge with VIES proof' regular_invoice AT business ATU12345678 0
check 'deployed regular non-EU business invoice has zero Dutch VAT' regular_invoice US business '' 0
finish
