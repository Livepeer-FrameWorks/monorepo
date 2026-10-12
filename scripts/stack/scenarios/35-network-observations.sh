#!/usr/bin/env bash
# stack-scenario: manual
# The Livepeer network-observation pipeline end to end. The coordinator reads
# its catalog from the stack chain (scripts/stack/livepeer-chain.py: one
# orchestrator with an IP-literal URI, one behind a hostname). A prober speaking
# the network-prober protocol registers through GET /v1/work, answers the
# hostname with a DNS batch, receives assignments for both endpoints and reports
# a qualifying success for one and a qualifying timeout for the other, plus a
# heartbeat. A gateway reports discovery, state, a dispatched attempt and an
# accepted segment for the first orchestrator through Decklog, as the gateway
# fork does. Then:
#   - a wrong secret is refused on work and ingest and leaves no receipt; an
#     exact replay is acknowledged with the same ID and a conflicting reuse of
#     the ID is refused;
#   - every observation lands once in periscope.network_observations with the
#     reported facts, and the rollup loop's 5-minute and hourly snapshots and
#     the work snapshots equal the deduplicated raw rows;
#   - Bridge's public GET /v1/network/livepeer returns both orchestrators with
#     the observed latency, failure and accepted work, for the raw window and
#     for a window old enough to be served from the snapshots, without raw
#     observer identities; diagnostics refuse an anonymous caller;
#   - the public GraphQL orchestrator fields answer with the gateway's vantage
#     and instance.
# Catches: any break between prober, coordinator, Decklog, Kafka,
# periscope-ingest, the rollup loop, periscope-query and Bridge, a replay
# counted twice, and observer identities leaking onto the public route.
# Manual: it waits for the rollup loop's five-minute pass, and in the default
# order behind scenario 32's analytics backlog (about 12 minutes in all).
. "$(dirname "$0")/../lib.sh"

need curl jq python3 psql || finish

: "${NETWORK_OBSERVER_URL:=http://network-observer:18024}"
: "${LIVEPEER_CHAIN_URL:=http://livepeer-chain:8545}"
: "${NETWORK_OBSERVATION_NETWORK:=42161}"
: "${NETWORK_OBSERVATION_ENVIRONMENT:=stack}"
SECRET=${NETWORK_OBSERVER_SECRET:-}
[ -n "$SECRET" ] || { blocked "stack-runner has no NETWORK_OBSERVER_SECRET"; finish; }
stack_service_exists network-observer || { blocked "the slot runs no network-observer"; finish; }
TENANT=$STACK_TENANT_ID
NET=$NETWORK_OBSERVATION_NETWORK
ENVN=$NETWORK_OBSERVATION_ENVIRONMENT
PROBER=stack-prober-eu
REGION=stack-eu
GATEWAY=stack-livepeer-gateway-a
GATEWAY_REGION=stack-central
SINCE=$(utc_now)
RUN_START=$(date -u +%s)
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

now_rfc3339() { date -u +%Y-%m-%dT%H:%M:%S.%NZ; }
new_id() { cat /proc/sys/kernel/random/uuid; }
# The coordinator derives the IDs of the events it emits for a batch from the
# batch's IDs (uuid.NewSHA1 over the OID namespace, i.e. a version-5 UUID).
derived_id() { python3 -c 'import sys, uuid; print(uuid.uuid5(uuid.NAMESPACE_OID, sys.argv[1]))' "$1"; }
# observer <method> <path> <secret> [body file]: prints the HTTP status; the
# response body is in $work_dir/body.
observer() {
  local args=(-s -m 20 -o "$work_dir/body" -w '%{http_code}' -X "$1" -H "Authorization: Bearer $3")
  [ -n "${4:-}" ] && args+=(-H 'Content-Type: application/json' --data-binary "@$4")
  curl "${args[@]}" "$NETWORK_OBSERVER_URL$2"
}
body() { cat "$work_dir/body"; }

CHAIN=$(curl -s -m 10 "$LIVEPEER_CHAIN_URL/orchestrators")
ORCH_A=$(echo "$CHAIN" | jq -r '.orchestrators[0].address | ascii_downcase')
URI_A=$(echo "$CHAIN" | jq -r '.orchestrators[0].uri')
IP_A=$(echo "$CHAIN" | jq -r '.orchestrators[0].ip')
ORCH_B=$(echo "$CHAIN" | jq -r '.orchestrators[1].address | ascii_downcase')
URI_B=$(echo "$CHAIN" | jq -r '.orchestrators[1].uri')
IP_B=$(echo "$CHAIN" | jq -r '.orchestrators[1].ip')
[ -n "$ORCH_A" ] && [ "$ORCH_A" != null ] && [ -n "$ORCH_B" ] && [ "$ORCH_B" != null ] ||
  { fail "stack chain lists no orchestrators: $CHAIN"; finish; }

log "credentials"
check "work polling with a wrong secret is refused" [ "$(observer GET "/v1/work?observer_id=$PROBER&region=$REGION" wrong-secret)" = 401 ]
BAD_ID=$(new_id)
jq -cn --arg id "$BAD_ID" --arg o "$PROBER" --arg at "$(now_rfc3339)" \
  '{id:$id, observer_id:$o, report:{kind:"heartbeat", observed_at:$at, cadence_seconds:60, healthy:true}}' >"$work_dir/bad.json"
check "ingest with a wrong secret is refused" [ "$(observer POST /v1/observations wrong-secret "$work_dir/bad.json")" = 401 ]
check "inventory with a wrong secret is refused" [ "$(observer GET /v1/inventory wrong-secret)" = 401 ]
check "the refused batch left no ingest receipt" \
  [ "$(pg network_observer "SELECT count(*) FROM network_observer.ingest_receipts WHERE tenant_id='$TENANT' AND id='$BAD_ID'")" = 0 ]
diag=$(curl -s -m 20 -o /dev/null -w '%{http_code}' "$BRIDGE_URL/v1/network/livepeer/diagnostics")
check "Bridge diagnostics refuse an anonymous caller (HTTP $diag)" [ "$diag" = 401 ]

log "catalog"
catalog_read() {
  [ "$(observer GET /v1/inventory "$SECRET")" = 200 ] || return 1
  body | jq -e --arg a "$ORCH_A" --arg ua "$URI_A" --arg ia "$IP_A" --arg b "$ORCH_B" --arg ub "$URI_B" \
    --arg n "$NET" --arg e "$ENVN" '
    .network == $n and .environment == $e
    and any(.resources[]; .id == $a and .uri == $ua and .active and .catalog_source == "contract")
    and any(.resources[]; .id == $b and .uri == $ub and .active and .catalog_source == "contract")
    and any(.endpoints[]; .resource.id == $a and .ip == $ia)' >/dev/null
}
eventually 120 "the coordinator's catalog lists both chain orchestrators and the IP-literal endpoint" catalog_read || finish

log "prober registration and DNS"
check "the prober registers by polling for work" [ "$(observer GET "/v1/work?observer_id=$PROBER&region=$REGION" "$SECRET")" = 200 ]
body >"$work_dir/work.json"
RESOURCE_B=$(jq -c --arg b "$ORCH_B" '.catalog[] | select(.id == $b)' "$work_dir/work.json")
[ -n "$RESOURCE_B" ] || { fail "work catalog lacks $ORCH_B: $(jq -c .catalog "$work_dir/work.json")"; finish; }
DNS_ID=$(new_id)
jq -cn --arg id "$DNS_ID" --arg o "$PROBER" --argjson r "$RESOURCE_B" --arg ip "$IP_B" --arg at "$(now_rfc3339)" \
  '{id:$id, observer_id:$o, dns:[{resource:$r, ips:[$ip], observed_at:$at}]}' >"$work_dir/dns.json"
check "the DNS answer for the hostname orchestrator is accepted" [ "$(observer POST /v1/observations "$SECRET" "$work_dir/dns.json")" = 200 ]
REPORT_ID=$(new_id)
jq -cn --arg id "$REPORT_ID" --arg o "$PROBER" --arg at "$(now_rfc3339)" \
  '{id:$id, observer_id:$o, report:{kind:"heartbeat", observed_at:$at, cadence_seconds:60, queue_records:0, dropped_records:0, healthy:true}}' >"$work_dir/report.json"
check "the prober heartbeat is accepted" [ "$(observer POST /v1/observations "$SECRET" "$work_dir/report.json")" = 200 ]

assigned() {
  [ "$(observer GET "/v1/work?observer_id=$PROBER&region=$REGION" "$SECRET")" = 200 ] || return 1
  body >"$work_dir/work.json"
  jq -e --arg ia "$IP_A" --arg ib "$IP_B" --arg o "$PROBER" '
    any(.assignments[]; .endpoint.ip == $ia and .observer_id == $o and (.signature | length) == 64)
    and any(.assignments[]; .endpoint.ip == $ib and .observer_id == $o)' "$work_dir/work.json" >/dev/null
}
# A fresh endpoint is assigned at once; an endpoint whose last cycle failed
# waits out its backoff (2 minutes after one failed cycle).
eventually 300 "the prober is assigned a discovery check of both endpoints" assigned || finish

# result_batch <batch id> <result id> <ip> <success> <latency> <failure kind>:
# one assignment's result, shaped as internal/prober posts it (the signed
# assignment snapshot included).
result_batch() {
  jq -c --arg id "$1" --arg rid "$2" --arg ip "$3" --argjson ok "$4" --argjson ms "$5" --arg fk "$6" \
    --arg o "$PROBER" --arg at "$(now_rfc3339)" '
    (.assignments | map(select(.endpoint.ip == $ip)) | first) as $a
    | {id:$id, observer_id:$o, results:[{
        assignment:$a, id:$rid, assignment_id:$a.id, endpoint_id:$a.endpoint.id,
        check:({observer_id:$o, observed_at:$at, success:$ok, qualifying:true, latency_ms:$ms}
          + (if $ok then {actual_ip:$ip} else {failure_kind:$fk} end))}
        + (if $ok then {quote:{numerator:"1200", denominator:"1", currency:"ETH", unit:"pixel", observed_at:$at},
            capabilities:["h264"], hardware:"stack-gpu"} else {} end)]}' "$work_dir/work.json"
}

log "prober results"
BATCH_A=$(new_id) RESULT_A=$(new_id) BATCH_B=$(new_id) RESULT_B=$(new_id)
result_batch "$BATCH_A" "$RESULT_A" "$IP_A" true 42 "" >"$work_dir/a.json"
result_batch "$BATCH_B" "$RESULT_B" "$IP_B" false 5000 timeout >"$work_dir/b.json"
check "the success of $IP_A is accepted" [ "$(observer POST /v1/observations "$SECRET" "$work_dir/a.json")" = 200 ]
check "the acknowledgement names the batch" json_has "$(body)" ".accepted == true and .event_id == \"$BATCH_A\""
check "the timeout of $IP_B is accepted" [ "$(observer POST /v1/observations "$SECRET" "$work_dir/b.json")" = 200 ]
check "an exact replay is acknowledged" [ "$(observer POST /v1/observations "$SECRET" "$work_dir/a.json")" = 200 ]
check "the replay acknowledgement names the same batch" json_has "$(body)" ".accepted == true and .event_id == \"$BATCH_A\""
jq -c '.results[0].check.latency_ms = 7' "$work_dir/a.json" >"$work_dir/a-conflict.json"
check "a different body under the same batch ID is refused" [ "$(observer POST /v1/observations "$SECRET" "$work_dir/a-conflict.json")" = 409 ]
check "the replayed batch has one ingest receipt" \
  [ "$(pg network_observer "SELECT count(*) FROM network_observer.ingest_receipts WHERE tenant_id='$TENANT' AND id='$BATCH_A'")" = 1 ]

endpoint_state() {
  [ "$(observer GET /v1/inventory "$SECRET")" = 200 ] || return 1
  body | jq -e --arg ia "$IP_A" --arg ib "$IP_B" '
    any(.endpoints[]; .ip == $ia and .status == "up" and .price.numerator == "1200" and (.last_success | startswith("0001") | not))
    and any(.endpoints[]; .ip == $ib and .status == "unavailable" and .failure_streak >= 1)' >/dev/null
}
eventually 60 "the inventory shows $IP_A up with its quote and $IP_B unavailable" endpoint_state
outbox_drained() { [ "$(pg network_observer "SELECT count(*) FROM network_observer.telemetry_outbox WHERE tenant_id='$TENANT'")" = 0 ]; }
eventually 60 "the coordinator's telemetry outbox drains to Decklog" outbox_drained

log "Bridge public REST inventory"
# The inventory comes from the coordinator, not from analytics. Availability
# counts only checks from observers seen in the last two minutes, so the
# prober polls once more, as it does every five seconds.
check "the prober keeps polling for work" [ "$(observer GET "/v1/work?observer_id=$PROBER&region=$REGION" "$SECRET")" = 200 ]
code=$(curl -s -m 30 -o "$work_dir/inventory.json" -w '%{http_code}' "$BRIDGE_URL/v1/network/livepeer")
check "GET /v1/network/livepeer answers (HTTP $code)" [ "$code" = 200 ]
I=$(cat "$work_dir/inventory.json")
check "the projection is the stack namespace" json_has "$I" \
  ".inventory.network == \"$NET\" and .inventory.environment == \"$ENVN\""
check "the inventory lists both orchestrators" json_has "$I" \
  "any(.inventory.resources[]; .id == \"$ORCH_A\") and any(.inventory.resources[]; .id == \"$ORCH_B\")"
check "$IP_A is up with its quote; checks and assignments are withheld" json_has "$I" \
  "any(.inventory.endpoints[]; .ip == \"$IP_A\" and .status == \"up\" and .price.numerator == \"1200\" and .checks == null and .assigned_observers == null)"
check "$IP_B is unavailable from the checked location" json_has "$I" "any(.inventory.endpoints[]; .ip == \"$IP_B\" and .status == \"unavailable\")"
inventory_private() { ! grep -qE "$PROBER" "$work_dir/inventory.json"; }
check "the public inventory names no raw observer" inventory_private

log "gateway telemetry"
SESSION=stack-netobs-session-$RUN_START
# gateway_event <event id> <payload field> <payload json> [stream tenant]: one
# GatewayTelemetryEvent through Decklog's SendGatewayTelemetry, as the gateway
# fork sends it.
gateway_event() {
  jq -cn --arg id "$1" --arg field "$2" --argjson p "$3" --arg st "${4:-}" --arg g "$GATEWAY" --arg r "$GATEWAY_REGION" \
    --arg t "$TENANT" --arg n "$NET" --arg e "$ENVN" --arg at "$(now_rfc3339)" '
    {gatewayId:$g, gatewayRegion:$r, clusterId:"central-primary", clusterOwnerTenantId:$t, timestamp:$at,
     schemaVersion:3, eventId:$id, networkId:$n, environment:$e, producerKind:"gateway"}
    + {($field):$p} + (if $st == "" then {} else {streamTenantId:$st} end)' |
    stack_exec decklog sh -c 'grpcurl -plaintext -max-time 10 -H "authorization: Bearer $SERVICE_TOKEN" -d @ "localhost:${PORT:-18006}" helmsmancontrol.DecklogService/SendGatewayTelemetry' >/dev/null 2>"$work_dir/grpc.err"
}
G_DISCOVERY=$(new_id) G_STATE=$(new_id) G_ATTEMPT=$(new_id) G_SEGMENT=$(new_id)
VANTAGE=$(jq -cn --arg ip "$IP_A" '{resolvedIp:$ip, dialed:true}')
check "Decklog accepts the gateway's discovery" gateway_event "$G_DISCOVERY" discovery \
  "$(jq -cn --arg a "$ORCH_A" --arg u "$URI_A" --arg ip "$IP_A" --argjson v "$VANTAGE" \
    '{orchAddr:$a, orchUrl:$u, discoveryLatencyMs:37, reachable:true, vantage:$v, targetIp:$ip, samplingSource:"gateway", observerHealthy:true}')"
check "Decklog accepts the gateway's state" gateway_event "$G_STATE" state \
  "$(jq -cn --arg a "$ORCH_A" --arg u "$URI_A" --argjson v "$VANTAGE" \
    '{orchAddr:$a, canonicalUrl:$u, capabilities:["h264"], pricePerUnit:"1200", pixelsPerUnit:"1", priceKnown:true, priceCurrency:"ETH", priceUnit:"pixel", hardware:"stack-gpu", source:"gateway_pool", vantage:$v}')"
check "Decklog accepts the gateway's dispatched attempt" gateway_event "$G_ATTEMPT" transcode \
  "$(jq -cn --arg a "$ORCH_A" --arg u "$URI_A" --arg ip "$IP_A" --arg s "$SESSION" \
    '{orchAddr:$a, orchUrl:$u, sessionId:$s, manifestIdHash:"stack", seqNo:"1", success:true, overallMs:800, pixels:"1000000",
      profiles:["P240p30fps16x9"], resolvedIp:$ip, remoteIpVerified:true, sourceDurationMs:2000, disposition:"accepted",
      accounting:{version:1, requestDispatched:true, returnedPixelsKnown:true,
        serviceCharge:{numerator:"1200000000", denominator:"1", currency:"ETH", unit:"wei"}, ticketsSent:"1"}}')" "$TENANT"
check "Decklog accepts the gateway's accepted segment" gateway_event "$G_SEGMENT" segmentResult \
  "$(jq -cn --arg a "$ORCH_A" --arg u "$URI_A" --arg ip "$IP_A" --arg s "$SESSION" \
    '{orchAddr:$a, orchUrl:$u, resolvedIp:$ip, sessionId:$s, manifestIdHash:"stack", seqNo:"1", sourceDurationMs:2000, overallMs:900,
      attempts:1, success:true, profiles:["P240p30fps16x9"], workAccountingVersion:1, acceptedPixels:"1000000", acceptedRenditions:1}')" "$TENANT"
[ -s "$work_dir/grpc.err" ] && echo "    grpcurl: $(tr '\n' ' ' <"$work_dir/grpc.err")"

log "ClickHouse observations"
NS="tenant_id='$TENANT' AND provider='livepeer' AND network_id='$NET' AND environment='$ENVN'"
STATE_A=$(derived_id "$RESULT_A:state")
REPORT_EVENT=$(derived_id "$REPORT_ID:report")
EVENTS="'$RESULT_A','$STATE_A','$RESULT_B','$REPORT_EVENT','$G_DISCOVERY','$G_STATE','$G_ATTEMPT','$G_SEGMENT'"
rows_landed() {
  ROWS=$(ch "SELECT toString(event_id) AS id, kind, producer_kind, gateway_id, gateway_region, resource_id, resolved_ip,
      success, qualifying, latency_ms, failure_kind, price_numerator, accounting_version, accepted_pixels
    FROM periscope.network_observations FINAL WHERE $NS AND event_id IN ($EVENTS) FORMAT JSONEachRow" | jq -sc .)
  [ "$(echo "$ROWS" | jq length)" = 8 ]
}
# Periscope-ingest drains one analytics topic for every scenario at about ten
# events a second; scenario 32's viewer bursts (about 7,000 /play requests,
# one load-balancing event each) leave over ten minutes of backlog ahead of
# these events.
eventually 1200 "all 8 observations reached periscope.network_observations" rows_landed || { echo "    rows: $ROWS"; finish; }
row() { echo "$ROWS" | jq -e --arg id "$1" "any(.[]; .id == \$id and ($2))" >/dev/null; }
check "prober success: discovery of $IP_A, 42 ms, from $REGION" row "$RESULT_A" \
  ".kind == \"discovery\" and .producer_kind == \"prober\" and .gateway_id == \"$PROBER\" and .gateway_region == \"$REGION\" and .resource_id == \"$ORCH_A\" and .resolved_ip == \"$IP_A\" and .success == 1 and .qualifying == 1 and .latency_ms == 42"
check "prober success also records the quoted state" row "$STATE_A" ".kind == \"state\" and .producer_kind == \"prober\" and .price_numerator == \"1200\" and .resolved_ip == \"$IP_A\""
check "prober timeout: qualifying failure of $IP_B" row "$RESULT_B" \
  ".kind == \"discovery\" and .resource_id == \"$ORCH_B\" and .resolved_ip == \"$IP_B\" and .success == 0 and .qualifying == 1 and .failure_kind == \"timeout\""
check "prober heartbeat: coverage row" row "$REPORT_EVENT" ".kind == \"coverage\" and .gateway_id == \"$PROBER\" and .success == 1"
gateway_kinds() { echo "$ROWS" | jq -e '[.[] | select(.producer_kind == "gateway") | .kind] | sort == ["attempt","discovery","segment","state"]' >/dev/null; }
check "gateway rows: discovery, state, attempt and segment" gateway_kinds
check "the gateway's accepted segment carries its accounted pixels" row "$G_SEGMENT" ".accounting_version == 1 and (.accepted_pixels | tostring) == \"1000000\""
check "the refused conflicting body did not replace the reported latency" row "$RESULT_A" ".latency_ms == 42"
raw_copies=$(ch "SELECT count() FROM periscope.network_observations WHERE $NS AND event_id = '$RESULT_A'")
check "the replayed batch was emitted once (raw rows for its result: $raw_copies)" [ "$raw_copies" = 1 ]

log "Bridge public REST history"
raw_count() { # raw_count <kind> <ip> <region> <since epoch>
  ch "SELECT count() FROM periscope.network_observations FINAL WHERE $NS AND kind = '$1' AND resolved_ip = '$2' AND gateway_region = '$3' AND timestamp >= toDateTime($4)"
}
code=$(curl -s -m 30 -o "$work_dir/public.json" -w '%{http_code}' "$BRIDGE_URL/v1/network/livepeer")
check "GET /v1/network/livepeer answers (HTTP $code)" [ "$code" = 200 ]
P=$(cat "$work_dir/public.json")
check "the history is available" json_has "$P" '.history_status == "available"'
day_ago=$(($(date -u +%s) - 86400))
samples_a=$(raw_count discovery "$IP_A" "$REGION" "$day_ago")
check "summary for $IP_A from $REGION: $samples_a sample(s), all successes, p50 42 ms (the raw count, so the replay counted once)" json_has "$P" \
  "any(.history.summaries[]; .kind == \"discovery\" and .resource_id == \"$ORCH_A\" and .ip == \"$IP_A\" and .region == \"$REGION\" and .samples == $samples_a and .successes == $samples_a and .failures == 0 and .p50_ms == 42)"
samples_b=$(raw_count discovery "$IP_B" "$REGION" "$day_ago")
check "summary for $IP_B from $REGION: $samples_b qualifying failure(s)" json_has "$P" \
  "any(.history.summaries[]; .kind == \"discovery\" and .resource_id == \"$ORCH_B\" and .ip == \"$IP_B\" and .region == \"$REGION\" and .failures == $samples_b and .successes == 0)"
check "the gateway's discovery of $IP_A is a separate cohort at 37 ms" json_has "$P" \
  "any(.history.summaries[]; .kind == \"discovery\" and .ip == \"$IP_A\" and .region == \"$GATEWAY_REGION\" and .p50_ms == 37)"
check "raw samples include the 42 ms check without its observer ID" json_has "$P" \
  "any(.history.observations[]; .event_id == \"$RESULT_A\" and .latency_ms == 42 and .observer_id == null)"
segments_a=$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $NS AND kind='segment' AND resolved_ip='$IP_A' AND success=1 AND accounting_version=1 AND timestamp >= toDateTime($day_ago)")
check "work for $IP_A: $segments_a accepted segment(s) and a dispatched attempt" json_has "$P" \
  "any(.history.work.summaries[]; .resource_id == \"$ORCH_A\" and .ip == \"$IP_A\" and .accepted_segments == $segments_a and .dispatched_attempts >= 1 and (.accepted_pixels | tonumber) >= 1000000)"
no_identities() { ! grep -qE "$PROBER|$GATEWAY|$SESSION" "$work_dir/public.json"; }
check "no raw observer or gateway identity and no session reaches the public route" no_identities

log "GraphQL"
public_gql() {
  curl -s -m 30 "$BRIDGE_URL/graphql" -H 'Content-Type: application/json' \
    --data "$(jq -cn --arg q "$1" --argjson v "$2" '{query:$q, variables:$v}')"
}
gql_orchestrator() {
  G=$(public_gql 'query($a:String!){orchestrator(orchAddr:$a){orchestrator{orchAddr} instances{resolvedIp canonicalUrl capabilities} vantages{gatewayId gatewayRegion resolvedIp latestLatencyMs}} orchestratorsConnection(orchAddr:$a){totalCount nodes{orchAddr}}}' \
    "$(jq -cn --arg a "$ORCH_A" '{a:$a}')")
  json_has "$G" "(.errors == null) and .data.orchestrator.orchestrator.orchAddr == \"$ORCH_A\"
    and any(.data.orchestrator.vantages[]; .gatewayId == \"$GATEWAY\" and .resolvedIp == \"$IP_A\" and .latestLatencyMs == 37)
    and any(.data.orchestrator.instances[]; .resolvedIp == \"$IP_A\" and .canonicalUrl == \"$URI_A\")
    and .data.orchestratorsConnection.totalCount >= 1"
}
eventually 90 "anonymous GraphQL answers the orchestrator with the gateway's vantage (37 ms) and instance" gql_orchestrator ||
  echo "    graphql: $(echo "$G" | jq -c .)"
check "GraphQL vantages carry gateway observations only, never the prober" json_has "$G" \
  "all(.data.orchestrator.vantages[]; .gatewayId != \"$PROBER\")"

log "rollups"
# Snapshots must equal the deduplicated raw rows of every bucket this run
# touched, for both orchestrators.
RUN_BUCKET_5M=$((RUN_START / 300 * 300))
RUN_BUCKET_1H=$((RUN_START / 3600 * 3600))
SCOPE="$NS AND resource_id IN ('$ORCH_A','$ORCH_B')"
raw_cohorts() { # raw_cohorts <interval> <first bucket epoch>
  ch "SELECT toUnixTimestamp(toStartOfInterval(timestamp, INTERVAL $1 SECOND)) AS b, kind, resource_id, resolved_ip, gateway_region,
      count() AS samples, countIf(success=1 AND qualifying=1) AS successes, countIf(success=0 AND qualifying=1) AS failures
    FROM periscope.network_observations FINAL WHERE $SCOPE AND toStartOfInterval(timestamp, INTERVAL $1 SECOND) >= toDateTime($2)
    GROUP BY b, kind, resource_id, resolved_ip, gateway_region ORDER BY b, kind, resource_id, resolved_ip, gateway_region FORMAT TSV"
}
rollup_cohorts() { # rollup_cohorts <interval> <first bucket epoch>
  ch "SELECT toUnixTimestamp(bucket) AS b, kind, resource_id, resolved_ip, gateway_region, sum(samples), sum(successes), sum(failures)
    FROM periscope.network_observation_rollups FINAL WHERE $SCOPE AND interval_seconds = $1 AND bucket >= toDateTime($2)
    GROUP BY b, kind, resource_id, resolved_ip, gateway_region ORDER BY b, kind, resource_id, resolved_ip, gateway_region FORMAT TSV"
}
WORK_COLUMNS="countIf(kind='attempt' AND accounting_version=1 AND request_dispatched=1),
  sumIf(returned_pixels, kind='attempt' AND accounting_version=1 AND returned_pixels_known=1),
  countIf(kind='segment' AND accounting_version=1 AND success=1),
  sumIf(toUInt64(source_duration_ms), kind='segment' AND accounting_version=1 AND success=1),
  sumIf(accepted_pixels, kind='segment' AND accounting_version=1 AND success=1),
  countIf(service_charge_known=1)"
raw_work() { # raw_work <interval> <first bucket epoch>
  ch "SELECT toUnixTimestamp(toStartOfInterval(timestamp, INTERVAL $1 SECOND)) AS b, resource_id, resolved_ip, gateway_region, $WORK_COLUMNS
    FROM periscope.network_observations FINAL WHERE $SCOPE AND kind IN ('attempt','segment') AND toStartOfInterval(timestamp, INTERVAL $1 SECOND) >= toDateTime($2)
    GROUP BY b, resource_id, resolved_ip, gateway_region ORDER BY b, resource_id, resolved_ip, gateway_region FORMAT TSV"
}
rollup_work() { # rollup_work <interval> <first bucket epoch>
  ch "SELECT toUnixTimestamp(bucket) AS b, resource_id, resolved_ip, gateway_region, sum(dispatched_attempts), sum(returned_pixels),
      sum(accepted_segments), sum(accepted_source_ms), sum(accepted_pixels), sum(service_charge_samples)
    FROM periscope.network_work_rollups FINAL WHERE $SCOPE AND interval_seconds = $1 AND bucket >= toDateTime($2)
    GROUP BY b, resource_id, resolved_ip, gateway_region ORDER BY b, resource_id, resolved_ip, gateway_region FORMAT TSV"
}
snapshots_match() {
  local raw
  raw=$(raw_cohorts 300 "$RUN_BUCKET_5M")
  [ -n "$raw" ] && [ "$raw" = "$(rollup_cohorts 300 "$RUN_BUCKET_5M")" ] || return 1
  raw=$(raw_cohorts 3600 "$RUN_BUCKET_1H")
  [ "$raw" = "$(rollup_cohorts 3600 "$RUN_BUCKET_1H")" ] || return 1
  raw=$(raw_work 300 "$RUN_BUCKET_5M")
  [ -n "$raw" ] && [ "$raw" = "$(rollup_work 300 "$RUN_BUCKET_5M")" ] || return 1
  raw=$(raw_work 3600 "$RUN_BUCKET_1H")
  [ "$raw" = "$(rollup_work 3600 "$RUN_BUCKET_1H")" ]
}
# Periscope-ingest rebuilds changed buckets every five minutes.
if ! eventually 420 "observation and work snapshots (5m and 1h) equal the deduplicated raw rows" snapshots_match; then
  echo "    raw 5m:"; raw_cohorts 300 "$RUN_BUCKET_5M" | sed 's/^/      /'
  echo "    rollup 5m:"; rollup_cohorts 300 "$RUN_BUCKET_5M" | sed 's/^/      /'
  echo "    raw work 5m:"; raw_work 300 "$RUN_BUCKET_5M" | sed 's/^/      /'
  echo "    rollup work 5m:"; rollup_work 300 "$RUN_BUCKET_5M" | sed 's/^/      /'
fi
snapshot_has_success() {
  rollup_cohorts 300 "$RUN_BUCKET_5M" | awk -F'\t' -v ip="$IP_A" -v r="$REGION" \
    '$2 == "discovery" && $4 == ip && $5 == r && $6 >= 1 && $7 >= 1 { ok = 1 } END { exit !ok }'
}
snapshot_has_segment() {
  rollup_work 300 "$RUN_BUCKET_5M" | awk -F'\t' -v ip="$IP_A" '$3 == ip && $7 >= 1 && $9 >= 1000000 { ok = 1 } END { exit !ok }'
}
check "the 5-minute snapshot has the success of $IP_A" snapshot_has_success
check "the work snapshot has the accepted segment of $IP_A" snapshot_has_segment

log "Bridge public REST from snapshots"
# A window reaching past raw retention is served from the hourly snapshots.
start=$(date -u -d '31 days ago' +%Y-%m-%dT%H:%M:%SZ)
end=$(date -u -d '2 hours' +%Y-%m-%dT%H:%M:%SZ)
code=$(curl -s -m 30 -o "$work_dir/rolled.json" -w '%{http_code}' "$BRIDGE_URL/v1/network/livepeer?start=$start&end=$end")
check "the 31-day window answers (HTTP $code)" [ "$code" = 200 ]
R=$(cat "$work_dir/rolled.json")
all_a=$(raw_count discovery "$IP_A" "$REGION" 0)
check "the 31-day window is served from snapshots with $all_a sample(s) for $IP_A" json_has "$R" \
  ".history.rolled_up == true and any(.history.summaries[]; .kind == \"discovery\" and .ip == \"$IP_A\" and .region == \"$REGION\" and .samples == $all_a and .p50_ms == 42)"
all_segments=$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $NS AND kind='segment' AND resolved_ip='$IP_A' AND success=1 AND accounting_version=1")
check "the 31-day window's work comes from the work snapshots ($all_segments accepted segment(s))" json_has "$R" \
  ".history.work.interval_seconds == 3600 and any(.history.work.summaries[]; .ip == \"$IP_A\" and .accepted_segments == $all_segments)"

log "logs"
errors=$(log_json "$SINCE" 'select(.level == "error" or .level == "fatal" or .level == "panic") | "\(.msg) \(.error // "")"' \
  network-observer decklog periscope-ingest periscope-query bridge | sort | uniq -c)
if [ -z "$errors" ]; then
  pass "no errors from the observer, Decklog, Periscope or Bridge"
else
  fail "errors from the observer, Decklog, Periscope or Bridge"
  echo "$errors" | head -20 | sed 's/^/    /'
fi

finish
