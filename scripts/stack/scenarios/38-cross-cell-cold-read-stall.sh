#!/usr/bin/env bash
# stack-scenario: default
# A cell-B edge plays cell-A VODs whose first cold read stalls.
#
# One stall: the S3 proxy sends 64 KiB of the relay's block GET and then
# nothing. The relay gives the read up after its read-stall bound and resumes
# from the copied byte before Mist's own 5 s source read timeout, so Mist's read
# never stops: every variant spans the full duration and every one of its
# segments is a real TS segment.
# Catches: the relay's unbounded upstream client behind the rc5 edge-us hole (a
# cold EU DVR chapter block fetch stalled without progress; ~70 s of the chapter
# was unplayable). Without the bound the stalled fetch holds Mist's read until
# Mist times out and reconnects on its own.
#
# A store slow at one byte: every read of the block from its first byte stalls
# at the same byte, and every resume answers 4 s late, past what is left of
# Mist's 5 s budget. Mist gives the first read up once; the relay's block fill
# does not belong to that read, so it resumes and completes, and Mist's retry
# reads the block from the fill instead of starting it again.
# Catches: the rc9 cold cross-cell VOD read that stalled ~48 s (Mist timed out
# 9 times; every retry restarted the 32 MiB block from byte 0 and stalled at the
# same offset, because the fill was cancelled with the read that started it).
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 || finish
[ -n "${STACK_THUMBNAIL_FAULT_URL:-}" ] && [ -n "${SERVICE_TOKEN:-}" ] || {
  blocked 'the S3 fault proxy and its control credential are required'
  finish
}
fault() {
  curl -fsS -m 10 -H "Authorization: Bearer $SERVICE_TOKEN" \
    -H 'Content-Type: application/json' "$@" "$STACK_THUMBNAIL_FAULT_URL"
}
fault -d '{"stall_artifact":""}' >/dev/null || { blocked 'S3 fault proxy is unreachable'; finish; }

SECONDS_LONG=24
render_mp4 "$STACK_STATE_DIR/vod-cold-stall.mp4" 640x360 15 "$SECONDS_LONG" || { fail "render VOD source"; finish; }
V=$(upload_vod "$STACK_STATE_DIR/vod-cold-stall.mp4" "stack cold read stall")
VID=$(echo "$V" | jq -r '.id // empty')
VHASH=$(echo "$V" | jq -r '.artifactHash // empty')
VPB=$(echo "$V" | jq -r '.playbackId // empty')
[ -n "$VID" ] && [ -n "$VHASH" ] || { fail "upload: $V"; finish; }
render_mp4 "$STACK_STATE_DIR/vod-cold-slow-byte.mp4" 640x360 12 "$SECONDS_LONG" || { fail "render second VOD source"; finish; }
W=$(upload_vod "$STACK_STATE_DIR/vod-cold-slow-byte.mp4" "stack cold read slow byte")
WID=$(echo "$W" | jq -r '.id // empty')
WHASH=$(echo "$W" | jq -r '.artifactHash // empty')
WPB=$(echo "$W" | jq -r '.playbackId // empty')
[ -n "$WID" ] && [ -n "$WHASH" ] || { fail "upload: $W"; finish; }
trap 'fault -d '\''{"stall_artifact":""}'\'' >/dev/null 2>&1' EXIT

ready_synced() { # ready_synced <vod id> <artifact hash>
  local s sync
  s=$(vod_status "$1")
  sync=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(sync_status,'') FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$2'")
  echo "    status=$s sync=${sync:-?}"
  [ "$s" = FAILED ] && return 2
  [ "$s" = READY ] && [ "$sync" = synced ]
}
eventually 300 "VOD ready and synced in cell A" ready_synced "$VID" "$VHASH"
eventually 300 "second VOD ready and synced in cell A" ready_synced "$WID" "$WHASH"

# gapless_segments <foghorn> <playback id>: every variant's segments, each
# fetched in full and checked to be transport stream, and the summed duration.
gapless_segments() {
  local loc
  loc=$(location_at "$1" "$2")
  [ -n "$loc" ] || return 1
  python3 - "$loc" "$SECONDS_LONG" <<'PY'
import sys, urllib.request, urllib.parse
def get(url):
    with urllib.request.urlopen(url, timeout=20) as r:
        return r.read()
master, want = sys.argv[1], float(sys.argv[2])
body = get(master).decode("utf-8", "replace")
variants = [urllib.parse.urljoin(master, l.strip()) for l in body.splitlines() if l.strip() and not l.startswith("#")]
if "#EXT-X-STREAM-INF" not in body:
    variants = [master]
ok = bool(variants)
for url in variants:
    playlist = get(url).decode("utf-8", "replace")
    total = sum(float(l.split(":")[1].split(",")[0]) for l in playlist.splitlines() if l.startswith("#EXTINF:"))
    segments = [urllib.parse.urljoin(url, l.strip()) for l in playlist.splitlines() if l.strip() and not l.startswith("#")]
    bad = 0
    for seg in segments:
        data = get(seg)
        if len(data) < 3 * 188 or len(data) % 188 or any(data[i] != 0x47 for i in range(0, len(data), 188)):
            bad += 1
    print(f"    variant {url.rsplit('/', 1)[-1].split('?')[0]}: {len(segments)} segments, {total:.1f}s, {bad} not TS")
    if not segments or bad or total < want - 2 or total > want + 2:
        ok = False
sys.exit(0 if ok else 1)
PY
}

B=${FOGHORN_B_URLS%% *}

check "the next ranged media read of $VHASH stalls mid-body" fault -d "$(jq -cn --arg h "$VHASH" '{stall_artifact:$h}')"
SINCE=$(utc_now)
eventually 90 "first cross-cell play through cell B gets media" media_at "$B" "$VPB"

STALL=$(fault | jq -c '.stall')
echo "    proxy: $STALL"
check "a cold block read of $VHASH stalled mid-body" json_has "$STALL" '.stalled != null'
check "the relay resumed the stalled read from inside its range" json_has "$STALL" '.resumes | length >= 1'
# Mist's downloader gives a source read up after 5 s without data and logs it;
# the relay resumes a stalled upstream before that, so Mist's read never stops.
if log_has "$SINCE" "Timeout while retrieving .*$VHASH" "$STACK_EDGE_B_SERVICE"; then
  fail "Mist on edge-b timed out reading $VHASH through the relay"
else
  pass "Mist on edge-b never timed out reading $VHASH through the relay"
fi
check "every segment of the full ${SECONDS_LONG}s plays through cell B" gapless_segments "$B" "$VPB"

check "every read of $WHASH from its block start stalls and every resume answers 4 s late" \
  fault -d "$(jq -cn --arg h "$WHASH" '{stall_artifact:$h,stall_resume_delay:4}')"
SINCE=$(utc_now)
STARTED=$(date +%s)
eventually 60 "first cross-cell play of the slow-byte VOD through cell B gets media" media_at "$B" "$WPB"
echo "    first media after $(($(date +%s) - STARTED))s"

STALL=$(fault | jq -c '.stall')
echo "    proxy: $STALL"
check "a cold block read of $WHASH stalled mid-body" json_has "$STALL" '.stalled != null'
check "the block fill resumed past the reader that gave up" json_has "$STALL" '.resumes | length >= 1'
check "no read requested the stalled block from its first byte again" json_has "$STALL" '.restarts == 0'
TIMEOUTS=$(logs_since "$SINCE" "$STACK_EDGE_B_SERVICE" | grep -cE "Timeout while retrieving .*$WHASH" || true)
echo "    Mist source read timeouts for $WHASH: $TIMEOUTS"
if [ "${TIMEOUTS:-0}" -le 1 ]; then
  pass "Mist on edge-b read $WHASH within one retry"
else
  fail "Mist on edge-b timed out $TIMEOUTS times reading $WHASH through the relay"
fi
check "every segment of the slow-byte VOD's ${SECONDS_LONG}s plays through cell B" gapless_segments "$B" "$WPB"
finish
