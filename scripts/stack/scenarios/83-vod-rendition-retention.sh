#!/usr/bin/env bash
# stack-scenario: default
# tenant: qa
# staging: unsafe, stops the Livepeer gateway
# An uploaded original video track that already fits a ladder rung is that
# rung: no process produces it, nothing bills it, it stays visible. Fit means
# H264 or HEVC, short side within max(32, rung/20) of the rung and bitrate
# within 0.5x-2x of the rung target (360p 900k, 480p 1600k, 720p 4500k, 1080p
# 7800k). A non-fitting original is masked and its rung transcoded from the
# highest-bitrate original. Every rung appears exactly once in HLS.
. "$(dirname "$0")/../lib.sh"
need ffmpeg ffprobe jq curl python3 || finish
GATEWAY=${STACK_LIVEPEER_GATEWAY_A:-livepeer-gateway-a}
GATEWAY_STOPPED=''
trap '[ -z "$GATEWAY_STOPPED" ] || stack_ctl start "$GATEWAY" >/dev/null 2>&1' EXIT
SINCE=$(date +%s)
SECONDS_LONG=12

# render_renditions <out> <WxH:codec:kbps>...: one MP4 with a constant-bitrate
# video track per spec, all scaled from one test pattern, and one AAC track.
render_renditions() {
  local out=$1 graph i=0 spec size codec kbps maps=() codecs=()
  shift
  graph="[0:v]split=$#"
  for spec in "$@"; do graph+="[s$i]"; i=$((i + 1)); done
  i=0
  for spec in "$@"; do
    IFS=: read -r size codec kbps <<<"$spec"
    graph+=";[s$i]scale=${size/x/:},setsar=1[v$i]"
    maps+=(-map "[v$i]")
    case "$codec" in
      h264) codecs+=("-c:v:$i" libx264 "-x264-params:v:$i" 'nal-hrd=cbr:keyint=20:min-keyint=20:scenecut=0') ;;
      hevc) codecs+=("-c:v:$i" libx265 "-tag:v:$i" hvc1 "-x265-params:v:$i" 'keyint=20:min-keyint=20:scenecut=0:log-level=error') ;;
      *) return 1 ;;
    esac
    codecs+=("-b:v:$i" "${kbps}k" "-minrate:v:$i" "${kbps}k" "-maxrate:v:$i" "${kbps}k" "-bufsize:v:$i" "$((kbps * 2))k")
    i=$((i + 1))
  done
  ffmpeg -hide_banner -loglevel error -y -f lavfi -i 'testsrc2=size=1920x1080:rate=10' \
    -f lavfi -i 'sine=frequency=440:sample_rate=48000' -t "$SECONDS_LONG" -filter_complex "$graph" \
    "${maps[@]}" -map 1:a "${codecs[@]}" -preset ultrafast -pix_fmt yuv420p -bf 0 -c:a aac -b:a 96k \
    -movflags +faststart "$out"
}
ready_or_failed() {
  local status
  status=$(vod_status "$1")
  echo "    status: $status"
  [ "$status" = READY ] && return 0
  [ "$status" = FAILED ] && return 2
  return 1
}
artifact_tracks() {
  local database
  for database in "$FOGHORN_A_DB" "$FOGHORN_B_DB"; do
    pg "$database" "SELECT tracks::text FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1' AND NOT federated_pointer AND tracks IS NOT NULL" | grep -v '^$' && return 0
  done
  return 1
}
# recorded_ladder <hash> <retained rungs> <transcoded rungs> [retained hevc rung]:
# the stored VOD has one video track per rung; the retained rungs are original
# tracks (no source_track lineage), the others are process outputs.
recorded_ladder() {
  local tracks
  tracks=$(artifact_tracks "$1") || return 1
  echo "    video tracks: $(echo "$tracks" | jq -c '[.[] | select((.track_type // .trackType // .type)=="video") | {codec, w:.width, h:.height, kbps:(.bitrate_kbps // .bitrateKbps), src:(.source_track // .sourceTrack // "")}]')"
  echo "$tracks" | jq -e --arg retained "$2" --arg transcoded "$3" --arg hevc "${4:-}" '
    def rung($size): [360,480,720,1080][] as $r | select(($size - $r | fabs) <= ([32, $r/20] | max)) | $r;
    def rungs($list): $list | split(",") | map(select(. != "") | tonumber) | sort;
    [.[] | select((.track_type // .trackType // .type)=="video" and (.codec|ascii_downcase)!="jpeg")
      | {short:([.width,.height]|min), codec:(.codec|ascii_downcase), derived:((.source_track // .sourceTrack // "") != "")}] as $videos |
    ($videos | length) == (rungs($retained) + rungs($transcoded) | length) and
    ([$videos[] | select(.derived | not) | [rung(.short)][0]] | sort) == rungs($retained) and
    ([$videos[] | select(.derived) | [rung(.short)][0]] | sort) == rungs($transcoded) and
    (if $hevc == "" then true else any($videos[]; (.derived | not) and .codec=="hevc" and rung(.short)==($hevc|tonumber)) end)' >/dev/null
}
# hls_ladder <want hevc rung or ""> <foghorn> <playback id>: the master playlist
# lists each rung exactly once and nothing else.
hls_ladder() {
  local url
  url=$(location_at "$2" "$3")
  [ -n "$url" ] || return 1
  python3 - "$url" "$1" <<'PY'
import re,sys,urllib.request
with urllib.request.urlopen(sys.argv[1],timeout=20) as response:
    master=response.read().decode()
variants=[]
for line in master.splitlines():
    if line.startswith('#EXT-X-STREAM-INF'):
        size=re.search(r'RESOLUTION=(\d+)x(\d+)',line)
        codecs=re.search(r'CODECS="([^"]*)"',line)
        variants.append((min(map(int,size.groups())) if size else 0,codecs.group(1) if codecs else ''))
print('    HLS variants:',variants)
def rung(short):
    return next((r for r in (360,480,720,1080) if abs(short-r)<=max(32,r//20)),None)
rungs=sorted(rung(short) for short,_ in variants)
if rungs!=[360,480,720,1080]:
    raise SystemExit(f'expected exactly one variant per rung 360/480/720/1080, got {variants}')
if sys.argv[2]:
    codecs=next(c for short,c in variants if rung(short)==int(sys.argv[2]))
    if not re.search(r'\b(hvc1|hev1)',codecs):
        raise SystemExit(f'rung {sys.argv[2]} is not the retained HEVC original: {codecs}')
PY
}
# billed_rungs <hash>: the rungs Livepeer billed for the artifact ("480,720,"),
# from the rendition names of its segment samples.
billed_rungs() {
  ch "SELECT renditions_json FROM periscope.processing_segments_final_v
      WHERE tenant_id='$STACK_TENANT_ID' AND position(stream_name,'$1')>0 AND process_type='Livepeer'
        AND media_seconds>0 AND billable_at_ms >= $((SINCE * 1000)) FORMAT JSONEachRow" |
    jq -r '.renditions_json | fromjson? // [] | .[]? | .name // "" | capture("^(?<h>[0-9]+)p$").h' | sort -un | tr '\n' ','
}
# billing_matches <hash> <transcoded rungs>: Livepeer billed exactly the
# transcoded rungs.
billing_matches() {
  local billed want
  billed=$(billed_rungs "$1")
  want=$(echo "$2" | tr ',' '\n' | grep . | sort -n | tr '\n' ',')
  echo "    billed rungs: ${billed:-<none>} (want $want)"
  [ -n "$billed" ] && [ "$billed" = "$want" ]
}
full_length_upload() { full_length "$1" "$2" "$SECONDS_LONG"; }
gateway_state() {
  pg quartermaster "SELECT COALESCE(health_status,'') FROM quartermaster.service_instances WHERE instance_id='stack-livepeer-gateway-a'"
}
gateway_absent() { [ "$(gateway_state)" != healthy ] && [ "$(date +%s)" -ge "$DISCOVERY_DRAIN_AT" ]; }
gateway_present() { [ "$(gateway_state)" = healthy ]; }

work="$STACK_STATE_DIR/rendition-retention"
mkdir -p "$work"
# name | renditions | retained rungs | transcoded rungs | retained HEVC rung
cases=(
  'in-band|1920x1080:h264:7800 1280x720:h264:4500 640x360:h264:900|360,720,1080|480|'
  'whack-720|1920x1080:h264:7800 1280x720:h264:15000 640x360:h264:900|360,1080|480,720|'
  'hevc-720|1920x1080:h264:7800 1280x720:hevc:4500|720,1080|360,480|720'
  'single-in-band|1920x1080:h264:7800|1080|360,480,720|'
  'single-out-of-band|1920x1080:h264:24000||360,480,720,1080|'
)
for entry in "${cases[@]}"; do
  IFS='|' read -r name specs _ _ _ <<<"$entry"
  # shellcheck disable=SC2086
  render_renditions "$work/$name.mp4" $specs || { fail "render $name"; finish; }
done

eventually 90 'cell A Livepeer gateway discoverable' gateway_discoverable stack-livepeer-gateway-a || finish
run_case() { # run_case <path> <case entry>
  local path=$1 name specs retained transcoded hevc result id hash playback processes
  IFS='|' read -r name specs retained transcoded hevc <<<"$2"
  log "$path: $name ($specs)"
  result=$(upload_vod "$work/$name.mp4" "stack rendition retention $path $name" '{"keepOriginal":false}')
  id=$(echo "$result" | jq -r '.id // empty')
  hash=$(echo "$result" | jq -r '.artifactHash // empty')
  playback=$(echo "$result" | jq -r '.playbackId // empty')
  [ -n "$id" ] || { fail "$path/$name upload: $result"; return; }
  eventually 900 "$path/$name reaches READY" ready_or_failed "$id" || return
  processes=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(processes_json,'') FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash' ORDER BY created_at DESC LIMIT 1")
  if [ "$path" = livepeer ]; then
    check "$path/$name processed through Livepeer" json_has "$processes" 'any(.[]; .process=="Livepeer")'
  else
    check "$path/$name processed by local AV" json_has "$processes" 'any(.[]; .process=="AV") and all(.[]; .process!="Livepeer")'
  fi
  check "$path/$name stores originals {$retained} and transcodes {$transcoded}" recorded_ladder "$hash" "$retained" "$transcoded" "$hevc"
  every_replica "$path/$name HLS has each rung exactly once" "$playback" hls_ladder "$hevc"
  every_replica "$path/$name spans the full source" "$playback" full_length_upload
  # Local AV samples carry no output geometry; Livepeer's name each rendition.
  if [ "$path" = livepeer ]; then
    eventually 120 "$path/$name bills only the transcoded rungs {$transcoded}" billing_matches "$hash" "$transcoded"
  fi
}
for entry in "${cases[@]}"; do run_case livepeer "$entry"; done

# The local AV path applies the same rule when no gateway exists.
stack_ctl stop "$GATEWAY" || { fail 'stop gateway'; finish; }
GATEWAY_STOPPED=1
# Positive discovery is cached for 60 seconds; health and cache must both drain.
DISCOVERY_DRAIN_AT=$(($(date +%s) + 65))
eventually 180 'gateway undiscoverable before local uploads' gateway_absent || finish
run_case local "${cases[0]}"
run_case local "${cases[1]}"
stack_ctl start "$GATEWAY" || fail 'restart gateway'
GATEWAY_STOPPED=''
eventually 120 'gateway healthy again' gateway_present
finish
