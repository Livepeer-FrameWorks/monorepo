#!/usr/bin/env bash
# stack-scenario: default
# staging: safe
# tenant: qa
# A recording stream stopped and published again. Inside Mist's 12 s resume
# window the publisher resumes its tracks and its recording continues. After the
# buffer unloads, the republish's buffer can come up just before its DVR push
# attaches, which is exactly when the push used to end with no tracks and never
# be re-issued: that new broadcast must record (a segment within 30 s) and
# announce recording.started.
# Catches: N19 (republished recordings never start: empty RECORDING_END
# dropped as a parse error, unconfirmed push quarantined forever), N26 (a
# reconnect inside the resume window splitting or ending its recording).
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 || finish
SINCE=$(date +%s)
webhook_endpoint >/dev/null || fail "webhook endpoint at $RECEIVER_HOOK_URL"

S=$(create_stream "stack-republish-$(date +%s)" true)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
ingest() { gql 'query($id:ID!){stream(id:$id){ingest{state sessionId connectedAt liveSince endedAt resumableUntil}}}' "$(jq -cn --arg id "$SID" '{id:$id}')"; }
ingest_live() { json_has "$(ingest)" '.errors==null and .data.stream.ingest.state=="LIVE" and .data.stream.ingest.sessionId!=null and .data.stream.ingest.liveSince!=null'; }

dvr_hashes() { pg commodore "SELECT dvr_hash FROM commodore.dvr_recordings WHERE tenant_id='$STACK_TENANT_ID' AND stream_id='$UUID' ORDER BY created_at"; }
# Numeric reads default to 0 so an empty answer retries instead of making `[`
# exit 2, which eventually() treats as a refusal.
segments_of() {
  local n
  n=$(pg "$FOGHORN_A_DB" "SELECT count(*) FROM foghorn.dvr_segments WHERE artifact_hash='$1'")
  echo "${n:-0}"
}
started_count() {
  local n
  n=$(webhook_events "$SINCE" | awk -v s="$UUID" '$1=="recording.started" && $3=="valid" && index($0, s) {print $2}' | sort -u | wc -l | tr -d ' ')
  echo "${n:-0}"
}

log "first session"
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 854x480 15 40)
trap 'kill "$PUB" 2>/dev/null' EXIT
first_recording() { FIRST=$(dvr_hashes | head -1); [ -n "$FIRST" ] && [ "$(segments_of "$FIRST")" -ge 1 ]; }
eventually 60 "the first session records a segment" first_recording
eventually 30 'the public ingest state exposes the first live session' ingest_live
FIRST_SESSION=$(ingest | jq -r '.data.stream.ingest.sessionId // empty')
wait_publisher "$PUB" 100 || fail "first publisher still running after 100 s"

# Inside the recording resume window a new connector gets its own fenced
# ingest session, while its recording continues through the source handoff.
# Resumed media continues the recording's timeline without the wall-clock gap,
# so progress is counted in
# segments: the segment open at the stop closes, and at least one more follows.
log "republish 5 s after the stop, inside the resume window"
sleep 5
SEGMENTS_AT_STOP=$(segments_of "$FIRST")
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 854x480 15 40)
continues() { [ "$(segments_of "$FIRST")" -ge $((SEGMENTS_AT_STOP + 2)) ]; }
eventually 30 "the resumed publish continues the same recording within 30 s" continues
resumed_ingest() { json_has "$(ingest)" ".errors==null and .data.stream.ingest.state==\"LIVE\" and .data.stream.ingest.sessionId!=null and .data.stream.ingest.liveSince!=null and .data.stream.ingest.sessionId!=\"$FIRST_SESSION\""; }
eventually 15 'the resumed connector has its own fenced ingest session' resumed_ingest
RESUMED_SESSION=$(ingest | jq -r '.data.stream.ingest.sessionId // empty')
one_recording() { [ "$(dvr_hashes | wc -l | tr -d ' ')" = 1 ]; }
check "the resumed publish does not start a second recording" one_recording
wait_publisher "$PUB" 100 || fail "resumed publisher still running after 100 s"

# After the buffer unloads, a republish is a new broadcast: it must record
# (a segment within 30 s) and announce recording.started.
log "republish 30 s after the stop, after the buffer unloaded"
sleep 30
ingest_idle() { json_has "$(ingest)" ".errors==null and .data.stream.ingest.state==\"IDLE\" and .data.stream.ingest.sessionId==\"$RESUMED_SESSION\" and .data.stream.ingest.endedAt!=null and .data.stream.ingest.resumableUntil!=null"; }
eventually 15 'the ended publisher exposes its final session timestamps' ingest_idle
eventually 60 'stream.ended identifies the first unloaded session' saw_event "$SINCE" stream.ended ".streamId==\"$UUID\" and .sessionId==\"$FIRST_SESSION\""
eventually 60 'stream.ended identifies the resumed unloaded session' saw_event "$SINCE" stream.ended ".streamId==\"$UUID\" and .sessionId==\"$RESUMED_SESSION\""
REPUB_AT=$(date +%s)
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 854x480 15 60)
new_dvr() { SECOND=$(dvr_hashes | tail -1); [ -n "$SECOND" ] && [ "$SECOND" != "$FIRST" ]; }
eventually 30 "the republish starts a new recording" new_dvr
new_ingest() { json_has "$(ingest)" ".errors==null and .data.stream.ingest.state==\"LIVE\" and .data.stream.ingest.liveSince!=null and .data.stream.ingest.sessionId!=null and .data.stream.ingest.sessionId!=\"$FIRST_SESSION\" and .data.stream.ingest.sessionId!=\"$RESUMED_SESSION\""; }
eventually 15 'a new broadcast has a new public ingest session' new_ingest
# Only a recording distinct from the first session counts: without a new DVR,
# SECOND still names the first recording and its segments would pass this.
second_records() { [ -n "$SECOND" ] && [ "$SECOND" != "$FIRST" ] && [ "$(segments_of "$SECOND")" -ge 1 ]; }
left=$((30 - ($(date +%s) - REPUB_AT)))
[ "$left" -ge 1 ] || left=1
eventually "$left" "the republished recording has a segment within 30 s" second_records
two_started() { [ "$(started_count)" -ge 2 ]; }
eventually 60 "recording.started delivered for both recordings" two_started

kill "$PUB" 2>/dev/null
delete_stream "$SID" >/dev/null
finish
