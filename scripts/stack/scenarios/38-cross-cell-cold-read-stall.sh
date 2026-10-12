#!/usr/bin/env bash
# stack-scenario: default
# A cell-B edge plays a cell-A VOD whose first cold read stalls: the S3 proxy
# sends 64 KiB of the relay's block GET and then nothing. The relay gives the
# read up after its read-stall bound and resumes from the copied byte before
# Mist's own 5 s source read timeout, so Mist's read never stops: every variant
# spans the full duration and every one of its segments is a real TS segment.
# Catches: the relay's unbounded upstream client behind the rc5 edge-us hole (a
# cold EU DVR chapter block fetch stalled without progress; ~70 s of the chapter
# was unplayable). Without the bound the stalled fetch holds Mist's read until
# Mist times out and reconnects on its own.
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
trap 'fault -d '\''{"stall_artifact":""}'\'' >/dev/null 2>&1' EXIT

ready_synced() {
  local s sync
  s=$(vod_status "$VID")
  sync=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(sync_status,'') FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH'")
  echo "    status=$s sync=${sync:-?}"
  [ "$s" = FAILED ] && return 2
  [ "$s" = READY ] && [ "$sync" = synced ]
}
eventually 300 "VOD ready and synced in cell A" ready_synced

check "the next ranged media read of $VHASH stalls mid-body" fault -d "$(jq -cn --arg h "$VHASH" '{stall_artifact:$h}')"

B=${FOGHORN_B_URLS%% *}
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
check "every segment of the full ${SECONDS_LONG}s plays through cell B" gapless_segments "$B" "$VPB"
finish
