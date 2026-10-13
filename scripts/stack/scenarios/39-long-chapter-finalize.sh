#!/usr/bin/env bash
# stack-scenario: manual
# Scenario 04 with a chapter long enough for its finalize to run at the full
# processing speed: a slot brought up with STACK_DVR_WINDOW_SECONDS of at least
# 1800, the staging chapter length. Thumbs composes its sprite on a wall-clock
# interval, so at that speed the recorder catches up on a long stretch of source
# in one go and the buffer pauses the feed for longer than a reader's 25 s data
# wait. Thumbs must read on through every pause, and the chapter must finalize
# at full length with its thumbnails.
# Catches: the staging rc6 loss of a 30-minute chapter, where Thumbs gave up on
# data the paused feed withheld ("EOP: data wait timeout"), the recorder then
# waited on Thumbs' JPEG lookahead while the feed ran to the end, and the
# finalize stalled until Helmsman failed it, on every retry.
. "$(dirname "$0")/../lib.sh"

MIN_WINDOW=1800
if [ "${STACK_DVR_WINDOW_SECONDS:-120}" -lt "$MIN_WINDOW" ]; then
  blocked "needs a slot brought up with STACK_DVR_WINDOW_SECONDS>=$MIN_WINDOW (got ${STACK_DVR_WINDOW_SECONDS:-120})"
  finish
fi
LONG_SINCE=$(utc_now)
bash "$(dirname "$0")/04-recording.sh"
case $? in
  0) ;;
  2) blocked "the long-window run of scenario 04 was blocked" ;;
  *) fail "the long-window run of scenario 04 failed" ;;
esac
no_thumbs_data_timeout() { ! log_has "$LONG_SINCE" "MistProcThumbs.*EOP: data wait timeout" edge; }
check "Thumbs never gave up on data a paused finalize feed withheld" no_thumbs_data_timeout
finish
