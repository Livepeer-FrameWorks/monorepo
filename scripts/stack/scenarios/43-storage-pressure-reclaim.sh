#!/usr/bin/env bash
# stack-scenario: default
# An edge over its freeze threshold reclaims everything it may: the relay's
# block caches in every cache root (upload sources included) and sidecars whose
# media is gone, and its cleanup pass evicts the lowest-priority durable
# artifacts, a finished recording among them, until usage of the whole store
# is below the pass's target. Foghorn refuses no eviction as s3_object_missing
# while S3 holds the bytes, and the edge reports no candidate Foghorn cannot
# catalog because it is not an artifact at all.
# Measures: the edge's storage and both cleanup loops' logs after a pass at a
# logical capacity that puts the edge at 92% usage.
# Catches: upload/*.blocks never reclaimed, a <hash>.<ext>.dtsh sidecar
# enumerated as artifact "<hash>.<ext>" (not_found on every pass), a DVR's S3
# prefix compared as one object key (s3_object_missing on every pass); together
# they held staging edge-eu at 0.907 with a pass every 6 s freeing nothing; a
# cleanup pass that reports the usage of clips/ alone (0 under a logical
# capacity) instead of the whole store's.
. "$(dirname "$0")/../lib.sh"

EDGE_SERVICE=${STACK_EDGE_A_SERVICE:-edge}
STORE=/data/storage
# Helmsman loads .env from its working directory over the process environment
# at every start (see 30-edge-control-tls.sh).
EDGE_ENV=/opt/frameworks/helmsman/.env
need ffmpeg jq curl docker || finish
if [ "${STACK_TARGET:-stack}" = staging ]; then
  blocked "the scenario lowers a stack edge's logical capacity"
  finish
fi

SIDS=()
PUB=""
helmsman_restart() { stack_exec "$EDGE_SERVICE" /command/s6-svc -r /run/service/helmsman; }
edge_sh() { stack_exec "$EDGE_SERVICE" sh -c "$1"; }
as_helmsman() { stack_exec "$EDGE_SERVICE" /command/s6-setuidgid frameworks sh -c "$1"; }
cleanup() {
  local s
  [ -n "$PUB" ] && kill "$PUB" 2>/dev/null
  if [ -n "${CAPPED:-}" ]; then
    edge_sh "rm -f $EDGE_ENV" && helmsman_restart
  fi
  for s in "${SIDS[@]}"; do delete_stream "$s"; done
}
trap cleanup EXIT
edge_sh "[ ! -e $EDGE_ENV ]" || { blocked "$EDGE_ENV already exists on $EDGE_SERVICE"; finish; }

log "arrange: a finished recording, a processed upload and an orphan sidecar"
S=$(create_stream "stack-pressure-rec-$(date +%s)" true)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
SIDS+=("$SID")
PUB=$(publish "$EDGE_A_RTMP" "$(echo "$S" | jq -r '.streamKey')" 640x360 15 40)

render_mp4 "$STACK_STATE_DIR/pressure-vod.mp4" 854x480 15 20 || { fail "render VOD source"; finish; }
V=$(upload_vod "$STACK_STATE_DIR/pressure-vod.mp4" "stack pressure vod")
VID=$(echo "$V" | jq -r '.id // empty')
[ -n "$VID" ] || { fail "upload: $V"; finish; }
vod_ready() { [ "$(vod_status "$VID")" = READY ]; }
eventually 300 "uploaded VOD READY" vod_ready || finish
upload_blocks() { edge_sh "find $STORE/upload -maxdepth 1 -type d -name '*.blocks' 2>/dev/null"; }
[ -n "$(upload_blocks)" ] || { fail "arrange: processing the upload left no upload/*.blocks cache on $EDGE_SERVICE"; finish; }

wait_publisher "$PUB" 120 || fail "publisher still running 120s after it started"
PUB=""
dvr_row() {
  pg "$FOGHORN_A_DB" "SELECT artifact_hash || '|' || COALESCE(status,'') || '|' || COALESCE(sync_status,'') FROM foghorn.artifacts
    WHERE tenant_id='$STACK_TENANT_ID' AND artifact_type='dvr' AND stream_id='$UUID' ORDER BY created_at DESC LIMIT 1"
}
dvr_synced() {
  local row
  row=$(dvr_row)
  echo "    dvr hash|status|sync: $row"
  [[ "$row" == *"|completed"*"|synced" ]]
}
eventually 240 "recording finalized and synced" dvr_synced || finish
DVR_HASH=$(dvr_row | cut -d'|' -f1)
# A chapter still finalizing holds a lease on the recording's directory.
chapters_frozen() {
  local row
  row=$(pg "$FOGHORN_A_DB" "SELECT count(*) || '|' || count(*) FILTER (WHERE frozen_at IS NULL) FROM foghorn.dvr_chapters WHERE artifact_hash='$DVR_HASH'")
  echo "    chapters|unfrozen: $row"
  [ "${row%%|*}" -gt 0 ] 2>/dev/null && [ "${row#*|}" = 0 ]
}
eventually 300 "every chapter of the recording frozen" chapters_frozen || finish
dvr_dir() { edge_sh "find $STORE/dvr -mindepth 2 -maxdepth 2 -type d -name '$DVR_HASH' 2>/dev/null"; }
DVR_DIR=$(dvr_dir)
[ -n "$DVR_DIR" ] || { fail "arrange: recording $DVR_HASH has no directory on $EDGE_SERVICE"; finish; }
# Foghorn sends the chapter reclaim of the frozen recording's local segments
# asynchronously; usage measured before it lands puts the edge below its
# freeze threshold once it does.
segments_reclaimed() {
  local left
  left=$(edge_sh "find $DVR_DIR -name '*.ts' 2>/dev/null | wc -l")
  echo "    local segments left: $left"
  [ "${left:-1}" -eq 0 ] 2>/dev/null
}
eventually 240 "the frozen recording's local segments reclaimed" segments_reclaimed || finish

# The cleanup loops leave anything younger than an hour alone; age the
# recording's manifest and the orphan as a long-running edge's would be.
OLD=$(($(date +%s) - 7200))
ORPHAN="$STORE/vod/$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n').mkv.dtsh"
as_helmsman "mkdir -p $STORE/vod && head -c 4096 /dev/urandom >$ORPHAN && touch -d @$OLD $ORPHAN" ||
  { fail "arrange: plant an orphan sidecar"; finish; }

# cap_at <percent>: a logical capacity that puts the edge's storage at that
# usage, applied by a Helmsman restart.
store_bytes() { edge_sh "find $STORE -type f -exec stat -c %s {} + 2>/dev/null | awk '{s+=\$1} END {print s+0}'"; }
cap_at() {
  local used
  edge_sh "[ ! -e $DVR_DIR/$DVR_HASH.m3u8 ] || touch -d @$OLD $DVR_DIR/$DVR_HASH.m3u8"
  used=$(store_bytes)
  [ "${used:-0}" -gt 0 ] 2>/dev/null || return 1
  CAP=$((used * 100 / $1))
  echo "    storage ${used} bytes, capacity ${CAP} bytes"
  edge_sh "printf 'HELMSMAN_STORAGE_CAPACITY_BYTES=%s\n' $CAP >$EDGE_ENV && chmod 0644 $EDGE_ENV" || return 1
  CAPPED=1
  helmsman_restart
}
# pass_logged <since> <message>: a cleanup loop finished a pass after <since>.
pass_logged() { [ -n "$(log_json "$1" "select(.msg == \"$2\") | .time" "$EDGE_SERVICE")" ]; }

log "pressure: the freeze pass"
T1=$(utc_now)
cap_at 92 || { fail "cap $EDGE_SERVICE at 92% usage"; finish; }
eventually 420 "the storage manager ran a pressure pass" pass_logged "$T1" "Storage pressure pass completed"

log "pressure: the cleanup pass"
# The freeze pass may have freed enough to drop below the cleanup loop's 90%;
# cap again so the cleanup loop sees the remaining storage at 92%.
if [ -n "$(dvr_dir)" ]; then
  T2=$(utc_now)
  cap_at 92 || { fail "cap $EDGE_SERVICE at 92% usage"; finish; }
  cleanup_pass() { pass_logged "$T2" "Cleanup completed" || pass_logged "$T2" "No cleanup candidates found despite high storage usage"; }
  eventually 420 "the cleanup monitor ran a cleanup pass" cleanup_pass
fi

log "invariants"
blocks_left=$(upload_blocks)
check "no upload block cache left after the pass (left: ${blocks_left:-none})" [ -z "$blocks_left" ]
orphan_gone() { edge_sh "[ ! -e $ORPHAN ]"; }
check "the orphan sidecar is reclaimed" orphan_gone
# The cleanup monitor frees the lowest-priority evictable artifacts until usage
# is below its target, so a recording is evicted only when the artifacts ahead
# of it did not reach the target. Its pass is the first one after the monitor
# start that the last capacity restart caused.
PASS_SINCE=${T2:-$T1}
init=$(log_json_at "$PASS_SINCE" 'select(.msg == "Cleanup monitor initialized") | "\(.at) \(.target_threshold)"' "$EDGE_SERVICE" | tail -n 1)
INIT_AT=${init%% *}
TARGET=${init#* }
pass=$(log_json_at "$PASS_SINCE" "select(.msg == \"Cleanup completed\" and .at > \"$INIT_AT\") | \"\\(.at) \\(.final_usage)\"" "$EDGE_SERVICE" | head -n 1)
DONE_AT=${pass%% *}
FINAL=${pass#* }
usage_now=$(awk -v u="$(store_bytes)" -v c="${CAP:-0}" 'BEGIN { if (c > 0) printf "%.4f", u / c; else print "" }')
echo "    cleanup pass: target ${TARGET:-?} final_usage ${FINAL:-?} store usage now ${usage_now:-?}"
below() { [ -n "$1" ] && [ -n "$2" ] && awk -v a="$1" -v b="$2" 'BEGIN { exit !(a < b) }'; }
reached_target() { [ -n "$INIT_AT" ] && [ -n "$pass" ] && below "$FINAL" "$TARGET"; }
check "the cleanup pass logged its completion below its target (target ${TARGET:-none}, final_usage ${FINAL:-none})" reached_target
# Usage after the pass only falls, so the pass's final usage is at least the
# store's usage now: a measurement of one artifact directory reads near zero.
final_is_root_usage() { [ -n "$pass" ] && [ -n "$usage_now" ] && ! below "$FINAL" "$(awk -v n="$usage_now" 'BEGIN { print n - 0.05 }')"; }
check "the pass's final usage measures the whole store (final_usage ${FINAL:-none}, store now ${usage_now:-none})" final_is_root_usage
others=$(log_json_at "$PASS_SINCE" "select(((.msg == \"Cleaned up artifact\" and .at > \"$INIT_AT\" and .at <= \"${DONE_AT:-$INIT_AT}\") or ((.msg // \"\") | startswith(\"Evicted synced asset\"))) and ((.artifact_hash // .asset_hash) != \"$DVR_HASH\")) | (.artifact_hash // .asset_hash)" "$EDGE_SERVICE" | sort -u | wc -l | tr -d ' ')
echo "    other artifacts evicted since the cleanup pass began: $others"
dvr_gone() { [ -z "$(dvr_dir)" ]; }
# A pass that evicted nothing else reaches its target only by evicting the recording.
evicted_or_preempted() { dvr_gone || { [ "${others:-0}" -gt 0 ] && reached_target; }; }
check "the durable recording's local copy is evicted, or lower-priority artifacts brought the pass below target first (others evicted: ${others:-0})" evicted_or_preempted
missing=$(log_json "$T1" 'select(.reason == "s3_object_missing") | .asset_hash' foghorn foghorn-2 | sort -u | tr '\n' ' ')
check "Foghorn refused no eviction as s3_object_missing (refused: ${missing:-none})" [ -z "$missing" ]
bogus=$(log_json "$T1" 'select((.msg // "") | test("not cataloged")) | .sample_hashes[]? | select(test("^[0-9a-f]+$") | not)' "$EDGE_SERVICE" | sort -u | tr '\n' ' ')
check "every uncataloged candidate is an artifact hash (bogus: ${bogus:-none})" [ -z "$bogus" ]

finish
