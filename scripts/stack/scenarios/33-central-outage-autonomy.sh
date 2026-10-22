#!/usr/bin/env bash
# stack-scenario: manual
# staging: unsafe, stops Commodore and Quartermaster
# Both Commodore and both Quartermaster replicas are stopped while one stream
# publishes into each cell. The cells keep deciding on their signed local
# state:
#   - the running publishers keep ingesting (their segments advance);
#   - cell A's stream, dropped and republished, is refused on cell B's edge
#     (not its signed outage owner) and admitted again on cell A's edge (its
#     signed outage owner, the cluster it last ingested on);
#   - new viewers of both streams are redirected (307) through every Foghorn
#     replica of both cells and receive TS segments.
# Then the services start again: every current authority delivery is
# acknowledged and the media-authority doctor's problem counts are zero.
# Catches: ingest refused while Commodore is down (a live ownership read per
# admission with no signed fallback), viewers or ingest refused while
# Quartermaster is down (a per-request membership read), an outage reconnect
# admitted outside the signed owner cluster.
# Manual: the outage interrupts every other scenario.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl psql python3 || finish
CENTRAL=(commodore commodore-2 quartermaster quartermaster-2)
STOPPED=0 PUBS=() SIDS=()
cleanup() {
  local pid id
  for pid in "${PUBS[@]}"; do kill "$pid" 2>/dev/null; done
  [ "$STOPPED" = 1 ] && stack_ctl start "${CENTRAL[@]}"
  for id in "${SIDS[@]}"; do delete_stream "$id"; done
}
trap cleanup EXIT

streams_listed() { json_has "$(gql '{streamsConnection(page:{first:1}){totalCount}}')" '.data.streamsConnection.totalCount >= 0'; }
# Node inventory needs infrastructure:read, which the demo user's session holds.
nodes_listed() { json_has "$(gql_session '{nodesConnection(page:{first:5}){nodes{nodeName}}}')" '(.errors == null) and (.data.nodesConnection.nodes | length) >= 1'; }
running() { stack_running "$1"; }
stopped() { ! stack_running "$1"; }

declare -A PB KEY UUID IN
for cell in a b; do
  S=$(create_stream "stack-outage-$cell-$(date +%s)" false)
  SIDS+=("$(echo "$S" | jq -r '.id // empty')")
  PB[$cell]=$(echo "$S" | jq -r '.playbackId // empty')
  KEY[$cell]=$(echo "$S" | jq -r '.streamKey // empty')
  UUID[$cell]=$(echo "$S" | jq -r '.streamId // empty')
  [ -n "${PB[$cell]}" ] || { fail "createStream for cell $cell: $S"; finish; }
  IN[$cell]=$(internal_name_of "${UUID[$cell]}")
done

declare -A PUB RTMP=([a]=$EDGE_A_RTMP [b]=$EDGE_B_RTMP)
declare -A BASE=([a]="${FOGHORN_A_URLS%% *}" [b]="${FOGHORN_B_URLS%% *}")
for cell in a b; do
  PUB[$cell]=$(publish_until_admitted "${RTMP[$cell]}" "${KEY[$cell]}" 640x360 15 1200) ||
    { fail "cell $cell publisher admitted before the outage"; finish; }
  PUBS+=("${PUB[$cell]}")
done
for cell in a b; do
  eventually 90 "cell $cell stream is served before the outage" media_at "${BASE[$cell]}" "${PB[$cell]}" || finish
done

# The signed outage owner of cell A's stream is the cluster it ingests on.
OWNER=""
signed_owner() {
  OWNER=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(origin_cluster_id, '') FROM foghorn.media_object_authority_projection
    WHERE internal_name='${IN[a]}' AND lifecycle='active'")
  [ "$OWNER" = "$CELL_A_CLUSTER" ]
}
eventually 60 "cell A holds a signed origin on $CELL_A_CLUSTER for its stream" signed_owner || finish

# Each stream's edge playlist, read during the outage without Foghorn.
declare -A EDGE_URL
for cell in a b; do EDGE_URL[$cell]=$(location_at "${BASE[$cell]}" "${PB[$cell]}"); done

log "stop ${CENTRAL[*]}"
stack_ctl stop "${CENTRAL[@]}" || finish
STOPPED=1 OUTAGE_AT=$(date +%s)
for svc in "${CENTRAL[@]}"; do eventually 30 "$svc is stopped" stopped "$svc"; done

advanced() { [ -n "$1" ] && [ -n "$2" ] && [ "$2" -gt "$1" ]; }
for cell in a b; do
  first=$(hls_position "${EDGE_URL[$cell]}")
  sleep 20
  later=$(hls_position "${EDGE_URL[$cell]}")
  check "cell $cell publisher keeps ingesting through the outage (segment ${first:-?} -> ${later:-?})" advanced "$first" "$later"
done

viewers_everywhere() {
  local cell base
  for cell in a b; do
    for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
      if [ "$(play_status "$base" "${PB[$cell]}")" = 307 ] && media_at "$base" "${PB[$cell]}"; then
        pass "new viewer of cell $cell's stream via $base gets 307 and segments during the outage"
      else
        fail "new viewer of cell $cell's stream via $base during the outage (status $(play_status "$base" "${PB[$cell]}"))"
      fi
    done
  done
}
viewers_everywhere

log "cell A's publisher drops and reconnects during the outage"
kill "${PUB[a]}" 2>/dev/null
ended() { [ -z "$(open_generation "$FOGHORN_A_DB" "${IN[a]}")" ]; }
eventually 60 "cell A's ingest session ends after its publisher stops" ended

# refusals <since> <services...>: why the cells' Foghorns refused PUSH_REWRITE.
refusals() {
  local since=$1
  shift
  logs_since "$since" "$@" | jq -Rr 'fromjson? | select(.level == "warning" or .level == "error") |
    select(.msg | test("PUSH_REWRITE|push|outage ingest|stream key|ingest session"; "i")) |
    "    refusal: \(.msg) \(.error // .outage_owner_cluster_id // "")"' | cut -c1-220 | sort | uniq -c
}
# The reconnecting encoder retries every few seconds for a minute, as OBS does.
SINCE=$(utc_now) SINCE_S=$(($(date +%s) - 1))
if WRONG=$(publish_until_admitted "$EDGE_B_RTMP" "${KEY[a]}" 640x360 15 60 60); then
  PUBS+=("$WRONG")
  fail "a reconnect into cell B (not the signed owner $OWNER) is refused during the outage: admitted"
else
  pass "a reconnect into cell B (not the signed owner $OWNER) is refused for a minute of retries during the outage"
fi
refusals "$SINCE" foghorn-b foghorn-b-2
check "no ingest session for cell A's stream opened in cell B" [ -z "$(admitted_generation "$FOGHORN_B_DB" "${IN[a]}" "$SINCE_S")" ]

# One publisher connection, as an encoder reconnecting once: admission decides
# on signed local state inside its budget instead of waiting on Commodore, so
# the first connection is admitted.
SINCE=$(utc_now) ATTEMPT_AT=$(date +%s)
if PUB[a]=$(publish_until_admitted "$EDGE_A_RTMP" "${KEY[a]}" 640x360 15 600 1); then
  PUBS+=("${PUB[a]}")
  pass "the first reconnect into cell A (the signed owner $OWNER) is admitted during the outage (after $(($(date +%s) - ATTEMPT_AT))s)"
  eventually 60 "cell A's republished stream is served during the outage" media_at "${BASE[a]}" "${PB[a]}"
else
  PUB[a]=""
  fail "the first reconnect into cell A (the signed owner $OWNER) is admitted during the outage"
fi
refusals "$SINCE" foghorn foghorn-2
viewers_everywhere
# Peer advertisements, ingest claims and membership confirmations all lapse
# within a few minutes; viewers must still be served past that.
remaining=$((OUTAGE_AT + 300 - $(date +%s)))
[ "$remaining" -gt 0 ] && sleep "$remaining"
log "five minutes into the outage"
viewers_everywhere

log "restore ${CENTRAL[*]}"
stack_ctl start "${CENTRAL[@]}" || finish
STOPPED=0
for svc in "${CENTRAL[@]}"; do eventually 60 "$svc is running again" running "$svc"; done
eventually 90 "GraphQL serves streams again (Commodore)" streams_listed
eventually 90 "GraphQL serves nodes again (Quartermaster)" nodes_listed

# The doctor's convergence probe (cli cluster doctor, media_authority_convergence)
# reads these from Commodore; the stack checks them directly.
undelivered() {
  local n
  n=$(pg commodore "SELECT count(*) FROM commodore.media_authority_deliveries AS d
    JOIN commodore.media_authority_current AS c
      ON c.authority_kind = d.authority_kind AND c.authority_id = d.authority_id AND c.authority_version = d.authority_version
    WHERE d.status IN ('pending', 'delivering')")
  echo "    current deliveries not acknowledged: ${n:-?}"
  [ "${n:-1}" -eq 0 ]
}
eventually 180 "every current authority delivery is acknowledged after the restore" undelivered
doctor_problems() {
  pg commodore "SELECT 'parked=' || count(*) FROM commodore.media_authority_refresh_obligations WHERE status = 'parked';
    SELECT 'delivery_rejected=' || count(*) FROM commodore.media_authority_actionable_rejections;
    SELECT 'expired_warm=' || count(*) FROM commodore.media_authority_refresh_obligations
      WHERE lane IN ('object_deadline', 'tenant_deadline') AND status IN ('pending', 'processing', 'parked') AND expires_at < NOW();
    SELECT 'refresh_overdue=' || count(*) FROM commodore.media_authority_refresh_obligations
      WHERE status IN ('pending', 'processing') AND next_attempt_at <= NOW() - INTERVAL '300 seconds' AND lane <> 'bulk'"
}
problems=$(doctor_problems)
# shellcheck disable=SC2086 # one word per count
printf '    doctor: %s\n' $problems
check "the media-authority doctor reports no problem after the restore" \
  bash -c '[ -n "$1" ] && ! printf "%s\n" "$1" | grep -qv "=0$"' _ "$problems"
for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
  ready() { [ "$(curl -s -m 5 -o /dev/null -w '%{http_code}' "$1/ready")" = 200 ]; }
  eventually 60 "$base is ready after the restore" ready "$base"
done
if [ -z "${PUB[a]}" ] || ! kill -0 "${PUB[a]}" 2>/dev/null; then
  PUB[a]=$(publish_until_admitted "$EDGE_A_RTMP" "${KEY[a]}" 640x360 15 300 90) && PUBS+=("${PUB[a]}")
fi
eventually 60 "cell A's stream is served after the restore" media_at "${BASE[a]}" "${PB[a]}"
check "cell B's stream is still served after the restore" media_at "${BASE[b]}" "${PB[b]}"
finish
