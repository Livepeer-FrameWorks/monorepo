#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, injects WAN delay into this isolated stack's Foghorn namespaces
# A single encoder invocation must admit and play with a ready signed pair
# while every Foghorn -> database/Commodore packet pays 110 ms extra latency.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl psql || finish
id="" publisher="" faults=""
cleanup() {
  [ -z "$publisher" ] || kill "$publisher" 2>/dev/null
  for service in $faults; do
    netfault "$service" tc qdisc del dev eth0 root >/dev/null 2>&1
    netfault_stop "$service"
  done
  [ -z "$id" ] || delete_stream "$id"
  return 0
}
trap cleanup EXIT

created=$(create_stream "wan53-$(date +%s)-$RANDOM" false)
id=$(echo "$created" | jq -r '.id // empty')
uuid=$(echo "$created" | jq -r '.streamId // empty')
key=$(echo "$created" | jq -r '.streamKey // empty')
pb=$(echo "$created" | jq -r '.playbackId // empty')
[ -n "$id" ] && [ -n "$uuid" ] && [ -n "$key" ] && [ -n "$pb" ] || { fail "create WAN fixture"; finish; }
internal=$(internal_name_of_key "$key")

idle_and_released() {
  [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name IN ('$internal','live+$internal') AND ended_at IS NULL")" = 0 ] &&
    [ "$(pg commodore "SELECT count(*) FROM commodore.streams WHERE tenant_id='$STACK_TENANT_ID' AND id='$uuid' AND COALESCE(active_ingest_claim_id,'')<>''")" = 0 ]
}
pair_ready() {
  [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.media_object_authority_projection object JOIN foghorn.tenant_authority_projection tenant ON tenant.tenant_id=object.tenant_id WHERE object.tenant_id='$STACK_TENANT_ID' AND object.stream_id='$uuid' AND object.local_ingest_ready AND tenant.local_ingest_ready AND object.valid_until>NOW() AND tenant.valid_until>NOW()")" = 1 ] &&
    [ "$(pg commodore "SELECT version.payload_schema_version FROM commodore.media_authority_current current JOIN commodore.media_authority_versions version USING(authority_kind,authority_id,authority_version) WHERE current.authority_kind='media_object' AND current.authority_id='live_stream:$uuid'")" = 4 ]
}
# Only setup retries: neither admission under injected latency uses this helper.
publisher=$(publish_until_admitted "$EDGE_B_RTMP" "$key" 320x180 10 600 180) || { fail "prewarm signed authority and placement"; finish; }
kill "$publisher" 2>/dev/null; publisher=""
eventually 90 "prewarm publisher ends and releases its lease" idle_and_released || finish
eventually 120 "cell B holds a ready schema-4 authority pair" pair_ready || finish

for service in foghorn-b foghorn-b-2; do
  netfault_start "$service" || finish
  faults="$faults $service"
  # This helper shares only this stack container's network namespace. Delay
  # egress to PostgreSQL and Commodore, including already-open pooled sockets.
  netfault "$service" tc qdisc add dev eth0 root handle 1: prio bands 3 || { fail "install WAN classifier"; finish; }
  netfault "$service" tc qdisc add dev eth0 parent 1:3 handle 30: netem delay 110ms || { fail "install WAN delay"; finish; }
  for port in 5432 19001; do
    netfault "$service" tc filter add dev eth0 protocol ip parent 1: prio 1 u32 match ip protocol 6 0xff match ip dport "$port" 0xffff flowid 1:3 || { fail "classify WAN dependency port $port"; finish; }
  done
done

session_count() { pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name IN ('$internal','live+$internal')"; }
confirmed() {
  generation=$(pg "$FOGHORN_B_DB" "SELECT id FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND stream_internal_name IN ('$internal','live+$internal') AND ended_at IS NULL AND projection_state='active' AND publisher_admission_version=1 AND publisher_lease_strict_owner AND source_revision IS NOT NULL")
  [ -n "$generation" ]
}
release_acknowledged() {
  [ "$(pg "$FOGHORN_B_DB" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND id='$generation' AND ended_at IS NOT NULL AND publisher_lease_released_at IS NOT NULL")" = 1 ]
}
all_replicas_play() {
  local base
  for base in $FOGHORN_B_URLS; do media_at "$base" "$pb" || return 1; done
}
admission_calls() {
  local base
  for base in $FOGHORN_B_URLS; do curl -fsS -m 10 "$base/metrics" || return 1; done |
    awk '/^foghorn_publisher_admission_duration_seconds_count\{/ && /phase="total"/ {sum+=$NF} END {printf "%.0f\n",sum}'
}
previous_revision=0
for attempt in 1 2; do
  before=$(session_count)
  calls_before=$(admission_calls) || { fail "read admission counter"; finish; }
  started=$(monotonic_ms)
  publisher=$(publish "$EDGE_B_RTMP" "$key" 320x180 10 600)
  eventually 20 "WAN publisher $attempt confirms its exact durable generation" confirmed || finish
  elapsed=$(($(monotonic_ms)-started))
  revision=$(pg "$FOGHORN_B_DB" "SELECT source_revision FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID' AND id='$generation'")
  check "WAN publisher $attempt advances its source revision" test "$revision" -gt "$previous_revision"
  previous_revision=$revision
  check "WAN publisher $attempt creates exactly one session (elapsed ${elapsed}ms)" test "$(session_count)" -eq "$((before+1))"
  eventually 45 "WAN publisher $attempt serves TS media through every B replica" all_replicas_play || finish
  check "WAN publisher $attempt succeeds in one admission call" test "$(admission_calls)" -eq "$((calls_before+1))"
  kill "$publisher" 2>/dev/null; publisher=""
  eventually 90 "WAN publisher $attempt retires and releases its lease" idle_and_released || finish
  eventually 15 "WAN publisher $attempt durably acknowledges lease release" release_acknowledged || finish
  # Cross a renewal tick: a queued old-owner renewal must not restore the lease.
  sleep 12
  check "retired WAN publisher $attempt stays released after renewal" idle_and_released
done
for service in $faults; do
  netfault "$service" tc -s qdisc show dev eth0
  check "$service actually delayed dependency packets" netfault "$service" sh -c 'tc -s qdisc show dev eth0 | awk '\''/netem/{netem=1;next} netem && /Sent/{if ($4+0>0) ok=1;netem=0} END{exit !ok}'\'''
done
finish
