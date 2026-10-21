#!/usr/bin/env bash
# Unit checks for the post-run sweep's evaluators (scripts/stack/sweep.py)
# against fixtures shaped like what the stack and staging produced, so they need
# no stack. Each defect fixture is the shape a real defect left behind.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/fw-stack-sweep-test.XXXXXX")
trap 'rm -rf "$work"' EXIT

failures=0
expect() { # expect <description> <command...>
  local what=$1
  shift
  if "$@"; then printf 'ok    %s\n' "$what"; else printf 'FAIL  %s\n' "$what"; failures=$((failures + 1)); fi
}
sweep() { python3 "$here/sweep.py" "$@" >"$work/out" 2>&1; }
passes() { sweep "$@"; }
fails() { ! sweep "$@"; }
said() { grep -qE "$1" "$work/out"; }

# vtt <file> <first start> <last start> <step> [skip from] [skip to]: cues of
# <step> seconds from first to last start, leaving out starts in [skip from, skip to).
vtt() {
  python3 - "$@" <<'PY'
import sys
path, first, last, step = sys.argv[1], float(sys.argv[2]), float(sys.argv[3]), float(sys.argv[4])
skip = (float(sys.argv[5]), float(sys.argv[6])) if len(sys.argv) > 6 else (-1, -1)
def ts(s):
    return f"{int(s // 3600):02d}:{int(s % 3600 // 60):02d}:{s % 60:06.3f}"
out, t = ["WEBVTT", ""], first
while t <= last + 1e-6:
    if not (skip[0] <= t < skip[1]):
        out += [f"{ts(t)} --> {ts(t + step)}", f"sprite.jpg#xywh=0,0,160,90", ""]
    t += step
open(path, "w").write("\n".join(out))
PY
}

# ---- thumbnails -------------------------------------------------------------
vtt "$work/full.vtt" 0.08 43.08 2
expect "a sprite covering a 45 s VOD from its start passes" passes vtt 45.013 "$work/full.vtt"
# Mist process_thumbs pruned a processed VOD's cache to the buffer window: the
# 180 s staging VOD's sprite started at 126 s.
vtt "$work/pruned.vtt" 126.066 176.1 2
expect "a sprite of a 180 s VOD starting at 126 s fails (process_thumbs prune)" fails vtt 180.013 "$work/pruned.vtt"
expect "  ...naming the late first cue" said 'first cue starts at 126'
# The 65 s staging DVR chapter's sprite started at 8.1 s with 2 s cues.
vtt "$work/chapter.vtt" 8.139 61.866 2
expect "a 65 s chapter sprite starting at 8.1 s fails" fails vtt 65.301 "$work/chapter.vtt"
vtt "$work/short.vtt" 0.1 40 2
expect "a sprite ending 25 s before the end fails" fails vtt 65 "$work/short.vtt"
vtt "$work/hole.vtt" 0 118 2 40 60
expect "a sprite with a 20 s hole fails" fails vtt 120 "$work/hole.vtt"
expect "  ...naming the hole" said 'hole between cues'
vtt "$work/long.vtt" 0 1472 28
expect "a 1500 s chapter sprite with 28 s cues from 0 passes" passes vtt 1499.8 "$work/long.vtt"
printf 'WEBVTT\n\n' >"$work/empty.vtt"
expect "a sprite without cues fails" fails vtt 30 "$work/empty.vtt"

# ---- DVR chapters -------------------------------------------------------------
chapter() { # chapter <id> <start s> <end s> <state> [failure reason] [segments]
  jq -cn --arg id "$1" --argjson s "$2" --argjson e "$3" --arg st "$4" --arg why "${5:-}" --argjson n "${6:-10}" \
    '{chapterId:$id, startMs:($s*1000), endMs:($e*1000), state:$st, segmentCount:$n, hasGaps:false,
      playbackId:(if ($st|startswith("FAILED")) then null else "pb\($id)" end),
      lastFailureReason:(if $why == "" then null else $why end)}'
}
chapters() { # chapters <file> <recording start s> <recording end s> <chapter json...>
  local f=$1 start=$2 end=$3
  shift 3
  printf '%s\n' "$@" | jq -s --argjson s "$start" --argjson e "$end" '{recording:{startMs:($s*1000), endMs:($e*1000)}, chapters:.}' >"$f"
}
chapters "$work/ok.json" 1000 1210 "$(chapter a 1000 1120 FROZEN)" "$(chapter b 1120 1210 RECLAIMED)"
expect "two contiguous frozen chapters covering the recording pass" passes chapters "$work/ok.json"
# The staging recording's tail: the writer outlived the publisher past a
# boundary and the empty chapter after it failed as source-missing.
chapters "$work/trailing.json" 1000 1250 "$(chapter a 1000 1120 FROZEN)" "$(chapter b 1120 1240 FROZEN)" \
  "$(chapter c 1240 1250 FAILED_SOURCE_MISSING 'chapter range has no segments' 0)"
expect "an empty trailing chapter FAILED_SOURCE_MISSING fails" fails chapters "$work/trailing.json"
expect "  ...naming the state and reason" said 'FAILED_SOURCE_MISSING: chapter range has no segments'
chapters "$work/gap.json" 1000 1240 "$(chapter a 1000 1120 FROZEN)" "$(chapter b 1130 1240 FROZEN)"
expect "a 10 s gap between chapters fails" fails chapters "$work/gap.json"
chapters "$work/late.json" 1000 1240 "$(chapter a 1060 1120 FROZEN)" "$(chapter b 1120 1240 FROZEN)"
expect "chapters starting 60 s after the recording fail" fails chapters "$work/late.json"
chapters "$work/short.json" 1000 1400 "$(chapter a 1000 1120 FROZEN)" "$(chapter b 1120 1240 FROZEN)"
expect "chapters ending 160 s before the recording fail" fails chapters "$work/short.json"
chapters "$work/stuck.json" 1000 1240 "$(chapter a 1000 1120 FROZEN)" "$(chapter b 1120 1240 FINALIZING)"
expect "a chapter still FINALIZING after the wait fails" fails chapters "$work/stuck.json"
chapters "$work/none.json" 1000 1240
expect "an ended recording without chapters fails" fails chapters "$work/none.json"

# ---- analytics completeness -------------------------------------------------
cat >"$work/analytics-ok.json" <<'JSON'
{"events": [
  {"stream": "s1", "name": "live+one", "type": "stream_start", "ts": 100},
  {"stream": "s1", "name": "live+one", "type": "stream_end", "ts": 400},
  {"stream": "s2", "name": "two", "type": "stream_start", "ts": 100},
  {"stream": "s2", "name": "two", "type": "stream_end", "ts": 200},
  {"stream": "s2", "name": "two", "type": "stream_start", "ts": 210},
  {"stream": "s2", "name": "two", "type": "stream_end", "ts": 300}],
 "finals": [{"stream": "s1", "started_ms": 100000, "ended_ms": 400000},
            {"stream": "s2", "started_ms": 210000, "ended_ms": 300000}],
 "sessions": [{"name": "live+one", "started": 100, "ended": 395}, {"name": "two", "started": 210, "ended": 299}]}
JSON
expect "streams with an end and a final row after their last start pass" passes analytics "$work/analytics-ok.json"
# The staging race: the offline effect lost the revision CAS to the vanish and
# its STREAM_END never reached Decklog (1 start, 0 ends, no final row).
jq '.events |= map(select(.stream != "s1" or .type != "stream_end")) | .finals |= map(select(.stream != "s1"))' \
  "$work/analytics-ok.json" >"$work/analytics-noend.json"
expect "a stream without stream_end or final row fails" fails analytics "$work/analytics-noend.json"
expect "  ...naming both missing facts" bash -c "grep -q 'no stream_end' '$work/out' && grep -q 'no stream_sessions_final' '$work/out'"
jq '.events |= map(select(.stream != "s2" or .ts != 300))' "$work/analytics-ok.json" >"$work/analytics-republish.json"
expect "a republished stream whose second generation has no end fails" fails analytics "$work/analytics-republish.json"
jq '.sessions[0].ended = 0 | .events |= map(select(.stream != "s1" or .type != "stream_end")) | .finals |= map(select(.stream != "s1"))' \
  "$work/analytics-ok.json" >"$work/analytics-live.json"
expect "a stream whose generation is still open is not judged" passes analytics "$work/analytics-live.json"
jq '.sessions += [{"name": "live+three", "started": 150, "ended": 160}]' "$work/analytics-ok.json" >"$work/analytics-nostart.json"
expect "a generation Foghorn admitted without stream_start in ClickHouse fails" fails analytics "$work/analytics-nostart.json"
jq '.sessions = null' "$work/analytics-noend.json" >"$work/analytics-chonly.json"
expect "without Foghorn sessions the ClickHouse-only check still fails a missing end" fails analytics "$work/analytics-chonly.json"

# ---- stream runtime ledger ----------------------------------------------------
cat >"$work/runtime-ok.json" <<'JSON'
{"finals": [{"stream": "s1", "started_ms": 1000000, "ended_ms": 1934000, "projected_ms": 1935000},
            {"stream": "s2", "started_ms": 1000000, "ended_ms": 1133400, "projected_ms": 1134000},
            {"stream": "s3", "started_ms": 1000000, "ended_ms": 1100000, "projected_ms": 9000000}],
 "runtime": {"s1": {"seconds": 934, "windows": 5, "keys": 1},
             "s2": {"seconds": 132, "windows": 2, "keys": 1},
             "s3": {"seconds": 0, "windows": 0, "keys": 0}},
 "processed_ms": 5000000, "live": []}
JSON
expect "ended streams whose ledger runtime matches their final sessions pass" passes runtime "$work/runtime-ok.json"
# The staging defect: the live branch keyed the session from Mist's started_at,
# one second off the final row's event-log start, so both sets of rows summed.
jq '.runtime.s1 = {"seconds": 1418, "windows": 5, "keys": 2}' "$work/runtime-ok.json" >"$work/runtime-double.json"
expect "a session counted under two keys fails" fails runtime "$work/runtime-double.json"
expect "  ...naming both keys and both durations" said 's1: .*1418s under 2 session key.*ran 934s'
jq '.runtime.s1.seconds = 600' "$work/runtime-ok.json" >"$work/runtime-short.json"
expect "a ledger missing part of a session fails" fails runtime "$work/runtime-short.json"
jq '.runtime.s2.seconds = 900 | .live = ["s2"]' "$work/runtime-ok.json" >"$work/runtime-live.json"
expect "a stream live again is not judged" passes runtime "$work/runtime-live.json"

# ---- renditions -----------------------------------------------------------------
renditions() { # renditions <file> <source WxH...>: the stack's Livepeer ladder over the given video tracks
  local f=$1
  shift
  printf '%s\n' "$@" | jq -R 'split("x") | {type:"video", codec:"H264", width:(.[0]|tonumber), height:(.[1]|tonumber)}' |
    jq -s '{processes:[{process:"Livepeer", target_profiles:[{height:360},{height:480},{height:720},{height:1080}]},
                       {process:"Thumbs"}],
            tracks:(. + [{type:"video", codec:"JPEG", width:160, height:90}, {type:"audio", codec:"AAC"}])}' >"$f"
}
renditions "$work/r-ok.json" 854x480 642x360
expect "a 480p upload with its 360p rendition passes" passes renditions "$work/r-ok.json"
renditions "$work/r-missing.json" 854x480
expect "a 480p upload that owed a 360p rendition and has none fails" fails renditions "$work/r-missing.json"
expect "  ...naming the owed rendition" said 'owed renditions at 360p below the 480p source'
renditions "$work/r-360.json" 640x360
expect "a 360p upload owes no rendition (no upscale) and passes alone" passes renditions "$work/r-360.json"

# ---- doctor -------------------------------------------------------------------
cat >"$work/doctor.log" <<'LOG'
[OK] ClickHouse          : Local ClickHouse query OK (latency: 819.457ms)
[FAIL] Host crashes        : 1 finding(s) since 24 hours ago: fw-stg-edge-eu: core dumps: MistInBuffer x1
[FAIL] Listeners on fw-stg-edge-eu (edge): 1 listener(s) bind every interface outside the edge's public set: udp [::ffff:0.0.0.0]:8888 (MistInTSSRT)
[WARN] Edge config version : 2/2 edge(s) run config older than this CLI
  infrastructure + services:  [FAIL] 129/134 healthy
LOG
: >"$work/doctor-none.tsv"
expect "doctor FAIL lines fail without an allowlist" fails doctor "$work/doctor.log" "$work/doctor-none.tsv"
expect "  ...and the summary line is not counted as a check" bash -c "[ \"\$(grep -c '^FAIL' '$work/out')\" = 2 ]"
printf '%s\t%s\n' '^Listeners on fw-stg-edge-eu .*:8888 \(MistInTSSRT\)$' 'test reason' >"$work/doctor-one.tsv"
expect "one allowlisted FAIL leaves the other failing" fails doctor "$work/doctor.log" "$work/doctor-one.tsv"
printf '%s\t%s\n' '^Host crashes .*MistInBuffer' 'test reason' >>"$work/doctor-one.tsv"
expect "every FAIL allowlisted passes" passes doctor "$work/doctor.log" "$work/doctor-one.tsv"

# ---- edge listeners -------------------------------------------------------------
# /proc/net rows: wildcard RTMP 1935 and the stack's Helmsman 18007 listening;
# an SRT UDP socket on 8888 bound to every interface (the staging finding); a
# loopback-only Mist HTTP 4242; WebRTC's outgoing UDP socket in the ephemeral range.
cat >"$work/listeners.json" <<'JSON'
{"proc": {
  "tcp": "  sl  local_address rem_address   st\n   0: 00000000:078F 00000000:0000 0A\n   1: 0100007F:1092 00000000:0000 0A\n   2: 00000000:4667 00000000:0000 0A\n   3: 0A000002:078F 0A000003:D431 01\n",
  "udp": "  sl  local_address rem_address   st\n   0: 00000000:22B8 00000000:0000 07\n   1: 00000000:A000 00000000:0000 07\n",
  "tcp6": "", "udp6": ""},
 "public": {"tcp": [22, 80, 443, 1935], "udp": [443, 8889], "udp_range": [32768, 60999]}}
JSON
expect "a wildcard SRT socket on 8888 and an internal TCP port outside the public set fail" fails listeners "$work/listeners.json"
expect "  ...naming exactly those two" bash -c "[ \"\$(sort '$work/out' | tr '\n' ' ')\" = 'tcp 18023 udp 8888 ' ]"
jq '.public.tcp += [18023] | .public.udp += [8888]' "$work/listeners.json" >"$work/listeners-ok.json"
expect "the same listeners inside the public set pass" passes listeners "$work/listeners-ok.json"

# ---- allowlist narrowness (Report) ------------------------------------------------
narrow() {
  python3 - "$here" <<'PY'
import io, sys, contextlib
sys.path.insert(0, sys.argv[1])
import sweep
allow = [("03-livepeer-vod", "artifacts", "vod abc", "^status failed", "missing source")]
r = sweep.Report(allow, [("03-livepeer-vod", 100, 200), ("04-recording", 200, 300)])
buf = io.StringIO()
with contextlib.redirect_stdout(buf):
    ok1 = r.check("artifacts", "vod abc", ["status failed (source unavailable)"], at=150)
    ok2 = r.check("artifacts", "vod abd", ["status failed"], at=250)
    ok3 = r.check("api", "vod abc", ["node(id) -> null"], at=150)
    ok4 = r.check("artifacts", "vod abc", ["first cue starts at 12.0s"], at=150)
out = buf.getvalue()
assert ok1 and not ok2 and not ok3 and not ok4, out
assert "(04-recording)" in out and "allowlisted by 03-livepeer-vod: missing source" in out, out
assert r.scenario_of(350) == "after 04-recording" and r.scenario_of(50) == "bring-up"
PY
}
expect "an allowlist entry explains only its invariant, entity and detail; failures name the scenario" narrow

if [ "$failures" -gt 0 ]; then
  echo "sweep checks: $failures failed"
  exit 1
fi
echo "sweep checks: ok"
