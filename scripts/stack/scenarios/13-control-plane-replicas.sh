#!/usr/bin/env bash
# stack-scenario: default
# Commodore and Quartermaster each run two replicas behind one name
# (docker-compose.stack.yml: commodore-2, quartermaster-2), as production runs
# them on separate hosts. With one replica of either service killed, the API
# keeps answering, a new stream can be created, and a new publisher is admitted
# and served. The killed replica is started again before the next service.
# Catches: rc7 (stopping the one Commodore broke Bridge GraphQL and every new
# publish), clients pinned to one replica (pick_first) or to a draining one.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl getent || finish

replica_count() { getent ahostsv4 "$1" | awk '{print $1}' | sort -u | wc -l | tr -d ' '; }
two_replicas() { [ "$(replica_count "$1")" -ge 2 ]; }
running() { # running <service>: the compose service has a running container
  [ "$(docker compose -p "$COMPOSE_PROJECT_NAME" ps --status running -q "$1" 2>/dev/null | wc -l | tr -d ' ')" -ge 1 ]
}
stopped() { ! running "$1"; }
serving_again() { running "$1" && two_replicas "$1"; }
streams_listed() { json_has "$(gql '{streamsConnection(page:{first:1}){totalCount}}')" '.data.streamsConnection.totalCount >= 0'; }
nodes_listed() { json_has "$(gql '{nodesConnection(page:{first:5}){nodes{nodeName}}}')" '(.errors == null) and (.data.nodesConnection.nodes | length) >= 1'; }

# with_replica_down <service> <second replica>: kill the first replica, prove the
# API, stream creation and a new publish work through the second, restart it.
with_replica_down() {
  local svc=$1 other=$2 S SID PB KEY PUB
  log "$svc: one of two replicas killed"
  if ! running "$other"; then
    blocked "$other is not running; the stack has a single $svc replica"
    return
  fi
  eventually 30 "$svc resolves to both replicas" two_replicas "$svc"
  stack_ctl kill "$svc" || return
  eventually 20 "$svc replica 1 is down" stopped "$svc"
  # The killed container leaves DNS at once and its connections reset, so the
  # first calls after the kill already reach the surviving replica.
  sleep 3

  check "GraphQL reads streams with $svc replica 1 down" streams_listed
  check "GraphQL reads nodes with $svc replica 1 down" nodes_listed

  S=$(create_stream "stack-cp-$svc-$(date +%s)" false)
  SID=$(echo "$S" | jq -r '.id // empty')
  PB=$(echo "$S" | jq -r '.playbackId // empty')
  KEY=$(echo "$S" | jq -r '.streamKey // empty')
  if [ -n "$SID" ]; then
    pass "createStream with $svc replica 1 down"
    PUB=$(publish "$EDGE_A_RTMP" "$KEY" 640x360 15 90)
    eventually 90 "a new publish is admitted and served with $svc replica 1 down" media_at "${FOGHORN_A_URLS%% *}" "$PB"
    kill "$PUB" 2>/dev/null
    delete_stream "$SID"
  else
    fail "createStream with $svc replica 1 down: $S"
  fi

  stack_ctl start "$svc" || return
  eventually 90 "$svc replica 1 serves again" serving_again "$svc"
}

with_replica_down commodore commodore-2
with_replica_down quartermaster quartermaster-2
finish
