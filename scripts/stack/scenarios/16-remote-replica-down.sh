#!/usr/bin/env bash
# stack-scenario: default
# A remote cell with one Foghorn replica dead still answers placement through
# its other replica. The stream publishes into cell A and cell B's own edge is
# stopped, so every viewer resolved through cell B must be placed on cell A's
# edge by a placement RPC into cell A. The cell-A replica killed is the first
# address in Quartermaster's stable order (the one the pre-B1 code used
# exclusively); it is killed, not stopped, so it does not deregister and the
# peer census keeps listing it until Quartermaster's health poll notices.
# Resolves start at once. Every one must succeed on cell A's edge, and cell B
# logs the preparation failing over from the dead replica.
# Catches: B1 (placement RPCs pinned to one remote replica: a dead first replica
# refused every cross-cell viewer until Quartermaster dropped it).
. "$(dirname "$0")/../lib.sh"

ROUNDS=${STACK_REPLICA_RESOLVES:-30}
FOGHORNS_B=(foghorn-b foghorn-b-2)
EDGE_B=(edge-b edge-proxy-b)
need ffmpeg jq curl docker || finish

S=$(create_stream "stack-remote-replica-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$(echo "$S" | jq -r '.streamKey')" 640x360 15 $((ROUNDS * 4 + 400))) ||
  { fail "publisher never admitted"; delete_stream "$SID"; finish; }
KILLED="" EDGE_B_DOWN=""
restore() {
  local svc
  [ -n "$KILLED" ] && stack_ctl start "$KILLED"
  if [ -n "$EDGE_B_DOWN" ]; then for svc in "${EDGE_B[@]}"; do stack_ctl start "$svc"; done; fi
  KILLED="" EDGE_B_DOWN=""
}
trap 'kill "$PUB" 2>/dev/null; restore; delete_stream "$SID"' EXIT
eventually 90 "stream live in cell A" media_at "$FOGHORN_A_URL" "$PB"

log "cell B's edge stopped: cell B must place its viewers in cell A"
EDGE_B_DOWN=1
for svc in "${EDGE_B[@]}"; do stack_ctl stop "$svc" || { fail "stop $svc"; finish; }; done
on_cell_a_edge() { # on_cell_a_edge <foghorn> <playback id>: resolved onto cell A's edge
  case "$(location_at "$1" "$2")" in "http://$EDGE_A_HOST:"*) return 0 ;; *) return 1 ;; esac
}
for base in $FOGHORN_B_URLS; do
  eventually 120 "cell B places the viewer on cell A's edge before the fault via $base" on_cell_a_edge "$base" "$PB"
done

# Cell A's Foghorn gRPC replicas as ListPeerClusters orders them for peers.
ADDRS=$(pg quartermaster "SELECT DISTINCT si.advertise_host || ':' || si.port, si.advertise_host, si.port
  FROM quartermaster.service_cluster_assignments sca
  JOIN quartermaster.service_instances si ON si.id = sca.service_instance_id
  JOIN quartermaster.services svc ON svc.service_id = si.service_id
  WHERE sca.cluster_id = '$FOGHORN_A_CLUSTER' AND sca.is_active AND svc.type = 'foghorn'
    AND si.status = 'running' AND si.health_status = 'healthy' AND si.protocol = 'grpc'
  ORDER BY si.advertise_host, si.port" | cut -d'|' -f1)
echo "    cell A replicas for peers: $(echo "$ADDRS" | tr '\n' ' ')"
[ "$(echo "$ADDRS" | grep -c .)" -ge 2 ] || { blocked "cell A lists fewer than two healthy Foghorn replicas"; finish; }
FIRST=$(echo "$ADDRS" | head -1)
TARGET=${FIRST%:*}
[ -n "$(container_of "$TARGET")" ] || { blocked "first replica $FIRST is not a service of this stack"; finish; }

log "kill $TARGET ($FIRST), then $ROUNDS resolve rounds through cell B"
KILL_TS=$(utc_now)
stack_ctl kill "$TARGET" || { fail "kill $TARGET"; finish; }
KILLED=$TARGET
declare -A refused ok elsewhere
for round in $(seq 1 "$ROUNDS"); do
  for base in $FOGHORN_B_URLS; do
    loc=$(location_at "$base" "$PB")
    case "$loc" in
      "http://$EDGE_A_HOST:"*) ok[$base]=$((${ok[$base]:-0} + 1)) ;;
      "") refused[$base]=$((${refused[$base]:-0} + 1))
        printf '  round %d %s -> %s %s\n' "$round" "$base" "$(play_status "$base" "$PB")" "$(curl -s -m 10 "$base/play/$PB/hls" | head -c 200)" ;;
      *) elsewhere[$base]=$((${elsewhere[$base]:-0} + 1)); printf '  round %d %s -> %s\n' "$round" "$base" "$loc" ;;
    esac
  done
  sleep 1
done
STILL_LISTED=$(pg quartermaster "SELECT count(*) FROM quartermaster.service_instances si
  JOIN quartermaster.services svc ON svc.service_id = si.service_id
  WHERE svc.type = 'foghorn' AND si.protocol = 'grpc' AND si.advertise_host || ':' || si.port = '$FIRST'
    AND si.status = 'running' AND si.health_status = 'healthy'")
echo "    $FIRST still listed healthy at the end of the rounds: ${STILL_LISTED:-?}"
all_on_cell_a() { [ "${refused[$1]:-0}" = 0 ] && [ "${elsewhere[$1]:-0}" = 0 ]; }
for base in $FOGHORN_B_URLS; do
  check "0 of $ROUNDS resolves refused via $base with $TARGET dead (${refused[$base]:-0} refused, ${ok[$base]:-0} on cell A's edge, ${elsewhere[$base]:-0} elsewhere)" \
    all_on_cell_a "$base"
  check "real TS segment via $base with $TARGET dead" media_at "$base" "$PB"
done

FAILOVER=$(logs_since "$KILL_TS" "${FOGHORNS_B[@]}" | grep -E 'Remote placement preparation (failed on a|answered by another) cell replica')
printf '    failover log lines: %s\n' "$(echo "$FAILOVER" | grep -c .)"
echo "$FAILOVER" | head -4 | cut -c1-300
# Counted, not `grep -q`: under pipefail an early exit breaks the upstream pipe.
answered_elsewhere() {
  [ "$(echo "$FAILOVER" | grep 'answered by another cell replica' | grep -vc "\"peer_addr\":\"$FIRST\"")" -gt 0 ]
}
check "cell B logged a preparation answered by a replica other than $FIRST" answered_elsewhere

restore
healthy_again() { [ "$(docker inspect -f '{{.State.Health.Status}}' "$(container_of "$TARGET")" 2>/dev/null)" = healthy ]; }
eventually 120 "$TARGET healthy again" healthy_again
eventually 180 "cell B serves media again after the restore" media_at "$FOGHORN_B_URL" "$PB"
finish
