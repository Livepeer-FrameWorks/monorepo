#!/usr/bin/env bash
# End-to-end media lifecycle on the two-cell dev stack, with real media:
#   live publish -> viewers resolved on both cells and segments fetched
#   -> clip from the live buffer -> DVR (segment ledger + chapters)
#   -> VOD upload -> processing -> playback -> analytics rows + GraphQL summary.
#
# It runs on the stack scripts/verify-two-cell-media.sh leaves behind (both
# cells activated, the tenant's serve policy preferring the private cell), and
# brings up the analytics services that proof does not need. Every step reports
# PASS/FAIL with its evidence and the run continues, so one failure cannot hide
# the others; the exit status is non-zero when any step failed.
#
#   MIST_IMAGE=<image> make verify-media-lifecycle
#   MEDIA_LIFECYCLE_BOOTSTRAP=1 ... runs the two-cell proof first (fresh volume required)
set -uo pipefail
: "${MIST_IMAGE:?Set MIST_IMAGE to a full-feature Mist image (Dockerfile.mistserver build; the source-contract image cannot run processing)}"
export MIST_IMAGE
# The source-contract image (pkg/mist/testdata/Dockerfile.source-contract) is built with
# WITH_AV=false, which drops MistProcAV and MistProcThumbs; clip/VOD processing on it
# records nothing and this proof would report a product failure that is only a build gap.
if [ "$(docker image inspect --format '{{index .Config.Labels "org.frameworks.mist.purpose"}}' "$MIST_IMAGE" 2>/dev/null)" = "source-contract-not-release" ]; then
  echo "ERROR: $MIST_IMAGE is a source-contract test build (no processing binaries); use a full-feature Mist image" >&2
  exit 1
fi
for bin in MistProcAV MistProcThumbs; do
  docker run --rm --entrypoint sh "$MIST_IMAGE" -c "command -v $bin >/dev/null" 2>/dev/null || { echo "ERROR: $MIST_IMAGE lacks $bin; clip/VOD processing cannot run" >&2; exit 1; }
done
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root" || exit 1

if [ "${MEDIA_LIFECYCLE_BOOTSTRAP:-}" = 1 ]; then
  bash scripts/verify-two-cell-media.sh || exit 1
fi

API_TOKEN=fw_0000000000000000000000000000000000000000000000000000000000demo01
STREAM_KEY=sk_demo_live_stream_primary_key
PLAYBACK_ID=pb_demo_live_001
FOGHORN_A=http://127.0.0.1:18008
FOGHORN_B=http://127.0.0.1:18058
BRIDGE=http://127.0.0.1:18000
CH="http://127.0.0.1:${CLICKHOUSE_HTTP_PORT:-8123}/?user=$(grep '^CLICKHOUSE_USER=' .env | cut -d'"' -f2)&password=$(grep '^CLICKHOUSE_PASSWORD=' .env | cut -d'"' -f2)"
PUBLISHER=fw-two-cell-publisher
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

PASS=0; FAIL=0
ts()   { date -u +%H:%M:%S; }
log()  { printf '\n[lifecycle %s] %s\n' "$(ts)" "$*"; }
ok()   { PASS=$((PASS+1)); printf '  PASS  %s  (%s)\n' "$*" "$(ts)"; }
bad()  { FAIL=$((FAIL+1)); printf '  FAIL  %s  (%s)\n' "$*" "$(ts)"; }
compose() { docker compose --profile two-cell "$@"; }
gql()  { curl -s -m 30 "$BRIDGE/graphql" -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
           --data "$(jq -cn --arg q "$1" --argjson v "${2:-{\}}" '{query:$q, variables:$v}')"; }
ch()   { curl -s -m 20 "$CH" --data-binary "$1"; }
# docker exec, not compose exec: the latter swallows the caller's stdin, which a poll loop still owns.
pg()   { docker exec -i frameworks-postgres psql -U "$1" -d "$1" -Atc "$2" 2>/dev/null </dev/null; }
wait_until() { # wait_until <secs> <description> <command...>
  local dl=$(( $(date +%s) + $1 )); local what=$2; shift 2
  until "$@"; do [ "$(date +%s)" -ge "$dl" ] && { bad "timed out: $what"; return 1; }; sleep 3; done; ok "$what"
}
location_of() { curl -s -m 10 -o /dev/null -D - "$1/play/$PLAYBACK_ID/hls" | awk 'tolower($1)=="location:"{print $2}' | tr -d '\r'; }
http_code() { curl -s -m 10 -o /dev/null -w '%{http_code}' "$1"; }
playable() { local st; st=$(http_code "$1"); echo "    playback HTTP $st"; [ "$st" = 307 ] || [ "$st" = 200 ]; }
in_stack() { # in_stack <sh -c script>: run inside the compose network with the Mist image's curl/ffmpeg
  docker run --rm --network "$NETWORK" --entrypoint sh "$MIST_IMAGE" -c "$1"
}

log "0/6 stack: analytics services, publisher, stream identity"
# --no-deps: the proof already runs postgres/kafka/clickhouse/quartermaster; letting compose
# re-evaluate those here recreated quartermaster mid-run and broke every service's routing.
compose up -d --no-deps --build periscope-ingest periscope-metering periscope-query >/dev/null 2>&1 || bad "analytics services did not come up"
NETWORK="$(docker network ls --format '{{.Name}}' | grep -E '_frameworks$' | head -1)"
[ -n "$NETWORK" ] || { bad "compose network not found"; echo "PASS=$PASS FAIL=$FAIL"; exit 1; }
if ! docker ps --format '{{.Names}}' | grep -qx "$PUBLISHER"; then
  # Same publisher as the two-cell proof's start_publisher (test pattern, looped, no FLV metadata track).
  docker run -d --name "$PUBLISHER" --network "$NETWORK" --entrypoint sh "$MIST_IMAGE" -c \
    "ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=320x180:rate=15 -f lavfi -i sine=frequency=440:sample_rate=48000 -t 20 -c:v libx264 -g 15 -pix_fmt yuv420p -c:a aac /tmp/source.mp4 && while true; do ffmpeg -hide_banner -loglevel warning -re -stream_loop -1 -i /tmp/source.mp4 -map 0:v:0 -map 0:a:0 -c copy -flvflags no_metadata -f flv rtmp://mistserver:1935/live/$STREAM_KEY; sleep 3; done" >/dev/null
fi
R=$(gql 'query{ streamsConnection(page:{first:20}){ edges{ node{ id playbackId } } } }')
STREAM_GID=$(echo "$R" | jq -r --arg p "$PLAYBACK_ID" '.data.streamsConnection.edges[].node | select(.playbackId==$p) | .id')
[ -n "$STREAM_GID" ] && ok "stream $PLAYBACK_ID resolved via API ($STREAM_GID)" || bad "stream $PLAYBACK_ID not listed by the API: $(echo "$R" | head -c 300)"

log "1/6 live: both cells resolve a viewer and serve real segments"
wait_until 120 "cell A resolves a viewer" bash -c "[ -n \"\$(curl -s -m 10 -o /dev/null -D - $FOGHORN_A/play/$PLAYBACK_ID/hls | grep -i '^location:')\" ]"
wait_until 120 "cell B resolves a viewer" bash -c "[ -n \"\$(curl -s -m 10 -o /dev/null -D - $FOGHORN_B/play/$PLAYBACK_ID/hls | grep -i '^location:')\" ]"
loc_a=$(location_of "$FOGHORN_A"); loc_b=$(location_of "$FOGHORN_B")
echo "  A -> $loc_a"; echo "  B -> $loc_b"
# One viewer session: master playlist -> first variant playlist -> first segment, each
# relative to its playlist and carrying that session's token; prints "<code> <bytes>".
# One pass per attempt, like the two-cell proof's media_flows: every master fetch is a
# new viewer session, so the playlists must not be re-fetched between steps.
fetch_segment() { # fetch_segment <master playlist url>
  docker run --rm -i --network "$NETWORK" --entrypoint python3 "$MIST_IMAGE" - "$1" <<'PY' 2>/dev/null
import sys, urllib.request
def get(url, limit=None):
    with urllib.request.urlopen(url, timeout=10) as r:
        return r.status, (r.read(limit) if limit else r.read())
def entries(body, base):
    out = []
    for line in body.decode("utf-8", "replace").splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            out.append(line if line.startswith("http") else base + "/" + line)
    return out
try:
    url = sys.argv[1]
    for _ in range(3):  # master -> variant -> segment (nested playlists tolerated)
        code, body = get(url)
        if url.split("?")[0].endswith(".m3u8"):
            nxt = entries(body, url.rsplit("/", 1)[0])
            if not nxt:
                print(f"{code} 0"); sys.exit(0)
            url = nxt[0]
            continue
        print(f"{code} {len(body)}"); sys.exit(0)
    print("000 0")
except Exception as exc:  # a refused/absent session is a failed attempt, not a crash
    print(f"000 0 {exc.__class__.__name__}")
PY
}
media_at() { # media_at <master playlist url>: a real segment with bytes
  local got; got=$(fetch_segment "$1"); echo "    segment fetch: ${got:-<none>}"
  case "$got" in 200\ *) [ "${got#200 }" -gt 0 ] ;; *) false ;; esac
}
# A freshly (re)started publisher is placed and pulled across cells before a replica
# serves media; the two-cell proof allows 120s for the same, so does this.
for cell in "A:$loc_a" "B:$loc_b"; do
  loc=${cell#*:}; [ -n "$loc" ] || continue
  wait_until 300 "media flows at cell ${cell%%:*}" media_at "$loc"
  # Two more viewers per cell so routing/viewer analytics have rows without load.
  media_at "$loc" >/dev/null; media_at "$loc" >/dev/null
done

log "2/6 clip from the live buffer (staged on the ingest node under the tenant's serve policy)"
# Clip dispatch needs Foghorn to know the live buffer covers the requested range; right
# after a (re)started publisher that coverage is reported a little behind real time, so a
# "no source … coverage" refusal is retried for a while, never any other error.
create_clip() {
  local now; now=$(date +%s)
  R=$(gql 'mutation($i: CreateClipInput!){ createClip(input:$i){ __typename ... on Clip{ id clipHash playbackId status } ... on ValidationError{ message } ... on NotFoundError{ message } ... on AuthError{ message } } }' \
       "$(jq -cn --arg s "$STREAM_GID" --argjson start $((now-20)) '{i:{streamId:$s,title:"lifecycle clip",mode:"DURATION",startUnix:$start,duration:10}}')")
  echo "  createClip: $(echo "$R" | jq -c '.data.createClip // .errors')"
  echo "$R" | jq -e '.data.createClip.__typename=="Clip"' >/dev/null 2>&1 && return 0
  echo "$R" | grep -q "no source with at least one whole second" && return 1
  return 0
}
wait_until 90 "clip request accepted (live buffer coverage known)" create_clip
CLIP_ID=$(echo "$R" | jq -r '.data.createClip.id // empty'); CLIP_PB=$(echo "$R" | jq -r '.data.createClip.playbackId // empty'); CLIP_HASH=$(echo "$R" | jq -r '.data.createClip.clipHash // empty')
if [ -n "$CLIP_ID" ]; then
  clip_status() { gql 'query($id:ID!){ node(id:$id){ ... on Clip{ status } } }' "$(jq -cn --arg id "$CLIP_ID" '{id:$id}')" | jq -r '.data.node.status // "?"'; }
  # The API reports a finished clip as "done" (the artifact row is "ready").
  clip_finished() { case "$1" in done|ready) return 0;; esac; return 1; }
  clip_ready() { local s; s=$(clip_status); echo "    clip status: $s"; clip_finished "$s" && return 0; [ "$s" = failed ] && { echo "    job: $(pg foghorn "SELECT status||' '||COALESCE(error_message,'') FROM foghorn.processing_jobs WHERE artifact_hash='$CLIP_HASH'")"; return 0; }; return 1; }
  wait_until 240 "clip reaches a terminal status" clip_ready
  clip_finished "$(clip_status)" && ok "clip finished" || bad "clip did not finish ($(clip_status))"
  # Stored media is served from the tenant's preferred cell (B under the proof's policy)
  # once the clip has synced to durable storage; a cell the policy does not permit
  # refuses instead of redirecting, so check the preferred cell first.
  clip_playable() { playable "$FOGHORN_B/play/$CLIP_PB/hls" || playable "$FOGHORN_A/play/$CLIP_PB/hls"; }
  wait_until 180 "clip playable via a permitted cell" clip_playable
else bad "no clip created"; fi

log "3/6 DVR: segment ledger and chapters"
R=$(gql 'mutation($s:ID!){ startDVR(streamId:$s){ __typename ... on DVRRequest{ dvrHash playbackId } ... on ValidationError{ message } ... on NotFoundError{ message } ... on AuthError{ message } } }' "$(jq -cn --arg s "$STREAM_GID" '{s:$s}')")
echo "  startDVR: $(echo "$R" | jq -c '.data.startDVR // .errors')"
DVR=$(echo "$R" | jq -r '.data.startDVR.dvrHash // empty')
if [ -n "$DVR" ]; then
  segs() { pg foghorn "SELECT status||'='||count(*) FROM foghorn.dvr_segments WHERE artifact_hash='$DVR' GROUP BY status ORDER BY status" | tr '\n' ' '; }
  has_uploaded() { local s; s=$(segs); echo "    ledger: ${s:-<none>}"; echo "$s" | grep -q 'uploaded='; }
  wait_until 300 "DVR segments reach 'uploaded' in foghorn.dvr_segments" has_uploaded
  pend=$(pg foghorn "SELECT count(*) FROM foghorn.dvr_segments WHERE artifact_hash='$DVR' AND status='pending' AND created_at < NOW()-INTERVAL '60 seconds'")
  [ "${pend:-0}" = 0 ] && ok "no segment stuck 'pending' >60s" || bad "$pend segment(s) stuck pending >60s"
  chapters() { gql 'query($d:ID!){ dvrChapters(dvrId:$d){ chapters{ chapterId state segmentCount hasGaps } } }' "$(jq -cn --arg d "$DVR" '{d:$d}')" | jq -c '.data.dvrChapters.chapters // .errors'; }
  has_chapter() { local c; c=$(chapters); echo "    chapters: $c"; echo "$c" | jq -e 'type=="array" and length>0 and (map(.segmentCount)|max>0)' >/dev/null 2>&1; }
  # Chapter rows exist only once a chapter closes, and only for a DVR whose stream
  # has a chapter mode configured; the demo stream records a rolling window without
  # one, so the API must answer with an empty page rather than an error.
  chapter_mode=$(pg foghorn "SELECT COALESCE(dvr_chapter_mode,'') FROM foghorn.artifacts WHERE artifact_hash='$DVR' AND artifact_type='dvr'")
  if [ -n "$chapter_mode" ] && [ "$chapter_mode" != "none" ]; then
    wait_until 300 "dvrChapters reports a closed chapter with segments (mode $chapter_mode)" has_chapter
  else
    c=$(chapters); echo "    chapters: $c (no chapter mode configured for this stream)"
    echo "$c" | jq -e 'type=="array"' >/dev/null 2>&1 && ok "dvrChapters answers for a rolling-only DVR" || bad "dvrChapters errored: $c"
  fi
  echo "  stopDVR: $(gql 'mutation($d:ID!){ stopDVR(dvrHash:$d){ __typename } }' "$(jq -cn --arg d "$DVR" '{d:$d}')" | jq -c '.data.stopDVR // .errors')"
else bad "no DVR started"; fi

log "4/6 VOD: multipart upload -> complete -> processing -> playback"
# The MP4 muxer needs a seekable output for +faststart, so render to a file and stream it out.
in_stack "ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=320x180:rate=15 -f lavfi -i sine=frequency=440:sample_rate=48000 -t 6 -c:v libx264 -g 15 -pix_fmt yuv420p -c:a aac -movflags +faststart /tmp/vod.mp4 && cat /tmp/vod.mp4" > "$WORK/vod.mp4" 2>/dev/null
SZ=$(stat -f %z "$WORK/vod.mp4" 2>/dev/null || stat -c %s "$WORK/vod.mp4")
[ "${SZ:-0}" -gt 10000 ] && ok "VOD source rendered ($SZ bytes)" || bad "VOD source not rendered"
R=$(gql 'mutation($i:CreateVodUploadInput!){ createVodUpload(input:$i){ __typename ... on VodUploadSession{ id artifactHash playbackId partSize parts{ partNumber presignedUrl } } ... on ValidationError{ message } ... on AuthError{ message } } }' \
     "$(jq -cn --argjson sz "$SZ" '{i:{filename:"lifecycle.mp4",sizeBytes:$sz,contentType:"video/mp4",title:"lifecycle vod"}}')")
echo "  createVodUpload: $(echo "$R" | jq -c '.data.createVodUpload | del(.parts)' 2>/dev/null || echo "$R" | head -c 300)"
UP_ID=$(echo "$R" | jq -r '.data.createVodUpload.id // empty'); VOD_PB=$(echo "$R" | jq -r '.data.createVodUpload.playbackId // empty'); VOD_HASH=$(echo "$R" | jq -r '.data.createVodUpload.artifactHash // empty')
if [ -n "$UP_ID" ]; then
  PARTS='[]'
  for pn in $(echo "$R" | jq -r '.data.createVodUpload.parts[].partNumber'); do
    url=$(echo "$R" | jq -r --argjson pn "$pn" '.data.createVodUpload.parts[]|select(.partNumber==$pn).presignedUrl')
    etag=$(curl -s -m 120 -X PUT --data-binary "@$WORK/vod.mp4" -D - -o /dev/null "$url" | awk 'tolower($1)=="etag:"{print $2}' | tr -d '\r"')
    echo "  PUT part $pn -> etag ${etag:-<none>}"
    PARTS=$(echo "$PARTS" | jq -c --argjson pn "$pn" --arg e "$etag" '. + [{partNumber:$pn,etag:$e}]')
  done
  R2=$(gql 'mutation($i:CompleteVodUploadInput!){ completeVodUpload(input:$i){ __typename ... on VodAsset{ id playbackId status } ... on ValidationError{ message } ... on NotFoundError{ message } } }' "$(jq -cn --arg u "$UP_ID" --argjson p "$PARTS" '{i:{uploadId:$u,parts:$p}}')")
  echo "  completeVodUpload: $(echo "$R2" | jq -c '.data.completeVodUpload // .errors')"
  echo "$R2" | jq -e '.data.completeVodUpload.__typename=="VodAsset"' >/dev/null && ok "VOD upload completed" || bad "VOD complete failed"
  # Processed VOD is stored media: served from the tenant's preferred cell (B under the
  # proof's policy) once synced, and only after processing published it (status ready).
  vod_ready() { [ "$(pg foghorn "SELECT status FROM foghorn.artifacts WHERE artifact_hash='$VOD_HASH'")" = ready ]; }
  vod_play() { if ! vod_ready; then echo "    job: $(pg foghorn "SELECT status||' '||COALESCE(error_message,'') FROM foghorn.processing_jobs WHERE artifact_hash='$VOD_HASH'")"; return 1; fi; playable "$FOGHORN_B/play/$VOD_PB/hls" || playable "$FOGHORN_A/play/$VOD_PB/hls"; }
  wait_until 300 "uploaded VOD processed (artifact ready) and playable via a permitted cell" vod_play
else bad "no VOD upload session"; fi

log "5/6 analytics: ClickHouse rows and the GraphQL stream summary"
sleep 20
for t in routing_decisions:timestamp viewer_connection_events:timestamp viewer_sessions_current:connected_at federation_events:timestamp; do
  col=${t#*:}; t=${t%%:*}
  n=$(ch "SELECT count() FROM periscope.$t WHERE $col > now() - INTERVAL 30 MINUTE")
  printf '  %-26s %s rows (last 30m)\n' "$t" "${n:-?}"
  [ "${n:-0}" -gt 0 ] 2>/dev/null && ok "$t has rows" || bad "$t empty: ${n:-no response}"
done
S=$(gql 'query($id:ID!,$r:TimeRangeInput){ analytics { usage { streaming { streamAnalyticsSummary(streamId:$id,timeRange:$r){ rangeTotalViews rangeTotalSessions rangePeakConcurrentViewers } } } } }' \
    "$(jq -cn --arg id "$STREAM_GID" --arg s "$(date -u -v-1H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)" --arg e "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '{id:$id,r:{start:$s,end:$e}}')")
echo "  streamAnalyticsSummary: $(echo "$S" | jq -c '.data.analytics.usage.streaming.streamAnalyticsSummary // .errors')"
echo "$S" | jq -e '.data.analytics.usage.streaming.streamAnalyticsSummary.rangeTotalSessions > 0' >/dev/null 2>&1 && ok "summary shows sessions" || bad "summary shows no sessions"

log "6/6 internal reads are not audience: the clip's source read left no viewer session"
if [ -n "${CLIP_HASH:-}" ]; then
  n=$(ch "SELECT count() FROM periscope.viewer_connection_events WHERE timestamp > now() - INTERVAL 30 MINUTE AND connector = 'EBML'")
  [ "${n:-1}" = 0 ] && ok "no EBML (processing source read) viewer connection recorded" || bad "$n EBML viewer connection(s) recorded from processing reads"
fi

echo; echo "=============================="; echo "PASS=$PASS FAIL=$FAIL"
[ "$FAIL" = 0 ]
