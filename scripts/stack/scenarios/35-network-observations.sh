#!/usr/bin/env bash
# stack-scenario: manual
# The Livepeer network-observation pipeline end to end. The coordinator reads
# its catalog from the stack chain (scripts/stack/livepeer-chain.py: one
# orchestrator with an IP-literal URI, one behind a hostname). A prober speaking
# the network-prober protocol registers through GET /v1/work, answers the
# hostname with a DNS batch, receives assignments for both endpoints and reports
# a qualifying success for one and a qualifying timeout for the other, plus a
# heartbeat. Cell A's go-livepeer gateway reports to Decklog with the identity
# and Decklog settings the CLI renders for a production gateway
# (docker-compose.stack.yml); a stream published to cell A's edge is transcoded
# through it, so the gateway rows are its own discovery, state, admission
# decision, dispatched attempts, accepted segments and heartbeat. Then:
#   - a wrong secret is refused on work and ingest and leaves no receipt; an
#     exact replay is acknowledged with the same ID and a conflicting reuse of
#     the ID is refused;
#   - every prober observation lands once in periscope.network_observations
#     with the reported facts;
#   - every gateway row is attributed to the socket the gateway dialed
#     (livepeer-orch-a), the stream's attempts and accepted segments carry the
#     stream tenant and work accounting, each accepted segment's pixels equal
#     its dispatched attempt's returned pixels, and the gateway's heartbeat
#     lands as a coverage row;
#   - the rollup loop's 5-minute and hourly snapshots and the work snapshots
#     equal the deduplicated raw rows;
#   - Bridge's public GET /v1/network/livepeer returns both chain orchestrators
#     from the inventory, the prober's latency and failure, and the gateway's
#     discovery, segments and accepted work, for the raw window and for a
#     window old enough to be served from the snapshots, without raw observer
#     identities or the stream's name; diagnostics refuse an anonymous caller;
#   - the public GraphQL orchestrator fields answer with the gateway's vantage
#     and instance.
# Catches: any break between the gateway's Decklog client, the prober,
# coordinator, Decklog, Kafka, periscope-ingest, the rollup loop,
# periscope-query and Bridge, a replay counted twice, and observer identities
# leaking onto the public route.
# Not covered: service charges and ticket credit at a nonzero price. go-livepeer
# builds its price and its ticket sender and recipient only on a chain, so the
# offchain stack orchestrators quote no price and the gateway's attempts carry
# a known zero ticket credit and no service charge.
# Manual: it waits for the rollup loop's five-minute pass, and in the default
# order behind scenario 32's analytics backlog (about 12 minutes in all).
. "$(dirname "$0")/../lib.sh"

need curl jq python3 psql ffmpeg || finish

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
# Cell A's gateway identity and orchestrator, as docker-compose.stack.yml
# configures them.
GATEWAY=stack-lp-a
GATEWAY_REGION=eu-west
ORCH_URI=https://livepeer-orch-a:8935
ORCH_IP=$(python3 -c 'import socket; print(socket.gethostbyname("livepeer-orch-a"))')
SINCE=$(utc_now)
RUN_START=$(date -u +%s)
work_dir=$(mktemp -d)
SID="" PUB=""
cleanup() {
  [ -n "$PUB" ] && kill "$PUB" 2>/dev/null
  [ -n "$SID" ] && delete_stream "$SID"
  rm -rf "$work_dir"
}
trap cleanup EXIT

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

log "a live transcode through cell A's gateway"
# A 854x480@15 source keeps the work cheap: the 360p rung is transcoded and the
# 480p rung is equal-area, as in scenario 02. Two-second segments for 40 seconds.
S=$(create_stream "stack-netobs-$RUN_START" false)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
eventually 90 "cell A's Livepeer gateway is discoverable" gateway_discoverable stack-livepeer-gateway-a
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 854x480 15 40)
INTERNAL=$(internal_name_of "$UUID")
# The gateway's manifest ID, recorded as the segment's delivery session, is the
# Mist runtime name plus the Livepeer process's 8-character suffix.
DELIVERY_PREFIX="live+$INTERNAL-"
transcoded() { video_heights "$UUID" | grep -qx 360; }
eventually 90 "the stream carries the transcoded 360p rendition" transcoded
wait_publisher "$PUB" 120
PUB=""

log "ClickHouse observations"
NS="tenant_id='$TENANT' AND provider='livepeer' AND network_id='$NET' AND environment='$ENVN'"
STATE_A=$(derived_id "$RESULT_A:state")
REPORT_EVENT=$(derived_id "$REPORT_ID:report")
EVENTS="'$RESULT_A','$STATE_A','$RESULT_B','$REPORT_EVENT'"
rows_landed() {
  ROWS=$(ch "SELECT toString(event_id) AS id, kind, producer_kind, gateway_id, gateway_region, resource_id, resolved_ip,
      success, qualifying, latency_ms, failure_kind, price_numerator
    FROM periscope.network_observations FINAL WHERE $NS AND event_id IN ($EVENTS) FORMAT JSONEachRow" | jq -sc .)
  [ "$(echo "$ROWS" | jq length)" = 4 ]
}
# Periscope-ingest drains one analytics topic for every scenario at about ten
# events a second; scenario 32's viewer bursts (about 7,000 /play requests,
# one load-balancing event each) leave over ten minutes of backlog ahead of
# these events.
eventually 1200 "all 4 prober observations reached periscope.network_observations" rows_landed || { echo "    rows: $ROWS"; finish; }
row() { echo "$ROWS" | jq -e --arg id "$1" "any(.[]; .id == \$id and ($2))" >/dev/null; }
check "prober success: discovery of $IP_A, 42 ms, from $REGION" row "$RESULT_A" \
  ".kind == \"discovery\" and .producer_kind == \"prober\" and .gateway_id == \"$PROBER\" and .gateway_region == \"$REGION\" and .resource_id == \"$ORCH_A\" and .resolved_ip == \"$IP_A\" and .success == 1 and .qualifying == 1 and .latency_ms == 42"
check "prober success also records the quoted state" row "$STATE_A" ".kind == \"state\" and .producer_kind == \"prober\" and .price_numerator == \"1200\" and .resolved_ip == \"$IP_A\""
check "prober timeout: qualifying failure of $IP_B" row "$RESULT_B" \
  ".kind == \"discovery\" and .resource_id == \"$ORCH_B\" and .resolved_ip == \"$IP_B\" and .success == 0 and .qualifying == 1 and .failure_kind == \"timeout\""
check "prober heartbeat: coverage row" row "$REPORT_EVENT" ".kind == \"coverage\" and .gateway_id == \"$PROBER\" and .success == 1"
check "the refused conflicting body did not replace the reported latency" row "$RESULT_A" ".latency_ms == 42"
raw_copies=$(ch "SELECT count() FROM periscope.network_observations WHERE $NS AND event_id = '$RESULT_A'")
check "the replayed batch was emitted once (raw rows for its result: $raw_copies)" [ "$raw_copies" = 1 ]

GW_NS="$NS AND producer_kind='gateway' AND gateway_id='$GATEWAY' AND gateway_region='$GATEWAY_REGION' AND timestamp >= toDateTime($RUN_START)"
STREAM_SEGMENTS="$GW_NS AND kind='segment' AND startsWith(JSONExtractString(private_data, 'delivery_session_id'), '$DELIVERY_PREFIX')"
segment_count() { ch "SELECT count() FROM periscope.network_observations FINAL WHERE $STREAM_SEGMENTS"; }
# The gateway sends from its local spool; the stream's rows stop growing once
# the publisher has ended and the spool has drained.
gateway_settled() {
  local before
  before=$(segment_count)
  [ "${before:-0}" -ge 5 ] || return 1
  sleep 10
  [ "$(segment_count)" = "$before" ]
}
eventually 300 "the gateway's accepted segments for the stream reached ClickHouse and settled" gateway_settled || finish
SEGMENTS=$(segment_count)
GW_ORCH=$(ch "SELECT any(resource_id) FROM periscope.network_observations FINAL WHERE $GW_NS AND kind='discovery' AND resolved_ip='$ORCH_IP'")
# An offchain orchestrator has no Ethereum account, so it reports the zero
# address as its identity.
check "the gateway's discovery dialed $ORCH_URI at $ORCH_IP and identified orchestrator ${GW_ORCH:-<none>}" \
  [ "$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $GW_NS AND kind='discovery' AND success=1 AND qualifying=1
    AND resource_id='$GW_ORCH' AND endpoint_uri='$ORCH_URI' AND resolved_ip='$ORCH_IP'")" -ge 1 ]
check "the gateway reported the orchestrator's state" \
  [ "$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $GW_NS AND kind='state' AND resource_id='$GW_ORCH' AND endpoint_uri='$ORCH_URI'")" -ge 1 ]
check "the gateway reported its admission decision" \
  [ "$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $GW_NS AND kind='decision' AND disposition='accepted'
    AND JSONExtractString(private_data, 'stage') = 'admission'")" -ge 1 ]
# A failure without a connected socket stays unattributed (no IP); every other
# row carries the socket the gateway actually dialed.
misattributed=$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $GW_NS
  AND kind IN ('discovery','state','decision','attempt','segment')
  AND (resource_id != '$GW_ORCH' OR resolved_ip NOT IN ('$ORCH_IP', '') OR (success=1 AND resolved_ip != '$ORCH_IP'))")
check "every gateway row names the orchestrator and the socket it dialed ($misattributed other)" [ "$misattributed" = 0 ]
MANIFEST=$(ch "SELECT any(JSONExtractString(private_data, 'manifest_id_hash')) FROM periscope.network_observations FINAL WHERE $STREAM_SEGMENTS")
STREAM_ATTEMPTS="$GW_NS AND kind='attempt' AND JSONExtractString(private_data, 'manifest_id_hash') = '$MANIFEST'"
check "$SEGMENTS accepted segments for the stream, each with two renditions, the stream tenant and work accounting" \
  [ "$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $STREAM_SEGMENTS AND success=1 AND qualifying=1
    AND accounting_version=1 AND accepted_pixels > 0 AND accepted_renditions = 2 AND attempts >= 1
    AND private_tenant_id='$TENANT' AND profile_key != ''")" = "$SEGMENTS" ]
attempts=$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $STREAM_ATTEMPTS AND success=1
  AND disposition='workload' AND accounting_version=1 AND request_dispatched=1 AND returned_pixels_known=1 AND returned_pixels > 0
  AND source_duration_ms > 0 AND private_tenant_id='$TENANT'")
check "the stream's dispatched attempts are accounted with returned pixels ($attempts for $SEGMENTS segments)" [ "${attempts:-0}" -ge "$SEGMENTS" ]
unmatched=$(ch "SELECT count() FROM
    (SELECT JSONExtractUInt(private_data, 'seq_no') AS seq, accepted_pixels FROM periscope.network_observations FINAL WHERE $STREAM_SEGMENTS) s
  LEFT JOIN
    (SELECT JSONExtractUInt(private_data, 'seq_no') AS seq, max(returned_pixels) AS returned FROM periscope.network_observations FINAL
     WHERE $STREAM_ATTEMPTS AND success=1 GROUP BY seq) a USING seq
  WHERE a.returned != s.accepted_pixels")
check "each accepted segment's pixels equal its attempt's returned pixels ($unmatched differ)" [ "$unmatched" = 0 ]
gateway_heartbeat() {
  [ "$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $GW_NS AND kind='coverage' AND success=1")" -ge 1 ]
}
eventually 120 "the gateway's heartbeat is a coverage row" gateway_heartbeat
GW_LATENCY=$(ch "SELECT argMax(latency_ms, timestamp) FROM periscope.network_observations FINAL WHERE $GW_NS AND kind='discovery' AND resolved_ip='$ORCH_IP'")

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
check "raw samples include the 42 ms check without its observer ID" json_has "$P" \
  "any(.history.observations[]; .event_id == \"$RESULT_A\" and .latency_ms == 42 and .observer_id == null)"
gw_discovery=$(raw_count discovery "$ORCH_IP" "$GATEWAY_REGION" "$day_ago")
check "the gateway's discovery of $ORCH_IP is a $GATEWAY_REGION cohort of $gw_discovery sample(s)" json_has "$P" \
  "[.history.summaries[] | select(.kind == \"discovery\" and .resource_id == \"$GW_ORCH\" and .ip == \"$ORCH_IP\" and .region == \"$GATEWAY_REGION\") | .samples] | add == $gw_discovery"
gw_segments=$(raw_count segment "$ORCH_IP" "$GATEWAY_REGION" "$day_ago")
check "the gateway's segment cohorts for $ORCH_IP hold $gw_segments sample(s)" json_has "$P" \
  "[.history.summaries[] | select(.kind == \"segment\" and .resource_id == \"$GW_ORCH\" and .ip == \"$ORCH_IP\" and .region == \"$GATEWAY_REGION\") | .samples] | add == $gw_segments"
read -r accepted_gw pixels_gw <<<"$(ch "SELECT count(), sum(accepted_pixels) FROM periscope.network_observations FINAL WHERE $NS AND kind='segment'
  AND resolved_ip='$ORCH_IP' AND gateway_region='$GATEWAY_REGION' AND success=1 AND accounting_version=1 AND timestamp >= toDateTime($day_ago) FORMAT TSV")"
check "work for $ORCH_IP: $accepted_gw accepted segment(s) and $pixels_gw accepted pixels, all dispatched" json_has "$P" \
  "[.history.work.summaries[] | select(.resource_id == \"$GW_ORCH\" and .ip == \"$ORCH_IP\" and .region == \"$GATEWAY_REGION\")]
    | (map(.accepted_segments) | add) == $accepted_gw and (map(.accepted_pixels | tonumber) | add) == $pixels_gw
      and (map(.dispatched_attempts) | add) >= $accepted_gw"
no_identities() { ! grep -qF -e "$PROBER" -e "$GATEWAY" -e "$INTERNAL" "$work_dir/public.json"; }
check "no raw observer or gateway identity and no stream name reaches the public route" no_identities

log "GraphQL"
public_gql() {
  curl -s -m 30 "$BRIDGE_URL/graphql" -H 'Content-Type: application/json' \
    --data "$(jq -cn --arg q "$1" --argjson v "$2" '{query:$q, variables:$v}')"
}
gql_orchestrator() {
  G=$(public_gql 'query($g:String!, $a:String!){orchestrator(orchAddr:$g){orchestrator{orchAddr} instances{resolvedIp canonicalUrl capabilities} vantages{gatewayId gatewayRegion resolvedIp latestLatencyMs}} orchestratorsConnection(orchAddr:$g){totalCount nodes{orchAddr}} proberVantages: orchestratorVantages(orchAddr:$a){gatewayId}}' \
    "$(jq -cn --arg g "$GW_ORCH" --arg a "$ORCH_A" '{g:$g, a:$a}')")
  json_has "$G" "(.errors == null) and .data.orchestrator.orchestrator.orchAddr == \"$GW_ORCH\"
    and any(.data.orchestrator.vantages[]; .gatewayId == \"$GATEWAY\" and .gatewayRegion == \"$GATEWAY_REGION\" and .resolvedIp == \"$ORCH_IP\" and .latestLatencyMs == $GW_LATENCY)
    and any(.data.orchestrator.instances[]; .resolvedIp == \"$ORCH_IP\" and .canonicalUrl == \"$ORCH_URI\")
    and .data.orchestratorsConnection.totalCount >= 1"
}
eventually 90 "anonymous GraphQL answers the orchestrator with the gateway's vantage (${GW_LATENCY} ms) and instance" gql_orchestrator ||
  echo "    graphql: $(echo "$G" | jq -c .)"
check "GraphQL vantages carry gateway observations only, never the prober" json_has "$G" \
  "all(.data.proberVantages[]; .gatewayId != \"$PROBER\")"

log "rollups"
# Snapshots must equal the deduplicated raw rows of every bucket this run
# touched, for both chain orchestrators and the gateway's orchestrator.
RUN_BUCKET_5M=$((RUN_START / 300 * 300))
RUN_BUCKET_1H=$((RUN_START / 3600 * 3600))
SCOPE="$NS AND resource_id IN ('$ORCH_A','$ORCH_B','$GW_ORCH')"
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
  rollup_work 300 "$RUN_BUCKET_5M" | awk -F'\t' -v ip="$ORCH_IP" '$3 == ip && $7 >= 1 && $9 >= 1 { ok = 1 } END { exit !ok }'
}
check "the 5-minute snapshot has the success of $IP_A" snapshot_has_success
check "the work snapshot has the accepted segments of $ORCH_IP" snapshot_has_segment

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
all_segments=$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $NS AND kind='segment' AND resolved_ip='$ORCH_IP'
  AND gateway_region='$GATEWAY_REGION' AND success=1 AND accounting_version=1")
check "the 31-day window's work for $ORCH_IP comes from the work snapshots ($all_segments accepted segment(s))" json_has "$R" \
  ".history.work.interval_seconds == 3600
    and ([.history.work.summaries[] | select(.ip == \"$ORCH_IP\" and .region == \"$GATEWAY_REGION\") | .accepted_segments] | add) == $all_segments"

log "logs"
errors=$(log_json "$SINCE" 'select(.level == "error" or .level == "fatal" or .level == "panic") | "\(.msg) \(.error // "")"' \
  network-observer decklog periscope-ingest periscope-query bridge | sort | uniq -c)
if [ -z "$errors" ]; then
  pass "no errors from the observer, Decklog, Periscope or Bridge"
else
  fail "errors from the observer, Decklog, Periscope or Bridge"
  echo "$errors" | head -20 | sed 's/^/    /'
fi
undelivered=$(logs_since "$SINCE" "$STACK_LIVEPEER_GATEWAY_A" | grep -E 'frameworks telemetry (delivery pending|spool enqueue failed|: dropping)' | head -5)
check "the gateway delivered every telemetry record without a retry or drop" [ -z "$undelivered" ]
[ -n "$undelivered" ] && echo "$undelivered" | sed 's/^/    /'

finish
