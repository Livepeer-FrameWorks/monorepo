#!/usr/bin/env bash
# stack-scenario: default
# A Foghorn replica of cell A is stopped and started again, once for each
# replica, while one stream publishes into each cell. Every other replica of
# both cells keeps resolving both streams on the first attempt: the stopped
# replica may hold the cell's PeerManager lease or the peer cell's inbound
# PeerChannel, and neither may take the peer cell's source presence with it.
# Once the replica serves again it resolves both streams, and its return does
# not make any replica lose the local stream's source.
# Catches: D4 (peer ads pinned to one replica address: the peer's stream 503
# then 404 on the surviving replicas; a returning replica's startup evicting the
# local source for seconds), D10 (a restarted replica answering STREAM_STARTING
# for a live stream).
. "$(dirname "$0")/../lib.sh"

DOWN_SECONDS=${STACK_REPLICA_DOWN_SECONDS:-60}
UP_SECONDS=${STACK_REPLICA_UP_SECONDS:-40}
REPLICAS_A=(foghorn foghorn-2)
need ffmpeg jq curl || finish

SA=$(create_stream "stack-replica-restart-a-$(date +%s)" false)
SB=$(create_stream "stack-replica-restart-b-$(date +%s)" false)
SIDA=$(echo "$SA" | jq -r '.id // empty')
SIDB=$(echo "$SB" | jq -r '.id // empty')
[ -n "$SIDA" ] && [ -n "$SIDB" ] || { fail "createStream: $SA $SB"; finish; }
PBA=$(echo "$SA" | jq -r '.playbackId')
PBB=$(echo "$SB" | jq -r '.playbackId')
SECONDS_TOTAL=$(((DOWN_SECONDS + UP_SECONDS + 180) * ${#REPLICAS_A[@]} + 300))
PUBA=$(publish_until_admitted "$EDGE_A_RTMP" "$(echo "$SA" | jq -r '.streamKey')" 640x360 15 "$SECONDS_TOTAL") ||
  { fail "cell A publisher never admitted"; finish; }
PUBB=$(publish_until_admitted "$EDGE_B_RTMP" "$(echo "$SB" | jq -r '.streamKey')" 640x360 15 "$SECONDS_TOTAL") ||
  { kill "$PUBA"; fail "cell B publisher never admitted"; finish; }
STOPPED=""
trap 'kill "$PUBA" "$PUBB" 2>/dev/null; [ -n "$STOPPED" ] && stack_ctl start "$STOPPED"; delete_stream "$SIDA"; delete_stream "$SIDB"' EXIT
for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
  eventually 120 "cell A stream plays via $base" media_at "$base" "$PBA"
  eventually 120 "cell B stream plays via $base" media_at "$base" "$PBB"
done

base_of() { # base_of <service>: the service's public /play base URL
  local b
  if [ "${STACK_TARGET:-stack}" = staging ]; then
    local -a bases
    read -r -a bases <<<"$FOGHORN_A_URLS"
    case "$1" in
      foghorn) printf '%s\n' "${bases[0]:-}"; return ;;
      foghorn-2) printf '%s\n' "${bases[1]:-}"; return ;;
    esac
  fi
  for b in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
    case "$b" in "http://$1:"*) echo "$b"; return ;; esac
  done
}

# poll <seconds> <label> <base...>: resolves both streams through each base
# about twice a second; prints every answer that is not a redirect.
poll() {
  local seconds=$1 label=$2 deadline code pb name base body bad=0 total=0
  shift 2
  deadline=$(($(date +%s) + seconds))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    for base in "$@"; do
      for pb in "$PBA" "$PBB"; do
        name=A
        [ "$pb" = "$PBB" ] && name=B
        code=$(play_status "$base" "$pb")
        total=$((total + 1))
        case "$code" in
          307 | 302) ;;
          *)
            bad=$((bad + 1))
            body=$(curl -s -m 5 "$base/play/$pb/hls" | head -c 160)
            printf '  %s %s stream %s via %s -> %s %s\n' "$(ts)" "$label" "$name" "$base" "$code" "$body"
            ;;
        esac
      done
    done
    sleep 0.5
  done
  POLL_BAD=$bad POLL_TOTAL=$total
}

for target in "${REPLICAS_A[@]}"; do
  others=()
  for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
    [ "$base" = "$(base_of "$target")" ] || others+=("$base")
  done
  log "stop $target; the other replicas resolve both streams for ${DOWN_SECONDS}s"
  SINCE=$(utc_now)
  stack_ctl stop "$target" || { fail "stop $target"; finish; }
  STOPPED=$target
  poll "$DOWN_SECONDS" "while $target is down:" "${others[@]}"
  check "with $target down, $POLL_BAD of $POLL_TOTAL resolves through the other replicas refused" [ "$POLL_BAD" = 0 ]

  log "start $target"
  stack_ctl start "$target" || { fail "start $target"; finish; }
  STOPPED=""
  up() { [ "$(play_status "$(base_of "$target")" "$PBA")" != 000 ]; }
  eventually 120 "$target answers /play again" up
  poll "$UP_SECONDS" "after $target returned:" "$(base_of "$target")" "${others[@]}"
  check "after $target returned, $POLL_BAD of $POLL_TOTAL resolves through every replica refused" [ "$POLL_BAD" = 0 ]
  logs_since "$SINCE" foghorn foghorn-2 foghorn-b foghorn-b-2 |
    grep -E 'PeerManager leadership|PeerChannel (connected|disconnected|established|closed)|no permitted destination|STREAM_STARTING|source is (starting|offline)|Failed to resolve viewer endpoint' |
    cut -c1-400 | head -40
done
finish
