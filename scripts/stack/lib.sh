#!/usr/bin/env bash
# Shared helpers for the production-shaped stack scenarios (PLAN_TEST_HARDENING.md).
# Scenarios run inside the stack-runner container, address services by their
# compose names, and source only this file (which sources endpoints.sh).
#
# Result lines: PASS / FAIL / BLOCKED. BLOCKED means the stack lacks something
# the check needs (a tool, a service, fault-injection access); it is never a
# product verdict. A scenario exits 1 when anything FAILed, 2 when nothing
# failed but something was BLOCKED, and 0 otherwise.
set -uo pipefail

STACK_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "$STACK_LIB_DIR/endpoints.sh" ]; then
  # shellcheck source=/dev/null
  . "$STACK_LIB_DIR/endpoints.sh"
fi

# Contract defaults; endpoints.sh overrides any of them.
: "${BRIDGE_URL:=http://bridge:18000}"
: "${BRIDGE_WS_URL:=${BRIDGE_URL/http/ws}/graphql/ws}"
: "${FOGHORN_A_URLS:=http://foghorn:18008 http://foghorn-2:18018}"
: "${FOGHORN_B_URLS:=http://foghorn-b:18008 http://foghorn-b-2:18008}"
: "${EDGE_A_RTMP:=rtmp://edge:1935/live}"
: "${EDGE_B_RTMP:=rtmp://edge-b:1935/live}"
: "${WEBHOOK_RECEIVER_URL:=http://webhook-receiver:18777}"
: "${WEBHOOK_TARGET_URL:=$WEBHOOK_RECEIVER_URL/hooks}"
RECEIVER_URL=$WEBHOOK_RECEIVER_URL
RECEIVER_HOOK_URL=$WEBHOOK_TARGET_URL
: "${MAILPIT_API_URL:=http://mailpit:8025/api/v1}"
export MAILPIT_API=${MAILPIT_API_URL%/api/v1}
: "${CLICKHOUSE_URL:=http://clickhouse:8123/?user=${CLICKHOUSE_USER:-default}&password=${CLICKHOUSE_PASSWORD:-}}"
: "${PG_HOST:=postgres}"
: "${PG_PORT:=5432}"
: "${FOGHORN_A_DB:=foghorn}"
: "${FOGHORN_B_DB:=foghorn_b}"
: "${STACK_API_TOKEN:=${DEMO_API_TOKEN:-fw_0000000000000000000000000000000000000000000000000000000000demo01}}"
: "${STACK_TENANT_ID:=${DEMO_TENANT_ID:-5eed517e-ba5e-da7a-517e-ba5eda7a0001}}"
: "${STACK_DEMO_EMAIL:=demo@frameworks.network}"
: "${STACK_DEMO_PASSWORD:=stack-demo-password}"
: "${STACK_LIVEPEER_GATEWAY_A:=${LIVEPEER_GATEWAY_A_SERVICE:-livepeer-gateway-a}}"
: "${FOGHORN_A_CLUSTER:=${CELL_A_CLUSTER:-}}"
: "${STACK_STATE_DIR:=/tmp/fw-stack}"
mkdir -p "$STACK_STATE_DIR"

STACK_SCENARIO="$(basename "${0:-scenario}" .sh)"
PASS=0
FAIL=0
BLOCKED=0

ts() { date -u +%H:%M:%S; }
log() { printf '\n[%s %s] %s\n' "$STACK_SCENARIO" "$(ts)" "$*"; }
pass() { PASS=$((PASS + 1)); printf 'PASS  %s: %s\n' "$STACK_SCENARIO" "$*"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL  %s: %s\n' "$STACK_SCENARIO" "$*"; }
blocked() { BLOCKED=$((BLOCKED + 1)); printf 'BLOCKED  %s: %s\n' "$STACK_SCENARIO" "$*"; }
check() { # check <description> <command...>: one strict attempt
  local what=$1
  shift
  if "$@"; then pass "$what"; else fail "$what"; fi
}
json_has() { printf '%s' "$1" | jq -e "$2" >/dev/null 2>&1; } # json_has <json> <jq filter>
finish() {
  printf '\n%s: PASS=%d FAIL=%d BLOCKED=%d\n' "$STACK_SCENARIO" "$PASS" "$FAIL" "$BLOCKED"
  [ "$FAIL" -gt 0 ] && exit 1
  [ "$BLOCKED" -gt 0 ] && exit 2
  exit 0
}
need() { # need <tool>...: BLOCKED when a tool is missing
  local tool
  for tool in "$@"; do
    command -v "$tool" >/dev/null 2>&1 || { blocked "stack-runner lacks $tool"; return 1; }
  done
}

# eventually <secs> <description> <command...>: a bounded wait for an eventual
# state. Returns 2 from the command to stop early with a refusal. Never use it
# for a correctness check that must hold on the first attempt.
eventually() {
  local deadline=$(($(date +%s) + $1)) what=$2 rc
  shift 2
  while true; do
    "$@"
    rc=$?
    [ "$rc" = 0 ] && { pass "$what"; return 0; }
    [ "$rc" = 2 ] && { fail "refused: $what"; return 1; }
    [ "$(date +%s)" -ge "$deadline" ] && { fail "timed out: $what"; return 1; }
    sleep 3
  done
}

# ---- fault injection ----------------------------------------------------------

# stack_ctl <stop|start|restart|kill> <service>: acts on this stack's own compose
# project. Needs the docker CLI and socket in stack-runner; BLOCKED otherwise.
stack_ctl() {
  if ! command -v docker >/dev/null 2>&1 || [ ! -S /var/run/docker.sock ] || [ -z "${COMPOSE_PROJECT_NAME:-}" ]; then
    blocked "fault injection ($1 $2) needs docker, /var/run/docker.sock and COMPOSE_PROJECT_NAME in stack-runner"
    return 1
  fi
  docker compose -p "$COMPOSE_PROJECT_NAME" "$1" "$2" >/dev/null 2>&1
}
stack_exec() { # stack_exec <service> <command...>
  command -v docker >/dev/null 2>&1 && [ -S /var/run/docker.sock ] && [ -n "${COMPOSE_PROJECT_NAME:-}" ] || {
    blocked "exec in $1 needs docker access in stack-runner"
    return 1
  }
  local service=$1
  shift
  docker compose -p "$COMPOSE_PROJECT_NAME" exec -T "$service" "$@"
}
container_of() { docker compose -p "$COMPOSE_PROJECT_NAME" ps -q "$1" 2>/dev/null | head -1; } # container_of <service>
# logs_since <since> <service>...: the services' logs from <since> (RFC 3339 UTC).
logs_since() {
  local since=$1
  shift
  docker compose -p "$COMPOSE_PROJECT_NAME" logs --no-color --no-log-prefix --since "$since" "$@" 2>/dev/null
}
utc_now() { date -u +%Y-%m-%dT%H:%M:%S.%NZ; }
# log_has <since> <pattern> <service...>: a service logged a matching line.
# The log is read whole before matching: under pipefail an early-exiting
# `grep -q` breaks the pipe of a large log and reads as "no match".
log_has() {
  local since=$1 pattern=$2 out
  shift 2
  out=$(logs_since "$since" "$@")
  printf '%s\n' "$out" | grep -E "$pattern" >/dev/null
}
# log_json <since> <jq filter> <service...>: the service's JSON log lines through
# a filter; lines that are not JSON are skipped.
log_json() {
  local since=$1 filter=$2
  shift 2
  logs_since "$since" "$@" | jq -Rrc "fromjson? | $filter" 2>/dev/null
}

# Network faults on one service's network namespace. The edge bundle (Caddy,
# Mist, Helmsman) and its edge proxy share one namespace, so rules there act on
# the whole node while it keeps running, keeps its address and keeps its local
# (loopback) traffic. A helper container joined to that namespace with
# NET_ADMIN holds the tools; the rules live in their own chains (FW_FAULT_IN,
# FW_FAULT_OUT), so clearing them never touches Docker's own rules. The tools
# are installed before any rule exists, while the namespace still has its
# network.
: "${STACK_NETFAULT_IMAGE:=alpine:3.22}"
netfault_name() { printf '%s-netfault-%s' "$COMPOSE_PROJECT_NAME" "$1"; }
netfault_start() { # netfault_start <service>
  local cid name deadline
  command -v docker >/dev/null 2>&1 && [ -S /var/run/docker.sock ] && [ -n "${COMPOSE_PROJECT_NAME:-}" ] || {
    blocked "network faults on $1 need docker access in stack-runner"
    return 1
  }
  cid=$(container_of "$1")
  [ -n "$cid" ] || { blocked "no running container for $1"; return 1; }
  name=$(netfault_name "$1")
  docker rm -f "$name" >/dev/null 2>&1
  docker image inspect "$STACK_NETFAULT_IMAGE" >/dev/null 2>&1 || docker pull -q "$STACK_NETFAULT_IMAGE" >/dev/null 2>&1
  docker run -d --name "$name" --net "container:$cid" --cap-add NET_ADMIN "$STACK_NETFAULT_IMAGE" sh -c '
    apk add --no-cache iptables iproute2 >/dev/null || exit 1
    for c in IN OUT; do iptables -N FW_FAULT_$c || exit 1; done
    iptables -I INPUT -j FW_FAULT_IN && iptables -I OUTPUT -j FW_FAULT_OUT || exit 1
    touch /ready
    exec sleep infinity' >/dev/null || { blocked "could not start the network fault helper for $1"; return 1; }
  deadline=$(($(date +%s) + 120))
  until docker exec "$name" test -f /ready 2>/dev/null; do
    if [ "$(date +%s)" -ge "$deadline" ] || [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" != true ]; then
      blocked "network fault helper for $1 not ready: $(docker logs "$name" 2>&1 | tail -3)"
      return 1
    fi
    sleep 1
  done
}
netfault() { # netfault <service> <command...>: runs in the helper, inside the service's namespace
  local name
  name=$(netfault_name "$1")
  shift
  docker exec "$name" "$@"
}
netfault_clear() { netfault "$1" sh -c 'iptables -F FW_FAULT_IN && iptables -F FW_FAULT_OUT'; }
netfault_rules() { netfault "$1" sh -c 'iptables -S FW_FAULT_IN; iptables -S FW_FAULT_OUT' 2>/dev/null; }
netfault_stop() { # netfault_stop <service>: removes the rules, their chains and the helper
  local name
  name=$(netfault_name "$1")
  docker exec "$name" sh -c 'iptables -D INPUT -j FW_FAULT_IN; iptables -D OUTPUT -j FW_FAULT_OUT
    for c in IN OUT; do iptables -F FW_FAULT_$c; iptables -X FW_FAULT_$c; done' >/dev/null 2>&1
  docker rm -f "$name" >/dev/null 2>&1
  return 0
}

# ---- API -------------------------------------------------------------------

gql() { # gql <query> [variables-json] [token]
  curl -s -m 30 "$BRIDGE_URL/graphql" -H "Authorization: Bearer ${3:-$STACK_API_TOKEN}" \
    -H 'Content-Type: application/json' \
    --data "$(jq -cn --arg q "$1" --argjson v "${2:-{\}}" '{query:$q, variables:$v}')"
}
# bot_fields: the behavioral bot-check fields Commodore requires when no
# Turnstile secret is configured (the stack has none), as a JSON object to merge
# into /auth/login and /auth/register bodies.
bot_fields() {
  local now=$(($(date +%s) * 1000))
  jq -cn --argjson shown $((now - 8000)) --argjson now "$now" \
    '{turnstile_token:"XXXX.DUMMY.TOKEN.XXXX", human_check:"human",
      behavior:({formShownAt:$shown, submittedAt:$now, mouse:true, typed:true}|tojson)}'
}
# gateway_discoverable <instance_id>: Quartermaster has probed the gateway
# healthy recently, so Foghorn's discovery offers it. A restarted gateway is
# only offered again after the next health poll (every 30 s).
gateway_discoverable() {
  [ "$(pg quartermaster "SELECT count(*) FROM quartermaster.service_instances WHERE instance_id = '$1' AND health_status = 'healthy' AND last_health_check > NOW() - INTERVAL '45 seconds'")" = 1 ]
}
session_token() { # session_token: the demo user's access JWT from /auth/login (cached per scenario)
  local cache="$STACK_STATE_DIR/session-$STACK_SCENARIO.jwt" jar
  if [ -s "$cache" ]; then cat "$cache"; return 0; fi
  jar=$(mktemp)
  curl -s -m 30 -c "$jar" -o /dev/null "$BRIDGE_URL/auth/login" -H 'Content-Type: application/json' \
    --data "$(jq -cn --arg e "$STACK_DEMO_EMAIL" --arg p "$STACK_DEMO_PASSWORD" --argjson b "$(bot_fields)" \
      '{email:$e, password:$p} + $b')"
  awk '$6 == "access_token" { print $7 }' "$jar" >"$cache"
  rm -f "$jar"
  [ -s "$cache" ] || { rm -f "$cache"; echo "demo session login failed" >&2; return 1; }
  cat "$cache"
}
gql_session() { # gql_session <query> [variables-json]: as the logged-in demo user
  local jwt
  jwt=$(session_token) || return 1
  gql "$1" "${2:-{\}}" "$jwt"
}
gql_ok() { # gql_ok <response> <jq path to typename> <wanted typename>
  echo "$1" | jq -e --arg t "$3" "(.errors == null) and ($2 == \$t)" >/dev/null 2>&1
}

create_stream() { # create_stream <name> <record:true|false> -> JSON {id streamId playbackId streamKey}
  gql 'mutation($i:CreateStreamInput!){createStream(input:$i){__typename ... on Stream{id streamId playbackId streamKey record dvrChapterMode} ... on ValidationError{message}}}' \
    "$(jq -cn --arg n "$1" --argjson r "$2" '{i:{name:$n,record:$r}}')" | jq -c '.data.createStream // .'
}
delete_stream() { gql 'mutation($id:ID!){deleteStream(id:$id){__typename}}' "$(jq -cn --arg id "$1" '{id:$id}')" >/dev/null; }

# ---- databases --------------------------------------------------------------

pg() { # pg <db> <sql>: the service's own role, read-only use
  need psql || return 1
  PGPASSWORD="${POSTGRES_PASSWORD:-}" psql -h "$PG_HOST" -p "$PG_PORT" -U "$1" -d "$1" -v ON_ERROR_STOP=1 -Atc "$2" 2>/dev/null </dev/null
}
ch() { # ch <sql>: CLICKHOUSE_URL carries the credentials as query parameters
  curl -s -m 20 "$CLICKHOUSE_URL" --data-binary "$1"
}
internal_name_of() { # internal_name_of <stream uuid>
  pg commodore "SELECT internal_name FROM commodore.streams WHERE tenant_id='$STACK_TENANT_ID' AND id='$1'"
}
# ingest_sessions <foghorn db> <internal name>: "id|node|ended_reason|started epoch|ended epoch"
# per ingest generation of the stream, oldest first. Mist runtimes are named
# live+<internal name>.
ingest_sessions() {
  pg "$1" "SELECT id, node_id, COALESCE(ended_reason,''), floor(EXTRACT(EPOCH FROM started_at))::bigint,
      COALESCE(floor(EXTRACT(EPOCH FROM ended_at))::bigint, 0)
    FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID'
      AND stream_internal_name IN ('$2', 'live+$2') ORDER BY started_at"
}
# open_generation <foghorn db> <internal name>: the open, projected session's id.
open_generation() {
  pg "$1" "SELECT id FROM foghorn.ingest_sessions WHERE tenant_id='$STACK_TENANT_ID'
      AND stream_internal_name IN ('$2', 'live+$2') AND ended_at IS NULL AND projection_state='active'
    ORDER BY started_at DESC"
}

# ---- media ------------------------------------------------------------------

# publish <rtmp base> <stream key> <WxH> <fps> <seconds>: background RTMP publisher.
# Prints the PID. A lavfi test pattern with a tone, 2 s GOP, no FLV metadata track.
publish() {
  need ffmpeg || return 1
  ffmpeg -hide_banner -loglevel error -re \
    -f lavfi -i "testsrc2=size=$3:rate=$4" -f lavfi -i 'sine=frequency=440:sample_rate=48000' \
    -t "$5" -c:v libx264 -preset veryfast -g $(($4 * 2)) -pix_fmt yuv420p -c:a aac -b:a 96k \
    -flvflags no_metadata -f flv "$1/$2" >"$STACK_STATE_DIR/publish-$2.log" 2>&1 &
  echo $!
}
# publish runs ffmpeg inside the caller's $(...) subshell, so the PID it prints
# is not a child of the scenario shell and `wait` returns at once. Poll until the
# publisher is gone (bounded) instead.
wait_publisher() { # wait_publisher <pid> [timeout_seconds]
  local pid=$1 deadline=$(($(date +%s) + ${2:-600}))
  while kill -0 "$pid" 2>/dev/null; do
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 1
  done
}
# publish_until_admitted <rtmp base> <stream key> <WxH> <fps> <seconds> [timeout]:
# scenario setup for a stream whose publisher must be live before a fault is
# injected. Restarts the publisher while its push is refused (a freshly started
# stack refuses ingest until placement authority is ready) and prints the PID
# of the one that stayed connected for 8 s. Not for an admission under test.
publish_until_admitted() {
  local deadline=$(($(date +%s) + ${6:-120})) pid
  while [ "$(date +%s)" -lt "$deadline" ]; do
    pid=$(publish "$1" "$2" "$3" "$4" "$5") || return 1
    for _ in 1 2 3 4 5 6 7 8; do
      kill -0 "$pid" 2>/dev/null || break
      sleep 1
    done
    if kill -0 "$pid" 2>/dev/null; then echo "$pid"; return 0; fi
    sleep 3
  done
  return 1
}
render_mp4() { # render_mp4 <out> <WxH> <fps> <seconds>
  need ffmpeg || return 1
  ffmpeg -hide_banner -loglevel error -y -f lavfi -i "testsrc2=size=$2:rate=$3" \
    -f lavfi -i 'sine=frequency=440:sample_rate=48000' -t "$4" -c:v libx264 -preset veryfast -g $(($3 * 2)) \
    -pix_fmt yuv420p -c:a aac -movflags +faststart "$1"
}

# location_at <foghorn> <playback id> [header...]: the viewer redirect, or empty.
location_at() {
  local base=$1 id=$2
  shift 2
  curl -s -m 10 -o /dev/null -D - "$@" "$base/play/$id/hls" | awk 'tolower($1)=="location:"{print $2}' | tr -d '\r'
}
play_status() { curl -s -m 10 -o /dev/null -w '%{http_code}' "$1/play/$2/hls"; }

# segment_fetch <master url>: "<code> <bytes>" for master -> variant -> first TS
# segment of one viewer session. Bytes count only aligned transport packets.
segment_fetch() {
  python3 - "$1" <<'PY' 2>/dev/null
import sys, urllib.request, urllib.parse
def get(url):
    with urllib.request.urlopen(url, timeout=10) as r:
        return r.status, r.read()
def entries(body, base):
    return [urllib.parse.urljoin(base, l.strip()) for l in body.decode("utf-8", "replace").splitlines()
            if l.strip() and not l.startswith("#")]
try:
    url = sys.argv[1]
    for _ in range(3):
        code, body = get(url)
        if url.split("?")[0].endswith(".m3u8"):
            nxt = entries(body, url)
            if not nxt:
                print(f"{code} 0"); sys.exit(0)
            url = nxt[0]
            continue
        valid = len(body) >= 3 * 188 and all(body[i] == 0x47 for i in (0, 188, 376))
        print(f"{code} {len(body) if valid else 0}"); sys.exit(0)
    print("000 0")
except Exception as exc:
    print(f"{getattr(exc, 'code', '000')} 0")
PY
}
media_at() { # media_at <foghorn> <playback id>: a resolved viewer gets real TS bytes
  local loc got
  loc=$(location_at "$1" "$2")
  [ -n "$loc" ] || return 1
  got=$(segment_fetch "$loc")
  case "$got" in 200\ *) [ "${got#200 }" -gt 0 ] ;; *) return 1 ;; esac
}
# hls_position <master url>: the index of the newest segment of the first
# variant (media sequence + segment count), fetched from wherever the URL points
# (an edge address serves without Foghorn). Empty when unreachable.
hls_position() {
  python3 - "$1" <<'PY' 2>/dev/null
import sys, urllib.request, urllib.parse
def get(url):
    with urllib.request.urlopen(url, timeout=8) as r:
        return r.read().decode("utf-8", "replace")
url = sys.argv[1]
body = get(url)
if "#EXT-X-STREAM-INF" in body:
    url = urllib.parse.urljoin(url, next(l.strip() for l in body.splitlines() if l.strip() and not l.startswith("#")))
    body = get(url)
seq = next((int(l.split(":")[1]) for l in body.splitlines() if l.startswith("#EXT-X-MEDIA-SEQUENCE:")), 0)
print(seq + sum(1 for l in body.splitlines() if l.startswith("#EXTINF:")))
PY
}
# hls_session_url <master url>: the first variant's media playlist URL, which
# carries the viewer session Mist handed out, so polling it is one ongoing viewer.
hls_session_url() {
  python3 - "$1" <<'PY' 2>/dev/null
import sys, urllib.request, urllib.parse
with urllib.request.urlopen(sys.argv[1], timeout=8) as r:
    body = r.read().decode("utf-8", "replace")
print(urllib.parse.urljoin(sys.argv[1], next(l.strip() for l in body.splitlines() if l.strip() and not l.startswith("#"))))
PY
}
# hls_poll <media playlist url> [interval]: one ongoing viewer. Every interval
# (default 2 s) it fetches the playlist and its second-newest segment and
# prints "<unix time> ok|fail [why]" (a playlist alone is not media: Mist
# answers playlist requests of a refused session). Run it in the background
# (exec keeps $! the poller itself, so kill stops it).
hls_poll() {
  exec python3 -u - "$1" "${2:-2}" <<'PY' 2>/dev/null
import sys, time, urllib.request, urllib.parse
url, interval = sys.argv[1], float(sys.argv[2])
def get(u):
    with urllib.request.urlopen(u, timeout=8) as r:
        return r.read()
while True:
    why = ""
    try:
        body = get(url).decode("utf-8", "replace")
        segs = [l.strip() for l in body.splitlines() if l.strip() and not l.startswith("#")]
        if not segs:
            why = "empty-playlist"
        else:
            data = get(urllib.parse.urljoin(url, segs[-2] if len(segs) > 1 else segs[-1]))
            if len(data) < 3 * 188 or data[0] != 0x47:
                why = f"not-ts:{len(data)}"
    except Exception as e:
        why = f"{type(e).__name__}:{str(e)[:80]}".replace(" ", "_")
    print(f"{int(time.time())} {'fail ' + why if why else 'ok'}", flush=True)
    time.sleep(interval)
PY
}
# hls_poll_state <poll output file> <since unix time>: "ok" when every sample
# since then succeeded, "fail" when the latest one failed, empty with no sample.
hls_poll_state() {
  awk -v since="$2" '$1 >= since { n++; last = $2; if ($2 != "ok") bad++ } END { if (n) print (last == "fail" ? "fail" : (bad ? "mixed" : "ok")) }' "$1" 2>/dev/null
}
master_variants() { # master_variants <master url>: "WxH bandwidth" per variant
  curl -s -m 10 "$1" | awk -F'RESOLUTION=' '/^#EXT-X-STREAM-INF/{split($2,a,","); print a[1]}'
}

# variant_spans <foghorn> <playback id>: "<resolution or -> <seconds>" per HLS
# variant of one viewer session, the seconds summed from its EXTINF entries.
variant_spans() {
  local loc
  loc=$(location_at "$1" "$2")
  [ -n "$loc" ] || return 1
  python3 - "$loc" <<'PY' 2>/dev/null
import sys, urllib.request, urllib.parse
def get(url):
    with urllib.request.urlopen(url, timeout=15) as r:
        return r.read().decode("utf-8", "replace")
master = sys.argv[1]
body = get(master)
res = "-"
variants = []
for line in body.splitlines():
    line = line.strip()
    if line.startswith("#EXT-X-STREAM-INF"):
        res = line.split("RESOLUTION=")[1].split(",")[0] if "RESOLUTION=" in line else "-"
    elif line and not line.startswith("#"):
        variants.append((res, urllib.parse.urljoin(master, line)))
        res = "-"
if not variants:
    variants = [("-", master)]
for res, url in variants:
    total = sum(float(l.split(":")[1].split(",")[0]) for l in get(url).splitlines() if l.startswith("#EXTINF:"))
    print(f"{res} {total:.1f}")
PY
}
# full_length <foghorn> <playback id> <seconds> [tolerance]: every variant of the
# asset spans the expected length (renditions included, so a ladder missing its
# first minute fails).
full_length() {
  local spans want=$3 tol=${4:-4}
  spans=$(variant_spans "$1" "$2") || return 1
  echo "    spans: $(echo "$spans" | tr '\n' ' ')"
  [ -n "$spans" ] && echo "$spans" | awk -v w="$want" -v t="$tol" '{ if ($2 < w - t || $2 > w + t) bad = 1 } END { exit bad }'
}

# every_replica <description> <playback id> <command-on-foghorn...>: runs the
# check once through every Foghorn replica of both cells; each must pass.
every_replica() {
  local what=$1 id=$2 base
  shift 2
  for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
    if "$@" "$base" "$id"; then pass "$what via $base"; else fail "$what via $base"; fi
  done
}

# upload_vod <file> <title>: multipart upload through presigned parts. Prints the
# completed VodAsset JSON. Presigned URLs are signed for their Host, so an
# address the runner cannot reach is redirected with STORAGE_CONNECT_TO
# ("host:port:target:port", curl --connect-to syntax) instead of being rewritten.
upload_vod() {
  local file=$1 title=$2 size r upload parts='[]' pn url etag connect=()
  size=$(stat -c %s "$file" 2>/dev/null || stat -f %z "$file")
  [ -n "${STORAGE_CONNECT_TO:-}" ] && connect=(--connect-to "$STORAGE_CONNECT_TO")
  r=$(gql 'mutation($i:CreateVodUploadInput!){createVodUpload(input:$i){__typename ... on VodUploadSession{id artifactHash playbackId parts{partNumber presignedUrl}} ... on ValidationError{message}}}' \
    "$(jq -cn --argjson sz "$size" --arg f "$(basename "$file")" --arg t "$title" '{i:{filename:$f,sizeBytes:$sz,contentType:"video/mp4",title:$t}}')")
  upload=$(echo "$r" | jq -r '.data.createVodUpload.id // empty')
  [ -n "$upload" ] || { echo "createVodUpload: $r" >&2; return 1; }
  for pn in $(echo "$r" | jq -r '.data.createVodUpload.parts[].partNumber'); do
    url=$(echo "$r" | jq -r --argjson pn "$pn" '.data.createVodUpload.parts[]|select(.partNumber==$pn).presignedUrl')
    etag=$(curl -s -m 120 "${connect[@]}" -X PUT --data-binary "@$file" -D - -o /dev/null "$url" | awk 'tolower($1)=="etag:"{print $2}' | tr -d '\r"')
    [ -n "$etag" ] || { echo "part $pn upload returned no ETag" >&2; return 1; }
    parts=$(echo "$parts" | jq -c --argjson pn "$pn" --arg e "$etag" '. + [{partNumber:$pn,etag:$e}]')
  done
  gql 'mutation($i:CompleteVodUploadInput!){completeVodUpload(input:$i){__typename ... on VodAsset{id artifactHash playbackId status} ... on ValidationError{message}}}' \
    "$(jq -cn --arg u "$upload" --argjson p "$parts" '{i:{uploadId:$u,parts:$p}}')" | jq -c '.data.completeVodUpload // .'
}
vod_status() { gql 'query($id:ID!){node(id:$id){... on VodAsset{status errorMessage}}}' "$(jq -cn --arg id "$1" '{id:$id}')" | jq -r '.data.node.status // "?"'; }

# ---- tracks -----------------------------------------------------------------

# latest_tracks <stream uuid>: "type codec WxH" lines from the newest track list.
# timestamp has second precision and Mist emits several lists within one second
# while tracks are added; the UUIDv7 event_id orders them within that second.
latest_tracks() {
  ch "SELECT track_list FROM periscope.track_list_events WHERE stream_id = toUUID('$1') ORDER BY timestamp DESC, toString(event_id) DESC LIMIT 1 FORMAT TSVRaw" |
    jq -r '(if type=="array" then .[] else (to_entries[]|.value) end) | "\(.track_type // .type // .trackType) \(.codec) \(.width // 0)x\(.height // 0)"' 2>/dev/null
}
video_heights() { # video_heights <stream uuid>: sorted rendition heights, JPEG excluded
  latest_tracks "$1" | awk '$1=="video" && $2!="JPEG" && $2!="PNG" {split($3,d,"x"); print d[2]}' | sort -n
}

# ---- webhooks ---------------------------------------------------------------

# webhook_endpoint: one "*" endpoint at the receiver per stack run; prints
# "<endpoint id> <whsec secret>".
webhook_endpoint() {
  local cache="$STACK_STATE_DIR/webhook-endpoint" r
  if [ -s "$cache" ]; then cat "$cache"; return 0; fi
  r=$(gql_session 'mutation($i:CreateWebhookEndpointInput!){createWebhookEndpoint(input:$i){__typename ... on WebhookEndpointSecret{secret endpoint{id}} ... on ValidationError{message}}}' \
    "$(jq -cn --arg u "$RECEIVER_HOOK_URL" '{i:{url:$u,description:"stack scenarios",eventTypes:["*"]}}')")
  echo "$r" | jq -r '.data.createWebhookEndpoint | select(.__typename=="WebhookEndpointSecret") | "\(.endpoint.id) \(.secret)"' >"$cache"
  [ -s "$cache" ] || { echo "createWebhookEndpoint: $r" >&2; rm -f "$cache"; return 1; }
  cat "$cache"
}
webhook_requests() { curl -s -m 10 "$RECEIVER_URL/requests"; }

# webhook_events <since-epoch>: "<type> <webhook-id> <sig-valid> <data-json>" per
# delivery received at or after since, signatures checked against the endpoint secret.
webhook_events() {
  local secret
  secret=$(webhook_endpoint | awk '{print $2}')
  webhook_requests | python3 -c '
import base64, hashlib, hmac, json, sys
since, secret = float(sys.argv[1]), sys.argv[2]
key = base64.b64decode(secret.split("_", 1)[1]) if "_" in secret else b""
raw = sys.stdin.read().strip()
rows = json.loads(raw) if raw.startswith("[") else [json.loads(l) for l in raw.splitlines() if l.strip()]
for r in rows:
    if float(r.get("t", 0)) < since:
        continue
    h = {k.lower(): v for k, v in (r.get("headers") or {}).items()}
    mid, stamp, sigs = h.get("webhook-id", ""), h.get("webhook-timestamp", ""), h.get("webhook-signature", "")
    body = r.get("body", "")
    want = base64.b64encode(hmac.new(key, f"{mid}.{stamp}.{body}".encode(), hashlib.sha256).digest()).decode()
    ok = any(hmac.compare_digest(want, s.split(",", 1)[1]) for s in sigs.split() if s.startswith("v1,"))
    try:
        b = json.loads(body)
    except Exception:
        b = {}
    print(b.get("type", "?"), mid, "valid" if ok else "INVALID", json.dumps(b.get("data") or {}, separators=(",", ":")))
' "$1" "$secret"
}
# saw_event <since> <type> [jq filter on data]: at least one signed delivery.
saw_event() {
  webhook_events "$1" | awk -v t="$2" '$1==t && $3=="valid"{ $1=$2=$3=""; print substr($0,4) }' |
    while read -r data; do echo "$data" | jq -e "${3:-true}" >/dev/null 2>&1 && { echo hit; break; }; done | grep -q hit
}
