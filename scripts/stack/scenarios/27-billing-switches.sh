#!/usr/bin/env bash
# stack-scenario: default
# A disposable tenant crosses prepaid/postpaid boundaries through the operator
# CLI while the tenant sees the resulting statements and invoices through
# GraphQL and the billing document download API.
. "$(dirname "$0")/../lib.sh"

need jq curl python3 psql || finish
[ -x "$STACK_REPO/bin/stack-billing-runner" ] || { blocked 'billing driver missing; make build-stack-billing-runner'; finish; }
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

email=$(test_email "billing-$(date +%s)-$RANDOM")
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
if set_prepaid=$(cli_json admin billing set-tier --tenant-id "$tenant" --tier payg --billing-model prepaid \
  --reason 'stack billing switch test' --output json); then
  pass 'operator assigned prepaid tier'
else
  fail 'operator assigned prepaid tier'
  finish
fi
check 'prepaid assignment returned a JSON tier' json_has "$set_prepaid" '.tier_name == "payg" and .billing_model == "prepaid" and .primary_cluster_id != "central-primary"'

if credit=$(cli_json admin billing credit --tenant-id "$tenant" --amount 10.00 \
  --reason 'stack billing switch test credit' --output json); then
  pass 'operator credited prepaid balance'
else
  fail "operator credit failed: $credit"
fi
balance_before=$(pg purser "SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id='$tenant' AND currency='EUR'")
check 'prepaid credit is exactly EUR 10' [ "$balance_before" = 1000 ]
period_end=$(pg purser "SELECT billing_period_end AT TIME ZONE 'UTC' FROM purser.tenant_subscriptions WHERE tenant_id='$tenant'")

if grant=$(cli_json admin billing grant set --tenant-id "$tenant" --tier production --base-fee 1000.00 \
  --collection invoice --reason 'stack billing switch test' --output json); then
  pass 'operator granted paid postpaid tier'
else
  fail "operator grant failed: $grant"
  finish
fi
show=$(cli_json admin billing grant show --tenant-id "$tenant" --output json)
check 'grant show returned JSON' [ "$?" = 0 ]
check 'grant show retains tier, model, and collection' json_has "$show" \
  '.tier.tier_name == "production" and .tier.billing_model == "postpaid" and .grant.collection == "invoice"'
statement_count=$(pg purser "SELECT count(*) FROM purser.billing_invoices
  WHERE tenant_id='$tenant' AND document_kind='prepaid_statement' AND usage_details->'statement'->>'closes_prepaid_phase'='true'")
check 'prepaid phase closed with exactly one statement' [ "$statement_count" = 1 ]
statement=$(pg purser "SELECT id::text FROM purser.billing_invoices
  WHERE tenant_id='$tenant' AND document_kind='prepaid_statement' AND usage_details->'statement'->>'closes_prepaid_phase'='true' LIMIT 1")
balance_after=$(pg purser "SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id='$tenant' AND currency='EUR'")
check 'unused prepaid credit remained exactly EUR 10 at switch' [ "$balance_after" = 1000 ]
after_grant_end=$(pg purser "SELECT billing_period_end AT TIME ZONE 'UTC' FROM purser.tenant_subscriptions WHERE tenant_id='$tenant'")
check 'postpaid switch preserved the calendar period end' [ "$after_grant_end" = "$period_end" ]

# Issue tenant API traffic during the paid phase and wait for its actual
# five-minute metering report to reach Purser before closing that phase.
api_before=$(pg purser "SELECT COALESCE(SUM(usage_value),0)::numeric(20,0)
  FROM purser.usage_records WHERE tenant_id='$tenant' AND usage_type='api_requests'")
for n in $(seq 1 30); do
  usage_query=$(gql 'query{streamsConnection(page:{first:1}){nodes{id}}}' '{}' "$jwt")
  if ! json_has "$usage_query" '.errors == null and (.data.streamsConnection.nodes | type) == "array"'; then
    fail "paid-phase API request $n failed: $usage_query"
    break
  fi
done
api_usage() {
  local quantity
  quantity=$(pg purser "SELECT COALESCE(SUM(usage_value),0)::numeric(20,0)
    FROM purser.usage_records WHERE tenant_id='$tenant' AND usage_type='api_requests'")
  echo "    metered API requests: $quantity (before paid traffic: $api_before)"
  [ "$((${quantity:-0} - ${api_before:-0}))" -ge 30 ]
}
eventually 900 'paid-phase API traffic reached Purser through Periscope metering' api_usage
if jwt=$(refresh_session_token "$cookies"); then
  pass 'billing session renewed after paid-phase settlement'
else
  fail 'billing session renewal after paid-phase settlement failed'
  finish
fi
balance_postpaid=$(pg purser "SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id='$tenant' AND currency='EUR'")
draft_credit=$(pg purser "SELECT COALESCE(SUM(prepaid_credit_applied), 0)::numeric(12,2) FROM purser.billing_invoices
  WHERE tenant_id='$tenant' AND status='draft'")
check 'prepaid balance stays EUR 10 while the postpaid period runs' [ "$balance_postpaid" = 1000 ]
check 'open postpaid draft holds no prepaid credit' [ "$draft_credit" = 0.00 ]

if back=$(cli_json admin billing set-tier --tenant-id "$tenant" --tier payg --billing-model prepaid \
  --reason 'stack billing switch test return' --output json); then
  pass 'operator switched back to prepaid'
else
  fail 'operator switched back to prepaid'
  finish
fi
check 'return assignment reports prepaid tier' json_has "$back" '.tier_name == "payg" and .billing_model == "prepaid"'
invoice_count=$(pg purser "SELECT count(*) FROM purser.billing_invoices
  WHERE tenant_id='$tenant' AND document_kind='invoice' AND usage_details->>'closes_postpaid_phase'='true'
    AND usage_details->'tier_info'->>'tier_name'='production'")
check 'exactly one invoice closed the postpaid phase' [ "$invoice_count" = 1 ]
invoice=$(pg purser "SELECT id::text FROM purser.billing_invoices
  WHERE tenant_id='$tenant' AND document_kind='invoice' AND usage_details->>'closes_postpaid_phase'='true'
    AND usage_details->'tier_info'->>'tier_name'='production'
  ORDER BY created_at DESC LIMIT 1")
check 'postpaid phase closed with a finalized invoice' [ -n "$invoice" ]
invoice_api=0
if [ -n "$invoice" ]; then
  invoice_api=$(pg purser "SELECT COALESCE((usage_details->>'api_requests')::numeric,0)::numeric(20,2)
    FROM purser.billing_invoices WHERE id='$invoice' AND tenant_id='$tenant'")
fi
echo "    postpaid invoice API requests: $invoice_api"
check 'postpaid invoice retained paid-phase metered API usage' \
  awk -v quantity="$invoice_api" 'BEGIN {exit !(quantity > 0)}'
after_return_end=$(pg purser "SELECT billing_period_end AT TIME ZONE 'UTC' FROM purser.tenant_subscriptions WHERE tenant_id='$tenant'")
check 'return to prepaid preserved the calendar period end' [ "$after_return_end" = "$period_end" ]
continuity=$(pg purser "SELECT (s.period_end=i.period_start AND i.period_end=t.billing_period_start)::int
  FROM purser.billing_invoices s, purser.billing_invoices i, purser.tenant_subscriptions t
  WHERE s.id='$statement' AND i.id='$invoice' AND t.tenant_id='$tenant'")
check 'prepaid and postpaid phases meet without a gap or overlap' [ "$continuity" = 1 ]
base=$(pg purser "SELECT base_amount::text FROM purser.billing_invoices WHERE id='$invoice' AND tenant_id='$tenant'")
expected_base=$(pg purser "SELECT ((
  round(100000 * floor(extract(epoch FROM i.period_end-a.start_at)) /
    floor(extract(epoch FROM t.billing_period_end-a.start_at))) -
  round(100000 * floor(extract(epoch FROM i.period_start-a.start_at)) /
    floor(extract(epoch FROM t.billing_period_end-a.start_at)))
  ) / 100)::numeric(12,2)
  FROM purser.billing_invoices i, purser.tenant_subscriptions t,
    (SELECT min(period_start) start_at FROM purser.billing_invoices WHERE tenant_id='$tenant') a
  WHERE i.id='$invoice' AND t.tenant_id='$tenant'")
echo "    prorated base: actual=$base expected=$expected_base"
check 'postpaid base fee was prorated exactly once' [ "$base" = "$expected_base" ]
check 'postpaid stretch has a positive prorated base fee' awk -v base="$base" 'BEGIN {exit !(base > 0)}'
if [ -n "$invoice" ]; then
  visible=$(gql 'query($id:ID!){invoice(id:$id){id status periodStart periodEnd baseAmount meteredAmount amount}}' \
    "$(jq -cn --arg id "$invoice" '{id:$id}')" "$jwt")
  check 'tenant GraphQL shows its finalized postpaid invoice' json_has "$visible" \
    ".errors == null and .data.invoice.id == \"$invoice\" and .data.invoice.status != \"DRAFT\""
fi
open_drafts=$(pg purser "SELECT count(*) FROM purser.billing_invoices WHERE tenant_id='$tenant' AND status='draft'")
check 'no draft invoice remains after return to prepaid' [ "$open_drafts" = 0 ]

download_document() {
  local kind=$1 id=$2 headers body code digest actual
  headers="$STACK_STATE_DIR/billing-$kind-$id.headers"
  body="$STACK_STATE_DIR/billing-$kind-$id.pdf"
  code=$(curl -s -m 20 -D "$headers" -o "$body" -w '%{http_code}' \
    -H "Authorization: Bearer $jwt" "$BRIDGE_URL/v1/billing/documents/$kind/$id")
  check "$kind document download returned 200" [ "$code" = 200 ]
  check "$kind document download is a PDF" bash -c 'test "$(head -c 5 "$1")" = "%PDF-"' _ "$body"
  check "$kind document download is served as application/pdf" \
    grep -qi '^content-type: application/pdf' "$headers"
  check "$kind document download is named .pdf" grep -qi '^content-disposition:.*\.pdf"' "$headers"
  digest=$(awk 'tolower($1)=="x-document-sha256:"{print $2}' "$headers" | tr -d '\r')
  actual=$(sha256sum "$body" | awk '{print $1}')
  check "$kind document integrity hash matches" [ "$digest" = "$actual" ]
}
download_document prepaid_statement "$statement"
download_document invoice "$invoice"

if revoke=$(cli_json admin billing grant revoke --tenant-id "$tenant" --reason 'stack billing test complete' --output json); then
  pass 'operator revoked billing grant'
else
  fail "operator revoke failed: $revoke"
fi
show=$(cli_json admin billing grant show --tenant-id "$tenant" --output json)
check 'grant show retains the tier after revoke' json_has "$show" '.tier.tier_name == "payg" and .grant == null'
# Meter actual traffic in the returned prepaid phase. The driver makes this
# fixture due at the last closed five-minute window and calls the production
# finalizer twice. Issued switch documents and global metering stay intact.
api_before=$(pg purser "SELECT COALESCE(SUM(usage_value),0)::numeric(20,0)
  FROM purser.usage_records WHERE tenant_id='$tenant' AND usage_type='api_requests'")
for n in $(seq 1 30); do
  usage_query=$(gql 'query{streamsConnection(page:{first:1}){nodes{id}}}' '{}' "$jwt")
  if ! json_has "$usage_query" '.errors == null and (.data.streamsConnection.nodes | type) == "array"'; then
    fail "returned-prepaid API request $n failed: $usage_query"
    break
  fi
done
eventually 900 'returned-prepaid traffic reached Purser through Periscope metering' api_usage
if jwt=$(refresh_session_token "$cookies"); then
  pass 'billing session renewed after returned-prepaid settlement'
else
  fail 'billing session renewal after returned-prepaid settlement failed'
  finish
fi
balance_before_month=$(pg purser "SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id='$tenant' AND currency='EUR'")
phase_start=$(pg purser "SELECT billing_period_start AT TIME ZONE 'UTC' FROM purser.tenant_subscriptions WHERE tenant_id='$tenant'")
export STACK_BILLING_TENANT_ID="$tenant" STACK_BILLING_FIXTURE_STATEMENT_ID="$statement"
if [ -z "${STACK_BILLING_DATABASE_URL:-}" ]; then
  STACK_BILLING_DATABASE_URL=$(PG_HOST="$PG_HOST" PG_PORT="$PG_PORT" python3 - <<'PYDSN'
import os, urllib.parse
print('postgresql://purser:' + urllib.parse.quote(os.environ.get('POSTGRES_PASSWORD',''), safe='') +
      '@' + os.environ['PG_HOST'] + ':' + os.environ['PG_PORT'] + '/purser?sslmode=disable')
PYDSN
  )
fi
export STACK_BILLING_DATABASE_URL
if "$STACK_REPO/bin/stack-billing-runner" -test.run '^TestStackMonthEnd$' -test.v -test.timeout 120s; then
  pass 'production month-end finalized once and refused duplicate finalization'
else
  fail 'production month-end finalization failed'
  finish
fi
monthly=$(pg purser "SELECT id::text FROM purser.billing_invoices WHERE tenant_id='$tenant'
  AND document_kind='prepaid_statement' AND period_start='$phase_start' AND status='paid'")
check 'month-end wrote a paid prepaid statement for the last phase' [ -n "$monthly" ]
[ -n "$monthly" ] || finish
calendar=$(pg purser "SELECT (s.period_start=i.period_end AND s.period_end=t.billing_period_start
  AND t.billing_period_end=t.billing_period_start + INTERVAL '1 month')::int
  FROM purser.billing_invoices s, purser.billing_invoices i, purser.tenant_subscriptions t
  WHERE s.id='$monthly' AND s.tenant_id='$tenant' AND i.id='$invoice' AND i.tenant_id='$tenant' AND t.tenant_id='$tenant'")
check 'month-end closed only the prepaid stretch and advanced one full calendar month' [ "$calendar" = 1 ]
monthly_api=$(pg purser "SELECT COALESCE((usage_details->>'api_requests')::numeric,0)
  FROM purser.billing_invoices WHERE tenant_id='$tenant' AND id='$monthly'")
check 'month-end statement contains returned-prepaid metered traffic' awk -v quantity="$monthly_api" 'BEGIN {exit !(quantity >= 30)}'
conservation=$(pg purser "SELECT (
  (SELECT COALESCE(SUM((usage_details->>'api_requests')::numeric),0) FROM purser.billing_invoices
    WHERE tenant_id='$tenant' AND base_fee_period_start IS NULL) =
  (SELECT COALESCE(SUM(quantity),0) FROM (
    SELECT usage_value AS quantity FROM purser.usage_records
    WHERE tenant_id='$tenant' AND usage_type='api_requests' AND value_kind='delta' AND granularity='minute_5'
      AND period_start < (SELECT period_end FROM purser.billing_invoices WHERE tenant_id='$tenant' AND id='$monthly')
    UNION ALL
    SELECT delta_value AS quantity FROM purser.usage_adjustments
    WHERE tenant_id='$tenant' AND usage_type='api_requests' AND status='applied' AND value_kind='correction_delta'
      AND period_start < (SELECT period_end FROM purser.billing_invoices WHERE tenant_id='$tenant' AND id='$monthly')
    ) ledger)
  )::int")
check 'all three billing phases account for actual API usage exactly once' [ "$conservation" = 1 ]
balance_after_month=$(pg purser "SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id='$tenant' AND currency='EUR'")
check 'month-end did not charge settled prepaid usage again' [ "$balance_after_month" = "$balance_before_month" ]
monthly_zero=$(pg purser "SELECT (amount=0 AND prepaid_credit_applied=0)::int
  FROM purser.billing_invoices WHERE tenant_id='$tenant' AND id='$monthly'")
check 'prepaid month-end statement has no invoice debt or extra credit' [ "$monthly_zero" = 1 ]
download_document prepaid_statement "$monthly"
report=$(cli_json admin billing prepaid-double-charges --tenant-id "$tenant" --output json)
check 'operator double-charge report completed' [ "$?" = 0 ]
check 'operator double-charge report has no findings' json_has "$report" '.charges == [] and .total_double_charged_cents == "0"'
finish
