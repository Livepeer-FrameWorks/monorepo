#!/usr/bin/env bash
# stack-scenario: default
# staging: safe
# tenant: qa
# A customer publishes RTMP and SRT on platform capacity. A stream placement
# policy set through the user-facing review/apply contract restricts publishing
# and viewing to platform capacity. RTMP also records DVR, decodes as a viewer,
# exports and replays a clip and the finalized chapter, and checks its signed
# lifecycle webhooks; both flows check final analytics against the admitted
# node and cluster. On staging it runs as the QA customer; scenario 51 covers a
# fresh Free signup and its private cluster on the stack.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 timeout || finish
run="tenant47-$(date +%s)-$RANDOM"
# Keep this run's ledger and webhook secret apart from the tenant's other runs.
export STACK_STATE_DIR="$STACK_STATE_DIR/$run"
mkdir -p "$STACK_STATE_DIR"
since=$(date +%s)
tenant=$STACK_TENANT_ID
stream="" publisher="" endpoint="" clip="" dvr_hash=""
streams=()
cleanup() {
  [ "${cleaned:-0}" = 0 ] || return 0
  cleaned=1
  [ -z "$publisher" ] || kill "$publisher" 2>/dev/null
  for id in "${streams[@]}"; do delete_stream "$id"; done
  [ -z "$clip" ] || gql 'mutation($id:ID!){deleteClip(id:$id){__typename}}' "$(jq -cn --arg id "$clip" '{id:$id}')" >/dev/null
  [ -z "$dvr_hash" ] || gql 'mutation($id:ID!){deleteDVR(dvrHash:$id){__typename}}' "$(jq -cn --arg id "$dvr_hash" '{id:$id}')" >/dev/null
  [ -z "$endpoint" ] || gql 'mutation($id:ID!){deleteWebhookEndpoint(id:$id){__typename}}' "$(jq -cn --arg id "$endpoint" '{id:$id}')" >/dev/null
  return 0
}
end_scenario() { cleanup || fail 'tenant stream cleanup failed'; finish; }
on_exit() { local rc=$?; cleanup || rc=1; exit "$rc"; }
trap on_exit EXIT

[ -n "$tenant" ] || { fail 'the run names no tenant'; end_scenario; }
active_tier() { pg purser "SELECT bt.tier_name FROM purser.tenant_subscriptions ts JOIN purser.billing_tiers bt ON bt.id=ts.tier_id WHERE ts.tenant_id='$tenant' AND ts.status='active'"; }
tier=$(active_tier)
[ -n "$tier" ] || { fail 'the tenant has an active plan'; end_scenario; }
. "$STACK_LIB_DIR/tenant-lifecycle.sh"
setup_tenant_webhooks || { fail 'the tenant creates its own webhook endpoint'; end_scenario; }
pass 'the tenant creates its own webhook endpoint'

log "the $tier tenant publishes on platform capacity"
# Platform node identities are hidden from tenants and a cell may run several
# edges, so the admitted node is read from the session, inside the cell's cluster.
for protocol in rtmp srt; do
  stream_flow PLATFORM_OFFICIAL "$EDGE_A_HOST" "$FOGHORN_A_DB" "" "$CELL_A_CLUSTER" "$protocol" "${FOGHORN_A_URLS%% *}" || end_scenario
done
check "streaming did not change the $tier plan" [ "$(active_tier)" = "$tier" ]
# Ledger workers run every five minutes with a two-minute settlement lag and
# a five-minute cursor boundary. Do not refresh them artificially for this test.
eventually 900 'both ended streams settle with exact runtime and nonzero viewer analytics' tenant_all_analytics
for id in "${streams[@]}"; do delete_stream "$id"; done
streams=()
check 'the tenant passes its own analytics, runtime, API and artifact sweep' env STACK_SWEEP_DOCTOR=0 bash "$STACK_LIB_DIR/sweep.sh" "$since"
end_scenario
