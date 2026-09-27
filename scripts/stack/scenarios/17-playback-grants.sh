#!/usr/bin/env bash
# stack-scenario: default
# One viewer's HLS session reaches Foghorn once, not once per request. Mist
# fires PLAY_REWRITE for the master playlist, every variant playlist and every
# segment; Foghorn admits the session's first request and sends the serving
# edge the stream's playback grant, and Helmsman answers the session's later
# requests (which carry Mist's session token) itself.
# Measures: PLAY_REWRITE forwarded to Foghorn for 20 HLS requests of one
# viewer, counted as "PLAY_REWRITE resolved by Foghorn" lines in the serving
# edge's Helmsman log (the serving edge is the host of the /play Location).
# Catches: per-request Foghorn round trips on the media path.
. "$(dirname "$0")/../lib.sh"

REQUESTS=${STACK_GRANT_REQUESTS:-20}
need ffmpeg jq curl python3 docker || finish

START=$(utc_now)
S=$(create_stream "stack-grants-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
IN=$(internal_name_of "$UUID")
PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$(echo "$S" | jq -r '.streamKey')" 640x360 15 300) ||
  { fail "publisher never admitted"; delete_stream "$SID"; finish; }
trap 'kill "$PUB" 2>/dev/null; delete_stream "$SID"' EXIT
eventually 90 "stream live" media_at "$FOGHORN_A_URL" "$PB"

log "one viewer: master playlist, then ${REQUESTS} requests in its session"
LOC=$(location_at "$FOGHORN_A_URL" "$PB")
[ -n "$LOC" ] || { fail "no /play redirect for $PB"; finish; }
HOST=$(printf '%s' "$LOC" | sed -E 's#^[a-z]+://([^/:]+).*#\1#')
case "$HOST" in
  *edge-b*) SERVING=$STACK_EDGE_B_SERVICE ;;
  *) SERVING=$STACK_EDGE_A_SERVICE ;;
esac
echo "    serving edge $SERVING ($LOC)"
SINCE=$(utc_now)
RESULT=$(python3 - "$LOC" "$REQUESTS" <<'PY'
import sys, time, urllib.request, urllib.parse
master, wanted = sys.argv[1], int(sys.argv[2])
codes = []
def get(url):
    with urllib.request.urlopen(url, timeout=10) as r:
        codes.append(r.status)
        return r.read()
def entries(body, base):
    return [urllib.parse.urljoin(base, l.strip()) for l in body.decode("utf-8", "replace").splitlines()
            if l.strip() and not l.startswith("#")]
try:
    variant = entries(get(master), master)[0]
    served = 0
    while served < wanted:
        segments = entries(get(variant), variant)
        served += 1
        for segment in segments[-2:]:
            if served >= wanted:
                break
            get(segment)
            served += 1
        time.sleep(1)
    print(f"{len(codes)} {sum(1 for c in codes if c == 200)} {variant}")
except Exception as e:
    print(f"{len(codes)} {sum(1 for c in codes if c == 200)} error:{e}")
PY
)
read -r TOTAL OK SESSION <<<"$RESULT"
echo "    requests $TOTAL, 200 OK $OK, session ${SESSION%%\?*}?..."
all_served() { [ "$TOTAL" -gt "$REQUESTS" ] && [ "$OK" = "$TOTAL" ]; }
check "every request of the session was served ($OK of $TOTAL)" all_served
has_token() { case "$SESSION" in *tkn=*) return 0 ;; *) return 1 ;; esac; }
check "the session's requests carry Mist's session token" has_token

sleep 2
EDGE_LOG=$(logs_since "$SINCE" "$SERVING")
FORWARDED=$(printf '%s\n' "$EDGE_LOG" | grep 'PLAY_REWRITE resolved by Foghorn' | grep -c "$IN")
echo "    PLAY_REWRITE forwarded to Foghorn: $FORWARDED for $TOTAL requests"
at_most_twice() { [ "$FORWARDED" -ge 1 ] && [ "$FORWARDED" -le 2 ]; }
check "one viewer's $TOTAL HLS requests reached Foghorn at most twice (got $FORWARDED)" at_most_twice
grant_applied() { logs_since "$START" "$SERVING" | grep 'Playback grant applied' | grep -q "$IN"; }
check "the serving edge holds the stream's playback grant" grant_applied
finish
