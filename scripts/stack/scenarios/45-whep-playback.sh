#!/usr/bin/env bash
# stack-scenario: default
# staging: safe
# A live stream plays over WebRTC with WHEP signalling from every edge. Each
# cell's Foghorn redirects /play/<id>/whep to its edge's Mist WHEP endpoint; a
# pion client (scripts/stack/whep) POSTs its offer there, connects ICE and
# DTLS through Mist's shared WebRTC UDP port, counts the RTP that arrives and
# DELETEs the session.
# Measures: per edge, the WHEP redirect, video RTP packets received within 30 s,
# the DELETE status, and the edge's wildcard sockets outside its public set
# right after the session (sweep.py edge-listeners; the doctor on staging).
# Catches: WHEP placement or signalling regressions, and a socket the WebRTC
# listener's STUN relay leaves on 0.0.0.0:<ephemeral> once STUN has passed
# through it (staging doctor on edge-eu, MistServer before a16851c86).
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl go python3 || finish

WHEP_BIN="$STACK_STATE_DIR/whep"
# The checkout is mounted read-only and has no .git in stack-runner; the binary
# goes to the state directory and VCS stamping is off.
if ! (cd "$STACK_LIB_DIR/whep" && go build -buildvcs=false -o "$WHEP_BIN" .) >"$STACK_STATE_DIR/whep-build.log" 2>&1; then
  blocked "the WHEP client did not build: $(tail -3 "$STACK_STATE_DIR/whep-build.log" | tr '\n' ' ')"
  finish
fi

S=$(create_stream "stack-whep-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$(echo "$S" | jq -r '.streamKey')" 640x360 15 300) ||
  { fail "publisher never admitted"; delete_stream "$SID"; finish; }
trap 'kill "$PUB" 2>/dev/null; delete_stream "$SID"' EXIT

whep_location() { # whep_location <foghorn>: the WHEP redirect for the stream, or empty
  curl -s -m 10 -o /dev/null -D - "$1/play/$PB/whep" | awk 'tolower($1)=="location:"{print $2}' | tr -d '\r'
}
whep_offered() { case "$(whep_location "$1")" in http*://*/whep/*) return 0 ;; *) return 1 ;; esac; }

EDGES_PLAYED=()
for base in "${FOGHORN_A_URLS%% *}" "${FOGHORN_B_URLS%% *}"; do
  eventually 90 "stream live and resolvable at $base" media_at "$base" "$PB" || continue
  eventually 60 "$base redirects /play/$PB/whep to a Mist WHEP endpoint" whep_offered "$base" || continue
  LOC=$(whep_location "$base")
  EDGE=$(printf '%s' "$LOC" | sed -E 's#^[a-z]+://([^/:]+).*#\1#')
  log "WHEP playback from $EDGE (${LOC%%\?*})"
  OUT=$("$WHEP_BIN" "$LOC" 50 30 2>"$STACK_STATE_DIR/whep-$EDGE.log")
  RC=$?
  echo "    $OUT"
  [ "$RC" = 0 ] || sed 's/^/    whep: /' "$STACK_STATE_DIR/whep-$EDGE.log" | tail -5
  video=$(jq -r '.video_packets // 0' <<<"$OUT" 2>/dev/null)
  deleted=$(jq -r '.delete_status // 0' <<<"$OUT" 2>/dev/null)
  check "video RTP arrives over WHEP from $EDGE (${video:-0} packets)" [ "${video:-0}" -ge 50 ]
  check "the WHEP session on $EDGE is deleted (HTTP ${deleted:-0})" test "${deleted:-0}" -ge 200 -a "${deleted:-0}" -le 299
  # Read right after the session: a connector restart replaces the listener
  # and its relay socket, so the end-of-run sweep alone can miss it.
  if [ "${STACK_TARGET:-stack}" = staging ]; then
    echo "    staging: the doctor's edge Listeners check judges $EDGE's sockets"
  else
    case "$EDGE" in
      *edge-b*) SERVICE=$STACK_EDGE_B_SERVICE ;;
      *) SERVICE=$STACK_EDGE_A_SERVICE ;;
    esac
    if CID=$(container_of "$SERVICE") && [ -n "$CID" ]; then
      SOCKETS=$(python3 "$STACK_LIB_DIR/sweep.py" edge-listeners "$CID")
      [ -z "$SOCKETS" ] || printf '%s\n' "$SOCKETS" | sed 's/^/    /'
      check "no socket on $EDGE listens on every interface outside its public set after the WHEP session" test -z "$SOCKETS"
    else
      blocked "reading $SERVICE's sockets needs docker access to the slot in stack-runner"
    fi
  fi
  EDGES_PLAYED+=("$EDGE")
done
distinct=$(printf '%s\n' "${EDGES_PLAYED[@]}" | sort -u | sed '/^$/d' | wc -l | tr -d ' ')
check "WHEP was played from both cells' edges (${EDGES_PLAYED[*]})" [ "$distinct" -ge 2 ]
finish
