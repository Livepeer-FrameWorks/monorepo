#!/usr/bin/env bash
# stack-scenario: default
# A viewer burst over many live streams is answered from each cell's local
# state. STACK_BURST_STREAMS streams (default 80) publish, half into each cell.
# Then two bursts, each spread over every stream and every Foghorn replica of
# both cells:
#   herd: STACK_BURST_PLAYS /play requests (default 5,000) at once, together
#     with STACK_BURST_USER_NEW synthetic Mist USER_NEW triggers per edge
#     (default 500) for the streams that edge ingests. It asks for more viewers
#     than the stack's two edges hold, so part of it is refused for capacity.
#   fit: STACK_BURST_FIT_PLAYS /play requests (default 600) at once, after the
#     herd's unused reservations lapsed, within the edges' capacity.
# And a third concentrated on two streams, one ingested in each cell:
#   hot: STACK_BURST_HOT_PLAYS /play requests (default 600) at once, after the
#     fit burst's reservations lapsed, each sent to a replica of the cell that
#     does not ingest the stream, so every viewer of a stream is handed the one
#     cross-cell pull into its arrival cell's edge. Within the edges' capacity.
# Measures, as Foghorn /metrics deltas summed over the replicas, for each burst:
#   media_request_central_rpcs_total for Quartermaster is 0 on every path, and
#   for Commodore is 0 on every viewer path; placement_pair_reads_total misses
#   stay within streams x replicas x seconds (a replica reads a pair at most
#   once per second). Every herd /play is answered by the cell (307, or 503
#   with Retry-After); every fit and hot /play redirects to media, every hot
#   viewer to its arrival cell's edge, and no Foghorn replica runs out of
#   retries writing a stream's registry record ("replication mutation
#   contention") during the hot burst.
# Catches: a per-request Quartermaster inventory read or Commodore call on the
# viewer path, pair reads that are not shared across a burst, viewers refused
# while capacity remains, and a herd on one stream refused because every
# viewer's preparation rewrites that stream's shared pull record.
# Sizing: every live stream runs a thumbnail process on its edge, and the
# stack's cell-B edge (1.5 GiB) runs out of memory well before 100 streams, so
# the default is 80. Every prepared viewer reserves an estimated 1 Mbit/s on
# its edge for 30 s, which is what bounds the herd. The publishers loop a
# pre-encoded 160x90 video-only file with -c copy, so the runner encodes
# nothing and the tier's Livepeer ladder (inhibited below 640x360) does not
# transcode.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl psql python3 || finish

N=${STACK_BURST_STREAMS:-80}
PLAYS=${STACK_BURST_PLAYS:-5000}
USER_NEWS=${STACK_BURST_USER_NEW:-500}
FIT_PLAYS=${STACK_BURST_FIT_PLAYS:-600}
HOT_PLAYS=${STACK_BURST_HOT_PLAYS:-600}
CONCURRENCY=${STACK_BURST_CONCURRENCY:-1000}
RUN="burst-$(date +%s)"
SRC="$STACK_STATE_DIR/$RUN.mp4"
WORK="$STACK_STATE_DIR/$RUN"
mkdir -p "$WORK"
read -r -a REPLICAS <<<"$FOGHORN_A_URLS $FOGHORN_B_URLS"

# paced <command...>: the API call, repeated while the gateway's per-token rate
# limit refuses it (every stream is created and deleted through one token).
paced() {
  local out
  for _ in $(seq 1 60); do
    out=$("$@")
    case "$out" in *rate_limit_exceeded*) sleep 1 ;; *) printf '%s\n' "$out"; return 0 ;; esac
  done
  printf '%s\n' "$out"
}
delete_paced() { paced gql 'mutation($id:ID!){deleteStream(id:$id){__typename}}' "$(jq -cn --arg id "$1" '{id:$id}')" >/dev/null; }
export -f paced delete_paced

SIDS=() LOOPS=()
cleanup() {
  local pid
  for pid in "${LOOPS[@]}"; do kill "$pid" 2>/dev/null; done
  pkill -f "$SRC" 2>/dev/null
  printf '%s\n' "${SIDS[@]}" | xargs -r -P 4 -I{} bash -c '. "$1"; delete_paced "$2"' _ "$STACK_LIB_DIR/lib.sh" {}
  rm -f "$SRC"
}
trap cleanup EXIT

ffmpeg -hide_banner -loglevel error -y -f lavfi -i 'testsrc2=size=160x90:rate=10' -t 20 \
  -c:v libx264 -preset veryfast -g 20 -pix_fmt yuv420p -an -movflags +faststart "$SRC" ||
  { fail "render the publisher source"; finish; }

log "create $N streams"
seq 1 "$N" | xargs -P 4 -I{} bash -c '. "$1"; paced create_stream "$2-{}" false' _ "$STACK_LIB_DIR/lib.sh" "stack-$RUN" \
  >"$WORK/created.jsonl"
while IFS= read -r sid; do SIDS+=("$sid"); done < <(jq -r '.id // empty' "$WORK/created.jsonl")
check "created $N streams (got ${#SIDS[@]})" [ "${#SIDS[@]}" -eq "$N" ]
[ "${#SIDS[@]}" -eq "$N" ] || finish

# One line per stream, "<playback id> <stream key> <internal name> <cell a|b>":
# odd streams publish into cell A, even ones into cell B.
pg commodore "SELECT s.playback_id, s.stream_key, s.internal_name FROM commodore.streams s
    WHERE s.tenant_id='$STACK_TENANT_ID' AND s.title LIKE 'stack-$RUN-%' ORDER BY s.title" |
  awk -F'|' '{ print $1, $2, $3, (NR % 2 ? "a" : "b") }' >"$WORK/streams"
check "Commodore lists the $N streams" [ "$(wc -l <"$WORK/streams" | tr -d ' ')" -eq "$N" ]

log "start $N looping publishers"
while read -r _ key _ cell; do
  rtmp=$EDGE_A_RTMP
  [ "$cell" = b ] && rtmp=$EDGE_B_RTMP
  # A refused or dropped push is retried, as an encoder does.
  (while :; do
    ffmpeg -nostdin -hide_banner -loglevel error -re -stream_loop -1 -i "$SRC" -c copy -f flv "$rtmp/$key" </dev/null >/dev/null 2>&1
    sleep 2
  done) &
  LOOPS+=("$!")
done <"$WORK/streams"

# admitted <cell> <foghorn db>: how many of the cell's streams have an open,
# projected ingest session.
admitted() {
  local names
  names=$(awk -v c="$1" '$4 == c { printf "%s'\''%s'\'','\''live+%s'\''", (n++ ? "," : ""), $3, $3 }' "$WORK/streams")
  pg "$2" "SELECT count(DISTINCT regexp_replace(stream_internal_name, '^live\\+', '')) FROM foghorn.ingest_sessions
    WHERE tenant_id='$STACK_TENANT_ID' AND ended_at IS NULL AND projection_state='active'
      AND stream_internal_name IN ($names)"
}
all_admitted() {
  local a b
  a=$(admitted a "$FOGHORN_A_DB") b=$(admitted b "$FOGHORN_B_DB")
  printf '    admitted: cell A %s, cell B %s of %s\n' "${a:-?}" "${b:-?}" "$N"
  [ "$((${a:-0} + ${b:-0}))" -ge "$N" ]
}
eventually 300 "all $N publishers are admitted" all_admitted || finish

# The burst driver. "probe" resolves every stream once through its own cell's
# first replica; "burst" sends the /play requests and per-edge USER_NEW
# triggers at once; "user_end" ends the synthetic sessions.
cat >"$WORK/burst.py" <<'PY'
import collections, concurrent.futures, http.client, json, sys, time, urllib.parse, uuid

mode, streams_file, out = sys.argv[1], sys.argv[2], sys.argv[3]
replicas = sys.argv[4].split()
plays, user_news, concurrency = int(sys.argv[5]), int(sys.argv[6]), int(sys.argv[7])
streams = [l.split() for l in open(streams_file) if l.strip()]
edges = {"a": "edge", "b": "edge-b"}
cell_replicas = {"a": replicas[:2], "b": replicas[2:]}

def request(base, method, path, body=None):
    u = urllib.parse.urlsplit(base)
    conn = http.client.HTTPConnection(u.hostname, u.port or 80, timeout=20)
    started = time.monotonic()
    try:
        conn.request(method, path, body=body)
        resp = conn.getresponse()
        data = resp.read()
        headers = {"location": resp.getheader("Location") or "", "retry": resp.getheader("Retry-After") or ""}
        return resp.status, headers, data.decode("utf-8", "replace").strip(), time.monotonic() - started
    except Exception as exc:
        return 0, {}, type(exc).__name__, time.monotonic() - started
    finally:
        conn.close()

# Each result is (kind, code, ok, answered, seconds, where): ok is the wanted
# outcome; answered is a decision by the cell (a redirect, or a 503 with
# Retry-After) rather than a transport failure or another status.
def play(i):
    pb = streams[i % len(streams)][0]
    base = replicas[i % len(replicas)]
    code, h, _, took = request(base, "GET", f"/play/{pb}/hls")
    ok = code in (302, 307) and bool(h.get("location"))
    return ("play", code, ok, ok or (code == 503 and bool(h.get("retry"))), took, base)

# A viewer of stream i % len(streams) arriving at a replica of the other cell.
# where is "local:" when the redirect names the arrival cell's own edge.
def play_cross(i):
    pb, _, _, cell = streams[i % len(streams)]
    arrival_cell = "b" if cell == "a" else "a"
    arrival = cell_replicas[arrival_cell]
    base = arrival[(i // len(streams)) % len(arrival)]
    code, h, _, took = request(base, "GET", f"/play/{pb}/hls")
    ok = code in (302, 307) and bool(h.get("location"))
    host = urllib.parse.urlsplit(h.get("location", "")).hostname or ""
    where = f"local:{base}" if ok and host == edges[arrival_cell] else f"{base} -> {host}"
    return ("play", code, ok, ok or (code == 503 and bool(h.get("retry"))), took, where)

def probe(pb, cell):
    code, h, _, took = request(cell_replicas[cell][0], "GET", f"/play/{pb}/hls")
    ok = code in (302, 307) and bool(h.get("location"))
    return ("probe", code, ok, ok, took, pb)

def session(cell, j):
    return f"burst-{cell}-{j}"

def viewer_ip(j):
    return f"10.99.{j // 250}.{j % 250 + 1}"

def user_new(cell, j):
    mine = [s for s in streams if s[3] == cell]
    name = f"live+{mine[j % len(mine)][2]}"
    host = edges[cell]
    body = "\n".join([name, viewer_ip(j), uuid.uuid4().hex, "HLS",
                      f"http://{host}:8080/hls/{name}/index.m3u8", session(cell, j), "", "", ""])
    code, _, text, took = request(f"http://{host}:18007", "POST", "/webhooks/mist/user_new", body=body.encode())
    return ("user_new", code, code == 200 and text == "true", code == 200 and text in ("true", "false"), took, f"{host}:{text[:20]}")

def user_end(cell, j):
    mine = [s for s in streams if s[3] == cell]
    host = edges[cell]
    body = "\n".join([session(cell, j), f"live+{mine[j % len(mine)][2]}", "HLS", viewer_ip(j), "5", "0", "0", ""])
    code, _, text, took = request(f"http://{host}:18007", "POST", "/webhooks/mist/user_end", body=body.encode())
    return ("user_end", code, code == 200, code == 200, took, f"{host}:{text[:40]}")

jobs = []
if mode == "probe":
    jobs = [lambda pb=pb, cell=cell: probe(pb, cell) for pb, _, _, cell in streams]
elif mode == "burst":
    jobs = [lambda i=i: play(i) for i in range(plays)]
    for cell in edges:
        jobs += [lambda cell=cell, j=j: user_new(cell, j) for j in range(user_news)]
    # Interleave so both kinds are in flight together.
    jobs = [jobs[k] for k in sorted(range(len(jobs)), key=lambda k: (k * 7919) % len(jobs))]
elif mode == "hot":
    jobs = [lambda i=i: play_cross(i) for i in range(plays)]
elif mode == "user_end":
    for cell in edges:
        jobs += [lambda cell=cell, j=j: user_end(cell, j) for j in range(user_news)]

started = time.time()
with concurrent.futures.ThreadPoolExecutor(max_workers=min(concurrency, len(jobs)) or 1) as pool:
    results = list(pool.map(lambda job: job(), jobs))
elapsed = time.time() - started

summary = collections.defaultdict(lambda: {"n": 0, "ok": 0, "answered": 0, "codes": collections.Counter(), "lat": []})
by_where = collections.Counter()
for kind, code, ok, answered, took, where in results:
    s = summary[kind]
    s["n"] += 1
    s["ok"] += ok
    s["answered"] += answered
    s["codes"][str(code)] += 1
    s["lat"].append(took)
    if not ok:
        by_where[f"{kind} {code} {where}"] += 1
report = {"elapsed": round(elapsed, 2)}
if mode == "hot":
    report["local"] = sum(1 for r in results if str(r[5]).startswith("local:"))
    report["elsewhere"] = dict(collections.Counter(r[5] for r in results if r[2] and not str(r[5]).startswith("local:")).most_common(6))
for kind, s in summary.items():
    lat = sorted(s["lat"])
    report[kind] = {"n": s["n"], "ok": s["ok"], "answered": s["answered"], "codes": dict(s["codes"]),
                    "p50_ms": round(lat[len(lat) // 2] * 1000), "p99_ms": round(lat[max(int(len(lat) * 0.99) - 1, 0)] * 1000)}
report["not_ok"] = dict(by_where.most_common(12))
json.dump(report, open(out, "w"))
PY

# Every stream resolves through its own cell before the bursts, so they
# measure steady state rather than first-ingest projection.
probe_all() {
  python3 "$WORK/burst.py" probe "$WORK/streams" "$WORK/probe.json" "${REPLICAS[*]}" 0 0 64
  printf '    resolving: %s of %s\n' "$(jq -r '.probe.ok' "$WORK/probe.json")" "$N"
  [ "$(jq -r '.probe.ok' "$WORK/probe.json")" -eq "$N" ]
}
eventually 180 "every stream resolves through its own cell" probe_all || finish
sleep 5

# metrics <file>: every replica's /metrics, prefixed with the replica.
metrics() {
  local base
  for base in "${REPLICAS[@]}"; do
    curl -s -m 10 "$base/metrics" | sed "s#^#$base #"
  done >"$1"
}
# delta <before> <after> <metric> <python condition on labels l>: the summed
# increase of the metric's series whose labels pass the condition. A labelled
# counter has no series until its first increment, so a missing one is 0.
delta() {
  python3 - "$1" "$2" "$3" "$4" <<'PY'
import re, sys
before, after, metric, cond = sys.argv[1:5]
line = re.compile(r'^(\S+) (\w+)\{([^}]*)\} (\S+)$')
def load(path):
    out = {}
    for l in open(path):
        m = line.match(l.strip())
        if not m or m.group(2) != metric:
            continue
        labels = dict(re.findall(r'(\w+)="([^"]*)"', m.group(3)))
        if eval(cond, {}, {"l": labels}):
            out[(m.group(1), m.group(3))] = float(m.group(4))
    return out
b, a = load(before), load(after)
print(int(sum(v - b.get(k, 0.0) for k, v in a.items())))
PY
}
VIEWER_PATHS='l.get("path") not in ("ingest_grpc", "ingest_http", "mist_push_rewrite", "mist_ingest_release")'

# burst <label> <plays> <user_new per edge> [mode] [streams file]: one burst and
# its metric checks. Mode "burst" spreads over every stream and replica; "hot"
# sends each stream's viewers to the other cell's replicas.
burst() {
  local label=$1 plays=$2 user_news=$3 mode=${4:-burst} streams=${5:-$WORK/streams}
  local start end window qm commodore purser misses bypass hits bound
  metrics "$WORK/$label.before"
  check "$label: every replica exports foghorn_placement_pair_reads_total" \
    [ "$(awk '$2 ~ /^foghorn_placement_pair_reads_total\{/ {print $1}' "$WORK/$label.before" | sort -u | wc -l | tr -d ' ')" -eq "${#REPLICAS[@]}" ]
  start=$(date +%s.%N)
  log "$label: $plays /play over ${#REPLICAS[@]} replicas and $user_news USER_NEW per edge, up to $CONCURRENCY in flight"
  python3 "$WORK/burst.py" "$mode" "$streams" "$WORK/$label.json" "${REPLICAS[*]}" "$plays" "$user_news" "$CONCURRENCY"
  end=$(date +%s.%N)
  metrics "$WORK/$label.after"
  window=$(python3 -c "import math,sys; print(math.ceil(float(sys.argv[2]) - float(sys.argv[1])) + 1)" "$start" "$end")
  jq -r '"    \(.elapsed) s; play \(.play.codes) p50/p99 \(.play.p50_ms)/\(.play.p99_ms) ms" +
    (if .user_new then "; USER_NEW ok \(.user_new.ok)/\(.user_new.n) p50/p99 \(.user_new.p50_ms)/\(.user_new.p99_ms) ms" else "" end)' "$WORK/$label.json"
  jq -r '.not_ok | to_entries[] | "    not ok: \(.value) x \(.key)"' "$WORK/$label.json"

  qm=$(delta "$WORK/$label.before" "$WORK/$label.after" foghorn_media_request_central_rpcs_total 'l.get("service") == "quartermaster"')
  commodore=$(delta "$WORK/$label.before" "$WORK/$label.after" foghorn_media_request_central_rpcs_total "l.get(\"service\") == \"commodore\" and $VIEWER_PATHS")
  purser=$(delta "$WORK/$label.before" "$WORK/$label.after" foghorn_media_request_central_rpcs_total "l.get(\"service\") == \"purser\" and $VIEWER_PATHS")
  misses=$(delta "$WORK/$label.before" "$WORK/$label.after" foghorn_placement_pair_reads_total 'l.get("result") == "miss"')
  bypass=$(delta "$WORK/$label.before" "$WORK/$label.after" foghorn_placement_pair_reads_total 'l.get("result") == "bypass"')
  hits=$(delta "$WORK/$label.before" "$WORK/$label.after" foghorn_placement_pair_reads_total 'l.get("result") == "hit"')
  bound=$((N * ${#REPLICAS[@]} * window))
  echo "    central RPCs: quartermaster $qm, commodore (viewer paths) $commodore, purser (viewer paths) $purser"
  echo "    pair reads: hit $hits, miss $misses, bypass $bypass (bound $bound = $N streams x ${#REPLICAS[@]} replicas x $window s)"
  check "$label: no Quartermaster call beneath any media request (got $qm)" [ "$qm" -eq 0 ]
  check "$label: no Commodore call on a viewer path (got $commodore)" [ "$commodore" -eq 0 ]
  check "$label: pair-read misses stay within streams x replicas x seconds ($misses <= $bound)" [ "$misses" -le "$bound" ]
  if [ "$qm" -ne 0 ] || [ "$commodore" -ne 0 ]; then
    python3 - "$WORK/$label.before" "$WORK/$label.after" <<'PY'
import re, sys
line = re.compile(r'^(\S+) foghorn_media_request_central_rpcs_total\{([^}]*)\} (\S+)$')
def load(p):
    return {(m.group(1), m.group(2)): float(m.group(3)) for m in (line.match(l.strip()) for l in open(p)) if m}
b, a = load(sys.argv[1]), load(sys.argv[2])
for k, v in sorted(a.items()):
    if v - b.get(k, 0) > 0:
        print(f"    +{int(v - b.get(k, 0))} {k[0]} {k[1]}")
PY
  fi
}
field() { jq -r "$2" "$WORK/$1.json"; }

burst herd "$PLAYS" "$USER_NEWS"
check "herd: every /play is answered by the cell, a redirect or a 503 with Retry-After ($(field herd '.play.answered') of $PLAYS)" \
  [ "$(field herd '.play.answered')" -eq "$PLAYS" ]
check "herd: every synthetic USER_NEW is answered true or false ($(field herd '.user_new.answered') of $((USER_NEWS * 2)))" \
  [ "$(field herd '.user_new.answered')" -eq "$((USER_NEWS * 2))" ]
python3 "$WORK/burst.py" user_end "$WORK/streams" "$WORK/user_end.json" "${REPLICAS[*]}" 0 "$USER_NEWS" 64
check "the synthetic sessions are ended ($(field user_end '.user_end.ok') of $((USER_NEWS * 2)); $(field user_end '.user_end.codes'))" \
  [ "$(field user_end '.user_end.ok')" -eq "$((USER_NEWS * 2))" ]

# Unused reservations lapse 30 s after their redirect.
sleep 35
burst fit "$FIT_PLAYS" 0
check "fit: every /play redirects to media ($(field fit '.play.ok') of $FIT_PLAYS)" [ "$(field fit '.play.ok')" -eq "$FIT_PLAYS" ]

# The hot burst: one stream of each cell, every viewer arriving at the other
# cell. One viewer of each first, so the herd shares an existing cross-cell pull
# rather than measuring its arrangement.
{ awk '$4 == "a"' "$WORK/streams" | head -1; awk '$4 == "b"' "$WORK/streams" | head -1; } >"$WORK/hot-streams"
cross_cell_resolves() {
  python3 "$WORK/burst.py" hot "$WORK/hot-streams" "$WORK/hot-warm.json" "${REPLICAS[*]}" 2 0 2
  [ "$(jq -r '.play.ok' "$WORK/hot-warm.json")" -eq 2 ]
}
eventually 60 "each hot stream resolves through the other cell" cross_cell_resolves
sleep 35
HOT_SINCE=$(utc_now)
burst hot "$HOT_PLAYS" 0 hot "$WORK/hot-streams"
check "hot: every cross-cell /play on two streams redirects to media ($(field hot '.play.ok') of $HOT_PLAYS)" \
  [ "$(field hot '.play.ok')" -eq "$HOT_PLAYS" ]
check "hot: every viewer is sent to its arrival cell's edge, which has room ($(field hot '.local') of $HOT_PLAYS; elsewhere $(field hot '.elsewhere | tojson'))" \
  [ "$(field hot '.local')" -eq "$HOT_PLAYS" ]
contended=$(logs_since "$HOT_SINCE" foghorn foghorn-2 foghorn-b foghorn-b-2 | grep -c 'replication mutation contention')
check "hot: no Foghorn replica exhausted its registry record retries (got $contended lines)" [ "${contended:-0}" -eq 0 ]

# A sample of streams is fetched as a viewer would, so the redirects point at
# segments and not only at a location.
sample_media() {
  local n=0 want=0 ok=0 pb cell base
  while read -r pb _ _ cell; do
    n=$((n + 1))
    [ $((n % 10)) -eq 1 ] || continue
    want=$((want + 1)) base=${REPLICAS[0]}
    [ "$cell" = b ] && base=${REPLICAS[2]}
    media_at "$base" "$pb" && ok=$((ok + 1))
  done <"$WORK/streams"
  echo "    sampled viewers with media: $ok of $want"
  [ "$ok" -eq "$want" ]
}
eventually 60 "sampled viewers receive TS segments" sample_media
finish
