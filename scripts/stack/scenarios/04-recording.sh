#!/usr/bin/env bash
# stack-scenario: default
# A default record:true stream, pushed past one DVR window and stopped:
# chapters close, finalize faster than real time, freeze with an accepted
# .dtsh, keep their id across the stop, announce recording.ready with the
# chapter's playback id, and replay through every replica of both cells.
# Catches: F9 (default not replayable), R1 (two inputs -> Mist crash), R2 (DTSH
# upload truncated, chapters never freeze), R4 (finalize pinned at 1x), N16
# (recording.ready at stop without a playback id; chapter id changed at stop),
# N8 (cross-cell chapter replay refused on non-leaders).
. "$(dirname "$0")/../lib.sh"

WINDOW=${STACK_DVR_WINDOW_SECONDS:-120}
PUSH=$((WINDOW + 90))
# Finalize runs unconstrained (R4): well under real time for a WINDOW-long chapter.
FINALIZE_BOUND=${STACK_FINALIZE_BOUND_SECONDS:-$((WINDOW / 2 + 90))}
need ffmpeg jq curl python3 || finish
SINCE=$(date +%s)
webhook_endpoint >/dev/null || fail "webhook endpoint at $RECEIVER_HOOK_URL"

S=$(create_stream "stack-recording-$(date +%s)" true)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
check "record:true without settings defaults to WINDOW_SIZED" json_has "$S" '.dvrChapterMode == "WINDOW_SIZED"'
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 854x480 15 "$PUSH")
trap 'kill "$PUB" 2>/dev/null' EXIT

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
# Before any chapter finalizes, recording.ready must not have fired.
if chapters | jq -e 'all(.state != "FINALIZED" and .state != "FROZEN")' >/dev/null; then
  no_early_ready() { ! saw_event "$SINCE" recording.ready; }
  check "no recording.ready while chapters are still finalizing" no_early_ready
fi

all_final() {
  local c
  c=$(chapters)
  echo "    chapters: $(echo "$c" | jq -c 'map({id:.chapterId[0:8],state,segs:.segmentCount})')"
  echo "$c" | jq -e 'length > 0 and all(.state == "FINALIZED" or .state == "FROZEN")' >/dev/null
}
eventually "$FINALIZE_BOUND" "every chapter FINALIZED within ${FINALIZE_BOUND}s of the stop" all_final
echo "    finalized after $(($(date +%s) - STOPPED))s"
C=$(chapters)
check "the first chapter kept its id across the stop ($FIRST_ID)" json_has "$C" "any(.chapterId == \"$FIRST_ID\")"
FIRST_PB=$(echo "$C" | jq -r --arg id "$FIRST_ID" '.[] | select(.chapterId==$id) | .playbackId // empty')
check "the finalized chapter has a playback id" [ -n "$FIRST_PB" ]

log "freeze: .dtsh accepted, chapter frozen"
frozen() {
  local row
  row=$(pg "$FOGHORN_A_DB" "SELECT c.frozen_at IS NOT NULL, COALESCE(a.dtsh_synced,false), COALESCE(a.sync_status,'')
    FROM foghorn.dvr_chapters c JOIN foghorn.artifacts a ON a.artifact_hash = c.playback_artifact_hash
    WHERE c.chapter_id='$FIRST_ID'")
  echo "    frozen|dtsh_synced|sync: $row"
  [ "$row" = "t|t|synced" ]
}
eventually 300 "first chapter frozen with its .dtsh synced" frozen

log "recording.ready carries the chapter"
eventually 60 "recording.ready with the first chapter's playback id" saw_event "$SINCE" recording.ready ".artifact.playbackId == \"$FIRST_PB\""
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
check "a chapter of the full ${WINDOW}s window was recorded" json_has "$(chapters)" \
  "any((.state == \"FINALIZED\" or .state == \"FROZEN\") and ((.endMs - .startMs) / 1000) >= $((WINDOW - 4)))"
finish
