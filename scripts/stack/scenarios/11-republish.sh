#!/usr/bin/env bash
# stack-scenario: default
# A recording stream stopped and republished within seconds: the republish's
# buffer can come up just before its DVR push attaches, which is exactly when
# the push used to end with no tracks and never be re-issued. The new session
# must record (a segment within 30 s) and announce recording.started.
# Catches: N19 (republished recordings never start: empty RECORDING_END
# dropped as a parse error, unconfirmed push quarantined forever).
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 || finish
SINCE=$(date +%s)
webhook_endpoint >/dev/null || fail "webhook endpoint at $RECEIVER_HOOK_URL"

S=$(create_stream "stack-republish-$(date +%s)" true)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
KEY=$(echo "$S" | jq -r '.streamKey // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }

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
wait_publisher "$PUB" 100 || fail "first publisher still running after 100 s"

log "republish 10 s after the stop"
sleep 10
REPUB_AT=$(date +%s)
PUB=$(publish "$EDGE_A_RTMP" "$KEY" 854x480 15 90)
new_dvr() { SECOND=$(dvr_hashes | tail -1); [ -n "$SECOND" ] && [ "$SECOND" != "$FIRST" ]; }
eventually 30 "the republish starts a new recording" new_dvr
# Only a recording distinct from the first session counts: without a new DVR,
# SECOND still names the first recording and its segments would pass this.
second_records() { [ -n "$SECOND" ] && [ "$SECOND" != "$FIRST" ] && [ "$(segments_of "$SECOND")" -ge 1 ]; }
left=$((30 - ($(date +%s) - REPUB_AT)))
[ "$left" -ge 1 ] || left=1
eventually "$left" "the republished recording has a segment within 30 s" second_records
two_started() { [ "$(started_count)" -ge 2 ]; }
eventually 60 "recording.started delivered for both sessions" two_started

kill "$PUB" 2>/dev/null
delete_stream "$SID" >/dev/null
finish
