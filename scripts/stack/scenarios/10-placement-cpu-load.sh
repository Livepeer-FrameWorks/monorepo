#!/usr/bin/env bash
# stack-scenario: manual
# Placement under real edge CPU load: with the edge at ~45 % whole-system CPU,
# 100 sequential viewer resolves through every replica still succeed.
# Catches: N1 (Helmsman read Mist's per-mille CPU as percent, so a 4-core edge
# at 45 % reported 100 % and placement refused it as capacity_exhausted).
. "$(dirname "$0")/../lib.sh"

EDGE_SERVICE=${STACK_EDGE_A_SERVICE:-edge}
LOAD_PERCENT=${STACK_CPU_LOAD_PERCENT:-45}
need ffmpeg jq curl || finish

S=$(create_stream "stack-cpu-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
PUB=$(publish "$EDGE_A_RTMP" "$(echo "$S" | jq -r '.streamKey')" 640x360 15 300)
trap 'kill "$PUB" 2>/dev/null; stack_exec "$EDGE_SERVICE" pkill -f "stack-cpu-burn" >/dev/null 2>&1; delete_stream "$SID"' EXIT
eventually 90 "stream live" media_at "${FOGHORN_A_URLS%% *}" "$PB"

log "load the edge to ~${LOAD_PERCENT}% of its cores for 150 s"
stack_exec "$EDGE_SERVICE" sh -c "n=\$(nproc); k=\$(( (n * $LOAD_PERCENT + 99) / 100 )); for i in \$(seq \$k); do (exec -a stack-cpu-burn timeout 150 sh -c 'while :; do :; done') & done" >/dev/null 2>&1 &
sleep 20

log "100 sequential resolves per replica under load (no retries)"
for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
  refused=0
  for _ in $(seq 1 100); do
    code=$(play_status "$base" "$PB")
    [ "$code" = 307 ] || [ "$code" = 302 ] || refused=$((refused + 1))
  done
  check "0 of 100 refused via $base under load ($refused refused)" [ "$refused" = 0 ]
done
finish
