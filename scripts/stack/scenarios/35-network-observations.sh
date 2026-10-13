#!/usr/bin/env bash
# stack-scenario: manual
# The Livepeer network-observation pipeline end to end. The coordinator reads
# its catalog from the stack chain (scripts/stack/livepeer-chain.py: an
# orchestrator with a documentation-range IP-literal URI, an unresolvable
# hostname, and cell B's stack orchestrator by hostname).
#
# The released network-prober (compose service network-prober, configured as
# its deploy/network-prober.env.example describes, allowed the slot's subnet)
# runs beside the stack. It registers by polling GET /v1/work, resolves the
# catalog, and dials its assignments directly: cell B's orchestrator answers
# discovery from the socket the prober pinned, identifying itself with the zero
# address an offchain orchestrator has, so the check completes as a qualifying
# identity mismatch; the documentation-range endpoint is refused before
# dialing; the unresolvable hostname fails its DNS cycle. Its results, DNS
# cycle and heartbeat travel its spool, the coordinator, Decklog and Kafka to
# Periscope and the public route.
#
# A scripted prober speaking the same protocol covers what the real one cannot
# produce here: credentials it would never send (a wrong secret), an exact
# replay and a conflicting reuse of a batch ID, and a qualifying success with a
# quote, which needs an orchestrator whose identity is in the pool. Its ID
# sorts before the real prober's, so while it polls the coordinator assigns it
# every endpoint; the real prober's checks are asserted before it starts.
#
# Cell A's go-livepeer gateway reports to Decklog with the identity and Decklog
# settings the CLI renders for a production gateway (docker-compose.stack.yml);
# a stream published to cell A's edge is transcoded through it, so the gateway
# rows are its own discovery, state, admission decision, dispatched attempts,
# accepted segments and heartbeat. Then:
#   - every observation lands once in periscope.network_observations with the
#     reported facts;
#   - every gateway row is attributed to the socket the gateway dialed
#     (livepeer-orch-a), the stream's attempts and accepted segments carry the
#     stream tenant and work accounting, each accepted segment's pixels equal
#     its dispatched attempt's returned pixels, and the gateway's heartbeat
#     lands as a coverage row;
#   - the rollup loop's 5-minute and hourly snapshots and the work snapshots
#     equal the deduplicated raw rows;
#   - Bridge's public GET /v1/network/livepeer returns the chain orchestrators
#     from the inventory, both probers' results, and the gateway's discovery,
#     segments and accepted work, for the raw window and for a window old
#     enough to be served from the snapshots, without raw observer identities
#     or the stream's name; diagnostics refuse an anonymous caller;
#   - the public GraphQL orchestrator fields answer with the gateway's vantage
#     and instance, and its performance series counts the gateway's discovery
#     and attempts exactly as the raw rows and the public route do.
# Catches: any break between the gateway's Decklog client, the prober binary,
# coordinator, Decklog, Kafka, periscope-ingest, the rollup loop,
# periscope-query and Bridge, a replay counted twice, GraphQL history diverging
# from the public route, and observer identities leaking onto the public route.
# Not covered: service charges and ticket credit at a nonzero price, and a real
# prober's successful discovery. go-livepeer builds its price, its ticket sender
# and recipient and its Ethereum identity only on a chain, so the offchain stack
# orchestrators quote no price, report the zero address, and the gateway's
# attempts carry a known zero ticket credit and no service charge.
# Manual: it waits for the rollup loop's five-minute pass.
. "$(dirname "$0")/../lib.sh"

need curl jq python3 psql ffmpeg || finish

: "${NETWORK_OBSERVER_URL:=http://network-observer:18024}"
: "${LIVEPEER_CHAIN_URL:=http://livepeer-chain:8545}"
: "${NETWORK_OBSERVATION_NETWORK:=42161}"
: "${NETWORK_OBSERVATION_ENVIRONMENT:=stack}"
SECRET=${NETWORK_OBSERVER_SECRET:-}
[ -n "$SECRET" ] || { blocked "stack-runner has no NETWORK_OBSERVER_SECRET"; finish; }
stack_service_exists network-observer || { blocked "the slot runs no network-observer"; finish; }
stack_service_exists network-prober || { blocked "the slot runs no network-prober"; finish; }
TENANT=$STACK_TENANT_ID
NET=$NETWORK_OBSERVATION_NETWORK
ENVN=$NETWORK_OBSERVATION_ENVIRONMENT
PROBER=stack-prober-eu
REGION=stack-eu
# The real prober's identity, as docker-compose.stack.yml configures it.
REAL_PROBER=stack-prober-real
REAL_REGION=stack-us
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
ORCH_C=$(echo "$CHAIN" | jq -r '.orchestrators[2].address | ascii_downcase')
URI_C=$(echo "$CHAIN" | jq -r '.orchestrators[2].uri')
[ -n "$ORCH_A" ] && [ "$ORCH_A" != null ] && [ -n "$ORCH_B" ] && [ "$ORCH_B" != null ] && [ -n "$ORCH_C" ] && [ "$ORCH_C" != null ] ||
  { fail "stack chain lists no orchestrators: $CHAIN"; finish; }
# Cell B's orchestrator, as the real prober resolves its catalog URI.
IP_C=$(python3 -c 'import socket, sys, urllib.parse; print(socket.gethostbyname(urllib.parse.urlsplit(sys.argv[1]).hostname))' "$URI_C")

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
    --arg c "$ORCH_C" --arg uc "$URI_C" --arg n "$NET" --arg e "$ENVN" '
    .network == $n and .environment == $e
    and any(.resources[]; .id == $a and .uri == $ua and .active and .catalog_source == "contract")
    and any(.resources[]; .id == $b and .uri == $ub and .active and .catalog_source == "contract")
    and any(.resources[]; .id == $c and .uri == $uc and .active and .catalog_source == "contract")
    and any(.endpoints[]; .resource.id == $a and .ip == $ia)' >/dev/null
}
eventually 120 "the coordinator's catalog lists the chain orchestrators and the IP-literal endpoint" catalog_read || finish

log "the real network-prober"
NS="tenant_id='$TENANT' AND provider='livepeer' AND network_id='$NET' AND environment='$ENVN'"
REAL_NS="$NS AND producer_kind='prober' AND gateway_id='$REAL_PROBER' AND gateway_region='$REAL_REGION'"
# The real prober has polled since the stack started; these checks finish
# before this scenario's scripted prober polls and takes over the assignments.
real_registered() {
  [ "$(observer GET /v1/inventory "$SECRET")" = 200 ] || return 1
  body | jq -e --arg o "$REAL_PROBER" --arg r "$REAL_REGION" \
    'any(.observers[]; .id == $o and .kind == "prober" and .region == $r and (now - (.last_seen | sub("\\.[0-9]+"; "") | fromdateiso8601)) < 60)' >/dev/null
}
eventually 120 "$REAL_PROBER registered through GET /v1/work and keeps polling" real_registered
real_endpoint() {
  [ "$(observer GET /v1/inventory "$SECRET")" = 200 ] || return 1
  body | jq -e --arg c "$ORCH_C" --arg ip "$IP_C" \
    'any(.endpoints[]; .resource.id == $c and .ip == $ip and .status == "unavailable" and .failure_streak >= 1)' >/dev/null
}
eventually 240 "its DNS answer for $URI_C created endpoint $IP_C, unavailable after its identity-mismatch cycle" real_endpoint ||
  echo "    endpoints: $(body | jq -c --arg c "$ORCH_C" '[.endpoints[] | select(.resource.id == $c) | {ip, status, failure_streak, checks}]')"
real_discovery() {
  [ "$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $REAL_NS AND kind='discovery'
    AND resource_id='$ORCH_C' AND endpoint_uri='$URI_C' AND resolved_ip='$IP_C'
    AND success=0 AND qualifying=1 AND failure_kind='identity_mismatch' AND latency_ms > 0")" -ge 1 ]
}
eventually 120 "its discovery RPC to $IP_C, pinned to that socket, reached Periscope as a qualifying identity mismatch" real_discovery
real_policy() {
  [ "$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $REAL_NS AND kind='discovery'
    AND resource_id='$ORCH_A' AND resolved_ip='$IP_A' AND success=0 AND qualifying=0 AND failure_kind='endpoint_policy'")" -ge 1 ]
}
eventually 120 "it refused the documentation-range $IP_A before dialing, as a non-qualifying observation" real_policy
real_reports() {
  [ "$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $REAL_NS AND kind='coverage' AND success=1
    AND JSONExtractString(private_data, 'report_kind') = 'heartbeat' AND timestamp >= toDateTime($RUN_START - 120)")" -ge 1 ] &&
    [ "$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $REAL_NS AND kind='coverage'
      AND JSONExtractString(private_data, 'report_kind') = 'dns_cycle'
      AND JSONExtractUInt(private_data, 'expected') >= 3 AND JSONExtractUInt(private_data, 'failed') >= 1")" -ge 1 ]
}
eventually 120 "its heartbeat and DNS cycle (the unresolvable $URI_B counted failed) are coverage rows" real_reports
real_public() {
  local raw qualifying_failures
  code=$(curl -s -m 30 -o "$work_dir/real.json" -w '%{http_code}' "$BRIDGE_URL/v1/network/livepeer")
  [ "$code" = 200 ] || return 1
  raw=$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $NS AND kind='discovery' AND resource_id='$ORCH_C'
    AND resolved_ip='$IP_C' AND gateway_region='$REAL_REGION' AND timestamp >= now() - INTERVAL 1 DAY")
  qualifying_failures=$(ch "SELECT countIf(success=0 AND qualifying=1) FROM periscope.network_observations FINAL WHERE $NS AND kind='discovery'
    AND resource_id='$ORCH_C' AND resolved_ip='$IP_C' AND gateway_region='$REAL_REGION' AND timestamp >= now() - INTERVAL 1 DAY")
  jq -e --arg c "$ORCH_C" --arg ip "$IP_C" --arg r "$REAL_REGION" --argjson n "${raw:-0}" --argjson f "${qualifying_failures:-0}" '
    $n >= 1
    and ([.history.summaries[] | select(.kind == "discovery" and .resource_id == $c and .ip == $ip and .region == $r)]
      | (map(.samples) | add) == $n and (map(.failures) | add) == $f and (map(.successes) | add) == 0)
    and any(.inventory.resources[]; .id == $c)
    and any(.inventory.endpoints[]; .resource.id == $c and .ip == $ip and .status == "unavailable" and .checks == null)
    and any(.inventory.observers[]; .kind == "prober" and .region == $r)' "$work_dir/real.json" >/dev/null
}
eventually 90 "Bridge's public route carries its $REAL_REGION cohort for $IP_C (the raw counts), the endpoint and its measurement site" real_public
real_private() { ! grep -qE "$PROBER|$REAL_PROBER" "$work_dir/real.json"; }
check "the public route names neither prober" real_private

log "scripted prober: credentials, replay and a quoted success"
check "the scripted prober registers by polling for work" [ "$(observer GET "/v1/work?observer_id=$PROBER&region=$REGION" "$SECRET")" = 200 ]
assigned() {
  [ "$(observer GET "/v1/work?observer_id=$PROBER&region=$REGION" "$SECRET")" = 200 ] || return 1
  body >"$work_dir/work.json"
  jq -e --arg ia "$IP_A" --arg o "$PROBER" '
    any(.assignments[]; .endpoint.ip == $ia and .observer_id == $o and (.signature | length) == 64)' "$work_dir/work.json" >/dev/null
}
# The lowest-ID healthy prober takes each new cycle; the real prober's last
# cycle of $IP_A was non-qualifying, so the next one is due within a minute.
eventually 300 "the scripted prober is assigned a discovery check of $IP_A" assigned || finish

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

log "scripted prober results"
BATCH_A=$(new_id) RESULT_A=$(new_id)
result_batch "$BATCH_A" "$RESULT_A" "$IP_A" true 42 "" >"$work_dir/a.json"
check "the success of $IP_A is accepted" [ "$(observer POST /v1/observations "$SECRET" "$work_dir/a.json")" = 200 ]
check "the acknowledgement names the batch" json_has "$(body)" ".accepted == true and .event_id == \"$BATCH_A\""
check "an exact replay is acknowledged" [ "$(observer POST /v1/observations "$SECRET" "$work_dir/a.json")" = 200 ]
check "the replay acknowledgement names the same batch" json_has "$(body)" ".accepted == true and .event_id == \"$BATCH_A\""
jq -c '.results[0].check.latency_ms = 7' "$work_dir/a.json" >"$work_dir/a-conflict.json"
check "a different body under the same batch ID is refused" [ "$(observer POST /v1/observations "$SECRET" "$work_dir/a-conflict.json")" = 409 ]
check "the replayed batch has one ingest receipt" \
  [ "$(pg network_observer "SELECT count(*) FROM network_observer.ingest_receipts WHERE tenant_id='$TENANT' AND id='$BATCH_A'")" = 1 ]

endpoint_state() {
  [ "$(observer GET /v1/inventory "$SECRET")" = 200 ] || return 1
  body | jq -e --arg ia "$IP_A" '
    any(.endpoints[]; .ip == $ia and .status == "up" and .price.numerator == "1200" and (.last_success | startswith("0001") | not))' >/dev/null
}
eventually 60 "the inventory shows $IP_A up with its quote" endpoint_state
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
check "the inventory lists the chain orchestrators" json_has "$I" \
  "any(.inventory.resources[]; .id == \"$ORCH_A\") and any(.inventory.resources[]; .id == \"$ORCH_B\") and any(.inventory.resources[]; .id == \"$ORCH_C\")"
check "$IP_A is up with its quote; checks and assignments are withheld" json_has "$I" \
  "any(.inventory.endpoints[]; .ip == \"$IP_A\" and .status == \"up\" and .price.numerator == \"1200\" and .checks == null and .assigned_observers == null)"
inventory_private() { ! grep -qE "$PROBER|$REAL_PROBER" "$work_dir/inventory.json"; }
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
STATE_A=$(derived_id "$RESULT_A:state")
EVENTS="'$RESULT_A','$STATE_A'"
rows_landed() {
  ROWS=$(ch "SELECT toString(event_id) AS id, kind, producer_kind, gateway_id, gateway_region, resource_id, resolved_ip,
      success, qualifying, latency_ms, failure_kind, price_numerator
    FROM periscope.network_observations FINAL WHERE $NS AND event_id IN ($EVENTS) FORMAT JSONEachRow" | jq -sc .)
  [ "$(echo "$ROWS" | jq length)" = 2 ]
}
# Periscope-ingest drains one analytics topic for every scenario, inserting
# each poll batch of up to 500 events at once; scenario 32's viewer bursts
# (about 8,000 events) are behind it within seconds.
eventually 120 "both scripted prober observations reached periscope.network_observations" rows_landed || { echo "    rows: $ROWS"; finish; }
row() { echo "$ROWS" | jq -e --arg id "$1" "any(.[]; .id == \$id and ($2))" >/dev/null; }
check "prober success: discovery of $IP_A, 42 ms, from $REGION" row "$RESULT_A" \
  ".kind == \"discovery\" and .producer_kind == \"prober\" and .gateway_id == \"$PROBER\" and .gateway_region == \"$REGION\" and .resource_id == \"$ORCH_A\" and .resolved_ip == \"$IP_A\" and .success == 1 and .qualifying == 1 and .latency_ms == 42"
check "prober success also records the quoted state" row "$STATE_A" ".kind == \"state\" and .producer_kind == \"prober\" and .price_numerator == \"1200\" and .resolved_ip == \"$IP_A\""
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
# A public cohort is keyed by resource, IP and region; earlier scenarios leave
# rows for the same IP under other resources (a never-identified orchestrator's
# URL key), so the raw count names the cohort's resource too.
raw_count() { # raw_count <kind> <resource> <ip> <region> <since epoch>
  ch "SELECT count() FROM periscope.network_observations FINAL WHERE $NS AND kind = '$1' AND resource_id = '$2' AND resolved_ip = '$3' AND gateway_region = '$4' AND timestamp >= toDateTime($5)"
}
code=$(curl -s -m 30 -o "$work_dir/public.json" -w '%{http_code}' "$BRIDGE_URL/v1/network/livepeer")
check "GET /v1/network/livepeer answers (HTTP $code)" [ "$code" = 200 ]
P=$(cat "$work_dir/public.json")
check "the history is available" json_has "$P" '.history_status == "available"'
day_ago=$(($(date -u +%s) - 86400))
samples_a=$(raw_count discovery "$ORCH_A" "$IP_A" "$REGION" "$day_ago")
check "summary for $IP_A from $REGION: $samples_a sample(s), all successes, p50 42 ms (the raw count, so the replay counted once)" json_has "$P" \
  "any(.history.summaries[]; .kind == \"discovery\" and .resource_id == \"$ORCH_A\" and .ip == \"$IP_A\" and .region == \"$REGION\" and .samples == $samples_a and .successes == $samples_a and .failures == 0 and .p50_ms == 42)"
check "raw samples include the 42 ms check without its observer ID" json_has "$P" \
  "any(.history.observations[]; .event_id == \"$RESULT_A\" and .latency_ms == 42 and .observer_id == null)"
gw_discovery=$(raw_count discovery "$GW_ORCH" "$ORCH_IP" "$GATEWAY_REGION" "$day_ago")
check "the gateway's discovery of $ORCH_IP is a $GATEWAY_REGION cohort of $gw_discovery sample(s)" json_has "$P" \
  "[.history.summaries[] | select(.kind == \"discovery\" and .resource_id == \"$GW_ORCH\" and .ip == \"$ORCH_IP\" and .region == \"$GATEWAY_REGION\") | .samples] | add == $gw_discovery"
gw_segments=$(raw_count segment "$GW_ORCH" "$ORCH_IP" "$GATEWAY_REGION" "$day_ago")
check "the gateway's segment cohorts for $ORCH_IP hold $gw_segments sample(s)" json_has "$P" \
  "[.history.summaries[] | select(.kind == \"segment\" and .resource_id == \"$GW_ORCH\" and .ip == \"$ORCH_IP\" and .region == \"$GATEWAY_REGION\") | .samples] | add == $gw_segments"
read -r accepted_gw pixels_gw <<<"$(ch "SELECT count(), sum(accepted_pixels) FROM periscope.network_observations FINAL WHERE $NS AND kind='segment'
  AND resource_id='$GW_ORCH' AND resolved_ip='$ORCH_IP' AND gateway_region='$GATEWAY_REGION' AND success=1 AND accounting_version=1 AND timestamp >= toDateTime($day_ago) FORMAT TSV")"
check "work for $ORCH_IP: $accepted_gw accepted segment(s) and $pixels_gw accepted pixels, all dispatched" json_has "$P" \
  "[.history.work.summaries[] | select(.resource_id == \"$GW_ORCH\" and .ip == \"$ORCH_IP\" and .region == \"$GATEWAY_REGION\")]
    | (map(.accepted_segments) | add) == $accepted_gw and (map(.accepted_pixels | tonumber) | add) == $pixels_gw
      and (map(.dispatched_attempts) | add) >= $accepted_gw"
no_identities() { ! grep -qF -e "$PROBER" -e "$REAL_PROBER" -e "$GATEWAY" -e "$INTERNAL" "$work_dir/public.json"; }
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

# The gateway's discovery and dispatched attempts of $ORCH_IP, per kind as
# "kind samples successes failures", from the raw rows, Bridge's public route
# and the GraphQL performance series over one window. The window ends a
# minute back so a row still in flight cannot land in one count only; the
# series' inclusive end is the last millisecond before it.
SERIES_END=0
raw_history() {
  ch "SELECT kind, count(), countIf(success=1 AND qualifying=1), countIf(success=0 AND qualifying=1)
    FROM periscope.network_observations FINAL WHERE $NS AND kind IN ('attempt','discovery') AND resource_id='$GW_ORCH'
    AND resolved_ip='$ORCH_IP' AND gateway_region='$GATEWAY_REGION'
    AND timestamp >= toDateTime($day_ago) AND timestamp < toDateTime($SERIES_END)
    GROUP BY kind ORDER BY kind FORMAT TSV" | tr '\t' ' '
}
rest_history() {
  curl -s -m 30 "$BRIDGE_URL/v1/network/livepeer?start=$(date -u -d "@$day_ago" +%Y-%m-%dT%H:%M:%SZ)&end=$(date -u -d "@$SERIES_END" +%Y-%m-%dT%H:%M:%SZ)&resource=$GW_ORCH&ip=$ORCH_IP&region=$GATEWAY_REGION" |
    jq -r --arg o "$GW_ORCH" --arg ip "$ORCH_IP" --arg r "$GATEWAY_REGION" '
      [.history.summaries[] | select((.kind == "attempt" or .kind == "discovery") and .resource_id == $o and .ip == $ip and .region == $r)]
      | group_by(.kind)[] | "\(.[0].kind) \(map(.samples) | add) \(map(.successes) | add) \(map(.failures) | add)"'
}
series_history() {
  SERIES=$(public_gql 'query($o:String!, $s:Time!, $e:Time!){orchestratorPerformanceSeries(orchAddr:$o, timeRange:{start:$s, end:$e}, interval:"5m"){timestamp gatewayId gatewayRegion resolvedIp attempts successes failures transcodeAttempts transcodeSuccesses transcodeFailures}}' \
    "$(jq -cn --arg o "$GW_ORCH" --arg s "$(date -u -d "@$day_ago" +%Y-%m-%dT%H:%M:%SZ)" --arg e "$(date -u -d "@$((SERIES_END - 1))" +%Y-%m-%dT%H:%M:%S).999Z" '{o:$o, s:$s, e:$e}')")
  echo "$SERIES" | jq -r --arg ip "$ORCH_IP" --arg r "$GATEWAY_REGION" '
    [.data.orchestratorPerformanceSeries[] | select(.resolvedIp == $ip and .gatewayRegion == $r)]
    | if (map(.transcodeAttempts) | add // 0) > 0 then "attempt \(map(.transcodeAttempts) | add) \(map(.transcodeSuccesses) | add) \(map(.transcodeFailures) | add)" else empty end,
      if (map(.attempts) | add // 0) > 0 then "discovery \(map(.attempts) | add) \(map(.successes) | add) \(map(.failures) | add)" else empty end'
}
history_parity() {
  SERIES_END=$(($(date -u +%s) - 60))
  RAW_HISTORY=$(raw_history) REST_HISTORY=$(rest_history) SERIES_HISTORY=$(series_history)
  [ -n "$RAW_HISTORY" ] && [ "$(echo "$RAW_HISTORY" | wc -l)" = 2 ] &&
    [ "$RAW_HISTORY" = "$REST_HISTORY" ] && [ "$RAW_HISTORY" = "$SERIES_HISTORY" ]
}
if ! eventually 90 "GraphQL orchestratorPerformanceSeries counts the gateway's $GATEWAY_REGION discovery and attempts of $ORCH_IP as the raw rows and the public route do" history_parity; then
  echo "    raw:     $(echo "$RAW_HISTORY" | paste -sd';')"
  echo "    public:  $(echo "$REST_HISTORY" | paste -sd';')"
  echo "    graphql: $(echo "$SERIES_HISTORY" | paste -sd';') $(echo "$SERIES" | jq -c '.errors // empty')"
fi

log "rollups"
# Snapshots must equal the deduplicated raw rows of every bucket this run
# touched, for the chain orchestrators and the gateway's orchestrator.
RUN_BUCKET_5M=$((RUN_START / 300 * 300))
RUN_BUCKET_1H=$((RUN_START / 3600 * 3600))
SCOPE="$NS AND resource_id IN ('$ORCH_A','$ORCH_B','$ORCH_C','$GW_ORCH')"
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
all_a=$(raw_count discovery "$ORCH_A" "$IP_A" "$REGION" 0)
check "the 31-day window is served from snapshots with $all_a sample(s) for $IP_A" json_has "$R" \
  ".history.rolled_up == true and any(.history.summaries[]; .kind == \"discovery\" and .ip == \"$IP_A\" and .region == \"$REGION\" and .samples == $all_a and .p50_ms == 42)"
all_segments=$(ch "SELECT count() FROM periscope.network_observations FINAL WHERE $NS AND kind='segment' AND resolved_ip='$ORCH_IP'
  AND gateway_region='$GATEWAY_REGION' AND success=1 AND accounting_version=1")
check "the 31-day window's work for $ORCH_IP comes from the work snapshots ($all_segments accepted segment(s))" json_has "$R" \
  ".history.work.interval_seconds == 3600
    and ([.history.work.summaries[] | select(.ip == \"$ORCH_IP\" and .region == \"$GATEWAY_REGION\") | .accepted_segments] | add) == $all_segments"

log "logs"
errors=$(log_json "$SINCE" 'select(.level == "error" or .level == "fatal" or .level == "panic") | "\(.msg) \(.error // "")"' \
  network-observer network-prober decklog periscope-ingest periscope-query bridge | sort | uniq -c)
if [ -z "$errors" ]; then
  pass "no errors from the observer, the prober, Decklog, Periscope or Bridge"
else
  fail "errors from the observer, the prober, Decklog, Periscope or Bridge"
  echo "$errors" | head -20 | sed 's/^/    /'
fi
undelivered=$(logs_since "$SINCE" "$STACK_LIVEPEER_GATEWAY_A" | grep -E 'frameworks telemetry (delivery pending|spool enqueue failed|: dropping)' | head -5)
check "the gateway delivered every telemetry record without a retry or drop" [ -z "$undelivered" ]
[ -n "$undelivered" ] && echo "$undelivered" | sed 's/^/    /'

finish
