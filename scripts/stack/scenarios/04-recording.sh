#!/usr/bin/env bash
# stack-scenario: default
# staging: safe
# A default record:true stream, pushed past one DVR window and stopped:
# chapters close, finalize faster than real time, freeze with an accepted
# .dtsh, keep their id across the stop, announce recording.ready with the
# chapter's playback id, and replay through every replica of both cells.
# Catches: F9 (default not replayable), R1 (two inputs -> Mist crash), R2 (DTSH
# upload truncated, chapters never freeze), R4 (finalize pinned at 1x), N16
# (recording.ready at stop without a playback id; chapter id changed at stop),
# N8 (cross-cell chapter replay refused on non-leaders), the origin edge's relay
# fetching a fresh chapter's .dtsh from itself until its fetch deadline, a
# stalled publisher's session recorded afresh after Mist's buffer ended it (a
# failed recording on every such stop).
. "$(dirname "$0")/../lib.sh"

WINDOW=${STACK_DVR_WINDOW_SECONDS:-120}
PUSH=$((WINDOW + 90))
# Finalize runs unconstrained (R4): well under real time for a WINDOW-long chapter.
FINALIZE_BOUND=${STACK_FINALIZE_BOUND_SECONDS:-$((WINDOW / 2 + 90))}
need ffmpeg jq curl python3 || finish
SINCE=$(date +%s)
LOG_SINCE=$(utc_now)
webhook_endpoint >/dev/null || fail "webhook endpoint at $RECEIVER_HOOK_URL"

S=$(create_stream "stack-recording-$(date +%s)" true)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
check "record:true without settings defaults to WINDOW_SIZED" json_has "$S" '.dvrChapterMode == "WINDOW_SIZED"'
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 854x480 15 "$PUSH")
trap 'kill "$PUB" 2>/dev/null' EXIT
# A second recording whose publisher stops 5 s before its first chapter
# boundary: the writer outlives the publisher by Mist's resume window (about
# 15 s) and crosses the boundary with no media after it. The post-run sweep
# checks its chapters (scripts/stack/sweep.py, invariant "dvr"); staging left
# such a tail as an empty chapter FAILED_SOURCE_MISSING.
TAIL_KEY=$(create_stream "stack-recording-tail-$(date +%s)" true | jq -r '.streamKey // empty')
if [ -n "$TAIL_KEY" ]; then
  TAIL_PUB=$(publish "$EDGE_A_RTMP" "$TAIL_KEY" 640x360 15 $((WINDOW - 5)))
  trap 'kill "$PUB" "$TAIL_PUB" 2>/dev/null' EXIT
else
  fail "createStream for the boundary-tail recording"
fi

dvr_id() { pg commodore "SELECT id FROM commodore.dvr_recordings WHERE tenant_id='$STACK_TENANT_ID' AND stream_id='$UUID' ORDER BY created_at DESC LIMIT 1"; }
chapters() { gql 'query($d:ID!){dvrChapters(dvrId:$d){chapters{chapterId mode state isCurrent playbackId segmentCount startMs endMs}}}' "$(jq -cn --arg d "$DVR" '{d:$d}')" | jq -c '.data.dvrChapters.chapters // []'; }

has_dvr() { DVR=$(dvr_id); [ -n "$DVR" ]; }
eventually 60 "recording started automatically" has_dvr
eventually 60 "recording.started webhook" saw_event "$SINCE" recording.started

log "open chapter while live"
open_chapter() { OPEN_ID=$(chapters | jq -r '[.[] | select(.state=="OPEN")][0].chapterId // empty'); [ -n "$OPEN_ID" ]; }
eventually 60 "an OPEN chapter exists while live" open_chapter
FIRST_ID=$OPEN_ID
echo "    first open chapter: $FIRST_ID"

log "stop after ${PUSH}s"
wait_publisher "$PUB" $((PUSH + 60)) || fail "publisher still running $((PUSH + 60))s after it started"
STOPPED=$(date +%s)
eventually 60 "recording.stopped webhook" saw_event "$SINCE" recording.stopped
# Before any chapter finalizes, recording.ready must not have fired for this
# recording. The tail recording above stops earlier and announces its own.
if chapters | jq -e 'all(.state != "FINALIZED" and .state != "FROZEN")' >/dev/null; then
  no_early_ready() { ! saw_event "$SINCE" recording.ready "tostring | contains(\"$UUID\")"; }
  check "no recording.ready while chapters are still finalizing" no_early_ready
fi

# While a chapter is FINALIZING nothing of it is stored, so /play answers the
# retryable 503 on every replica: never a 500, and never a redirect to the node
# still writing the file. A 307 counts only once the chapter has finalized.
FINALIZING_PLAY_PROBES=0
FINALIZING_PLAY_BAD=""
probe_finalizing_play() {
  local pb base code state
  for pb in $(echo "$1" | jq -r '.[] | select(.state=="FINALIZING" and (.playbackId // "") != "") | .playbackId'); do
    for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
      code=$(play_status "$base" "$pb")
      FINALIZING_PLAY_PROBES=$((FINALIZING_PLAY_PROBES + 1))
      [ "$code" = 503 ] && continue
      state=$(chapters | jq -r --arg pb "$pb" '.[] | select(.playbackId==$pb) | .state')
      if [ "$code" = 307 ] && { [ "$state" = FINALIZED ] || [ "$state" = FROZEN ]; }; then
        continue
      fi
      FINALIZING_PLAY_BAD="$FINALIZING_PLAY_BAD $base:$pb:$code:$state"
    done
  done
}
all_final() {
  local c
  c=$(chapters)
  echo "    chapters: $(echo "$c" | jq -c 'map({id:.chapterId[0:8],state,segs:.segmentCount})')"
  probe_finalizing_play "$c"
  echo "$c" | jq -e 'length > 0 and all(.state == "FINALIZED" or .state == "FROZEN")' >/dev/null
}
eventually "$FINALIZE_BOUND" "every chapter FINALIZED within ${FINALIZE_BOUND}s of the stop" all_final
echo "    finalized after $(($(date +%s) - STOPPED))s; /play probes of FINALIZING chapters: $FINALIZING_PLAY_PROBES"
check "/play of a FINALIZING chapter answers 503 on every replica (bad:${FINALIZING_PLAY_BAD:- none})" [ -z "$FINALIZING_PLAY_BAD" ]
C=$(chapters)
check "the first chapter kept its id across the stop ($FIRST_ID)" json_has "$C" "any(.chapterId == \"$FIRST_ID\")"
FIRST_PB=$(echo "$C" | jq -r --arg id "$FIRST_ID" '.[] | select(.chapterId==$id) | .playbackId // empty')
check "the finalized chapter has a playback id" [ -n "$FIRST_PB" ]

log "freeze: .dtsh accepted, chapter frozen"
frozen() {
  local row
  row=$(pg "$FOGHORN_A_DB" "SELECT c.frozen_at IS NOT NULL, COALESCE(a.dtsh_synced,false), COALESCE(a.sync_status,'')
    FROM foghorn.dvr_chapters c JOIN foghorn.artifacts a ON a.artifact_hash = c.playback_artifact_hash
    WHERE c.chapter_id='$FIRST_ID' AND a.tenant_id='$STACK_TENANT_ID'")
  echo "    frozen|dtsh_synced|sync: $row"
  [ "$row" = "t|t|synced" ]
}
eventually 300 "first chapter frozen with its .dtsh synced" frozen
# The chapter's origin edge builds its .dtsh from its own canonical file. A
# resolve that names the edge itself as the chapter's peer-relay origin makes
# its relay fetch the sidecar from itself and stall to its fetch deadline.
no_self_sidecar_fetch() { ! log_has "$LOG_SINCE" "relay dtsh fetch failed.*//$EDGE_A_HOST:8082/internal/artifact/" edge; }
check "the origin edge never fetched a chapter .dtsh from its own relay" no_self_sidecar_fetch

chapter_thumbnails() {
  local hash published code
  hash=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(c.playback_artifact_hash,'') FROM foghorn.dvr_chapters c
    JOIN foghorn.artifacts source ON source.artifact_hash=c.artifact_hash
    WHERE c.chapter_id='$FIRST_ID' AND source.tenant_id='$STACK_TENANT_ID'")
  [ -n "$hash" ] || return 1
  published=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(has_thumbnails,false) FROM foghorn.artifacts
    WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash'")
  [ "$published" = t ] || { echo "    chapter $FIRST_ID thumbnails not published"; return 1; }
  code=$(curl -s -m 10 -o /dev/null -w '%{http_code}' "$CHANDLER_URL/assets/$hash/poster.jpg")
  echo "    chapter poster: HTTP $code"
  [ "$code" = 200 ]
}
eventually 240 "chapter thumbnails are accepted and Chandler serves the poster" chapter_thumbnails

log "recording.ready carries a chapter"
# recording.ready names the chapter that finalized first. That is the first
# chapter unless it is far longer than the next one, as with a long window.
CHAPTER_PBS=$(chapters | jq -c '[.[].playbackId // empty]')
eventually 60 "recording.ready with a chapter's playback id" saw_event "$SINCE" recording.ready \
  ".artifact.playbackId as \$p | $CHAPTER_PBS | any(. == \$p)"
ready_count=$(webhook_events "$SINCE" | awk -v s="$UUID" '$1=="recording.ready" && $3=="valid" && index($0, s) {print $2}' | sort -u | wc -l | tr -d ' ')
check "exactly one recording.ready event for this recording (got $ready_count)" [ "$ready_count" = 1 ]

log "replay"
[ -n "$FIRST_PB" ] && every_replica "chapter replays" "$FIRST_PB" media_at

# Every finalized chapter holds its whole interval: a finalize fed faster than
# its recorder used to lose the chapter's start to buffer eviction (N25).
log "chapters at full length"
while IFS=' ' read -r cid cpb clen; do
  [ -n "$cpb" ] || { fail "chapter $cid has no playback id"; continue; }
  chapter_full() { full_length "$1" "$2" "$clen"; }
  echo "    chapter ${cid:0:8}: expected ${clen}s"
  every_replica "chapter ${cid:0:8} replays its full ${clen}s" "$cpb" chapter_full
done < <(chapters | jq -r '.[] | select(.state=="FINALIZED" or .state=="FROZEN") | "\(.chapterId) \(.playbackId // "") \(((.endMs - .startMs) / 1000) | floor)"')
C=$(chapters)
F=$FAIL
check "a chapter of the full ${WINDOW}s window was recorded" json_has "$C" \
  "any((.state == \"FINALIZED\" or .state == \"FROZEN\") and ((.endMs - .startMs) / 1000) >= $((WINDOW - 4)))"
# The chapters hold the whole ${PUSH}s push back to back: a segment that reached
# the ledger after its chapter was dispatched, or after the stop claimed the
# recording, is in no chapter.
echo "    chapter spans: $(echo "$C" | jq -c 'sort_by(.startMs) | map((.endMs - .startMs) / 1000)')"
check "the chapters hold the whole ${PUSH}s push without a hole" json_has "$C" \
  "sort_by(.startMs) | all(.[]; .state == \"FINALIZED\" or .state == \"FROZEN\")
   and (map(.endMs - .startMs) | add) >= $(((PUSH - 2) * 1000))
   and ([range(1; length) as \$i | (.[\$i].startMs - .[\$i - 1].endMs) | fabs <= 1000] | all)"
if [ "$FAIL" -gt "$F" ]; then
  echo "    segment ledger (offset from the first media ms, duration, status, recorded at):"
  pg "$FOGHORN_A_DB" "SELECT s.segment_name, s.media_start_ms - (EXTRACT(EPOCH FROM a.started_at) * 1000)::bigint, s.duration_ms, s.status, s.created_at
    FROM foghorn.dvr_segments s JOIN foghorn.artifacts a ON a.artifact_hash = s.artifact_hash
    WHERE s.artifact_hash = (SELECT artifact_hash FROM foghorn.dvr_chapters WHERE chapter_id = '$FIRST_ID') AND a.tenant_id = '$STACK_TENANT_ID'
    ORDER BY s.media_start_ms" | sed 's/^/      /'
fi

# Publishers that stall with their connection open: Mist's buffer reaches its
# input timeout, ends the recording, and closes the publisher about 2 s later.
# Helmsman reports the stopped recording on its 5 s push check, so for about
# half the streams the report reaches Foghorn while the session still looks
# live; several streams stall at once so the race is hit. No session may be
# recorded afresh on its gone source (staging rc11: a failed recording 30 s
# after such a stop).
STALLS=4
log "$STALLS stalled publishers end their streams without a failed recording"
STALL_INTERNALS=()
STALL_PUBS=()
for i in $(seq 1 "$STALLS"); do
  SN=$(create_stream "stack-recording-stall-$i-$(date +%s)" true)
  KN=$(echo "$SN" | jq -r '.streamKey // empty')
  [ -n "$KN" ] || { fail "createStream: $SN"; finish; }
  STALL_INTERNALS+=("$(internal_name_of_key "$KN")")
  STALL_PUBS+=("$(publish "$EDGE_A_RTMP" "$KN" 854x480 15 600)")
done
trap 'kill "$PUB" 2>/dev/null; for p in "${STALL_PUBS[@]}"; do kill -CONT "$p" 2>/dev/null; kill "$p" 2>/dev/null; done' EXIT
stall_dvrs() { # stall_dvrs <internal name>: "status|segments" per recording, oldest first
  pg "$FOGHORN_A_DB" "SELECT a.status, (SELECT count(*) FROM foghorn.dvr_segments s WHERE s.artifact_hash = a.artifact_hash)
    FROM foghorn.artifacts a WHERE a.tenant_id='$STACK_TENANT_ID' AND a.artifact_type='dvr'
      AND a.stream_internal_name IN ('$1', 'live+$1') ORDER BY a.created_at"
}
stalls_recording() {
  local n
  for n in "${STALL_INTERNALS[@]}"; do stall_dvrs "$n" | grep -Eq '^recording\|[1-9]' || return 1; done
}
eventually 120 "every stalled stream's recording holds a segment" stalls_recording
# Helmsman counts a recording's segments on its 30 s progress tick, and a writer
# that vanishes before it counted one is recreated rather than reported stopped.
sleep 40
for p in "${STALL_PUBS[@]}"; do kill -STOP "$p"; done
stalls_closed() {
  local n open
  for n in "${STALL_INTERNALS[@]}"; do
    open=$(pg "$FOGHORN_A_DB" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID'
      AND stream_internal_name IN ('$n', 'live+$n') AND ended_at IS NULL")
    [ "$open" = 0 ] || return 1
  done
}
eventually 120 "Mist ends every stalled publisher's session" stalls_closed
# A fresh recording on a gone source fails once Helmsman's push retries run out.
sleep 75
STALL_BAD=""
for n in "${STALL_INTERNALS[@]}"; do
  rows=$(stall_dvrs "$n" | tr '\n' ' ')
  echo "    recordings of ${n:0:8}: $rows"
  [ "$(stall_dvrs "$n" | wc -l | tr -d ' ')" = 1 ] && ! stall_dvrs "$n" | grep -q '^failed|' || STALL_BAD="$STALL_BAD ${n:0:8}"
done
check "ending a stalled publisher started no second recording and left no failed one (bad:${STALL_BAD:- none})" [ -z "$STALL_BAD" ]
for p in "${STALL_PUBS[@]}"; do
  kill -CONT "$p" 2>/dev/null
  kill "$p" 2>/dev/null
done
finish
