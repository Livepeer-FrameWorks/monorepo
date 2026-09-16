#!/usr/bin/env bash
# Two-cell media proof on the local dev stack (docker compose profile two-cell).
#
# Cell A: the ordinary dev cell (control cell central-primary, media cluster demo-media,
# edge-node-1, Mist on localhost:18090). Cell B: a second platform cell (us-primary) whose
# Foghorn (foghorn-b) serves the tenant's virtual private cluster demo-selfhosted
# (edge-node-b, Mist on localhost:8081). Every Foghorn is platform-run; the two cells
# federate. One Commodore and one Quartermaster serve both. The script resolves RTMP through
# both cells under an ingest policy, publishes to the exact node they agree on, resolves
# viewers through both cells onto the private edge and asserts real media there,
# then exercises private-only refusal, capacity fallback, publisher reconnect and a
# control-plane outage. It is a make target (verify-two-cell-media), not a CI gate: it
# rebuilds images, needs Docker and the edited Mist image, and takes minutes.
#
# Re-running against a stack whose Helmsman containers were recreated (image rebuild)
# fails enrollment: Quartermaster binds each edge node to its container fingerprint
# (machine-id plus MACs) and a recreated container carries new MACs. Use a fresh volume
# (docker compose down -v) or TWO_CELL_REENROLL=1, which drops the two edge registrations
# and lets both Helmsmans enroll again. TWO_CELL_RESUME=1 skips bring-up, seeding and
# activation when the stack is already activated.
set -euo pipefail

: "${MIST_IMAGE:?Set MIST_IMAGE to a Mist image whose PUSH_REWRITE carries the connector line (scripts/verify-mist-current-source.sh builds one)}"
export MIST_IMAGE
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

TENANT_ID=5eed517e-ba5e-da7a-517e-ba5eda7a0001
API_TOKEN=fw_0000000000000000000000000000000000000000000000000000000000demo01
STREAM_KEY=sk_demo_live_stream_primary_key
PLAYBACK_ID=pb_demo_live_001
# Stored media from the same demo tenant: a seeded VOD asset whose bytes exist on
# cell A's edge. It proves the serving policy reaches artifacts, which keep storage
# affinity for ranking and take placement only as an eligibility filter.
STORED_PLAYBACK_ID=vod1a2b3c4d5e6fg
FOGHORN_A=http://127.0.0.1:18008
FOGHORN_B=http://127.0.0.1:18058
BRIDGE=http://127.0.0.1:18000
# Edges advertise in-network addresses: peers pull DTSC from the host in the public URL
# (a host-mapped localhost is unreachable from the other cell's Mist), so viewer
# redirects point into the compose network and media checks run there too.
export EDGE_PUBLIC_URL=${EDGE_PUBLIC_URL:-http://mistserver:8082}
EDGE_A_HOST=mistserver:8082
EDGE_B_HOST=mistserver-b:8082
PUBLISHER=fw-two-cell-publisher
# Cell A's Mist host ports move off the defaults so the proof coexists with a Mist
# running natively on this host; nothing in the proof depends on those host ports.
export MIST_CONTROLLER_HOST_PORT=${MIST_CONTROLLER_HOST_PORT:-14242} MIST_HTTP_HOST_PORT=${MIST_HTTP_HOST_PORT:-18081} \
  MIST_RTMP_HOST_PORT=${MIST_RTMP_HOST_PORT:-11935} MIST_RTSP_HOST_PORT=${MIST_RTSP_HOST_PORT:-15554} \
  MIST_DTSC_HOST_PORT=${MIST_DTSC_HOST_PORT:-14200} MIST_SRT_HOST_PORT=${MIST_SRT_HOST_PORT:-18889} \
  MIST_WEBRTC_HOST_PORT=${MIST_WEBRTC_HOST_PORT:-28203}
# Both edges enroll with bootstrap tokens: edge-node-1 with the demo token (the .env
# default; the demo seed does not pre-provision it), edge-node-b with its own token in compose.
export EDGE_ENROLLMENT_TOKEN=${EDGE_ENROLLMENT_TOKEN:-demo_bootstrap_token_for_local_development_testing_only}
# Cross-cell placement discovery and origin pull run over Foghorn federation (off in .env).
export FEDERATION_ENABLED=true FEDERATION_ALLOW_INSECURE_DEV=true
compose() { docker compose --profile two-cell "$@"; }
log() { printf '\n[two-cell] %s\n' "$*"; }
fail() { printf '\n[two-cell] FAIL: %s\n' "$*" >&2; exit 1; }
psql_db() { compose exec -T postgres psql -v ON_ERROR_STOP=1 -U "$1" -d "$1" -Atc "$2"; }
wait_for() { # wait_for <seconds> <description> <command...>
  local deadline=$(( $(date +%s) + $1 )); local what=$2; shift 2
  until "$@"; do
    if [ "$(date +%s)" -ge "$deadline" ]; then fail "timed out waiting for $what"; fi
    sleep 2
  done
}
http_ok() { curl -fsS -m 5 "$1" >/dev/null 2>&1; }
http_any() { [ "$(curl -s -m 5 -o /dev/null -w "%{http_code}" "$1")" != 000 ]; }
gql() { # gql <query> <variables-json>
  curl -fsS -m 20 "$BRIDGE/graphql" -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
    --data "$(jq -cn --arg q "$1" --argjson v "$2" '{query:$q, variables:$v}')"
}
resolve_location() { # prints the 307 Location for a viewer at the given Foghorn, or empty
  curl -s -m 10 -o /dev/null -D - "$1/play/$PLAYBACK_ID/hls" | awk 'tolower($1)=="location:" {print $2}' | tr -d '\r'
}
resolve_status() { curl -s -m 10 -o /dev/null -w '%{http_code}' "$1/play/$PLAYBACK_ID/hls"; }
resolve_body() { curl -s -m 10 "$1/play/$PLAYBACK_ID/hls"; }
stored_status() { curl -s -m 10 -o /dev/null -w '%{http_code}' "$1/play/$STORED_PLAYBACK_ID/hls"; }
stored_body() { curl -s -m 10 "$1/play/$STORED_PLAYBACK_ID/hls"; }
stored_location() { # prints the 307 Location a stored-media viewer receives, or empty
  curl -s -m 10 -o /dev/null -D - "$1/play/$STORED_PLAYBACK_ID/hls" | awk 'tolower($1)=="location:" {print $2}' | tr -d '\r'
}
stored_refused() { [ "$(stored_status "$1")" = 503 ] && stored_body "$1" | grep -q PLAYBACK_PLACEMENT_UNAVAILABLE; }
# Both cells assert the same refusal shape. On the live lane every resolution
# failure carries this code, so the body is a symmetry check rather than proof of
# policy; what the refusal proves is that no destination was handed out at all.
resolve_refused() { [ "$(resolve_status "$1")" = 503 ] && resolve_body "$1" | grep -q PLAYBACK_PLACEMENT_UNAVAILABLE; }
pull_location() { # prints the 307 Location a viewer of the configured pull input receives
  curl -s -m 10 -o /dev/null -D - "$1/play/$PULL_PLAYBACK_ID/hls" | awk 'tolower($1)=="location:" {print $2}' | tr -d '\r'
}
pull_location_is() { # pull_location_is <foghorn> <edge-host>
  local loc; loc="$(pull_location "$1")"; [[ "$loc" == *"$2"* ]]
}
PULL_UPSTREAM=fw-two-cell-upstream
PULL_UPSTREAM_HOST="$PULL_UPSTREAM:80"
# Mist's HTTP-reachable inputs are the playlist ones, so the upstream is an HLS
# rendition of a seeded recording rather than the recording itself.
PULL_UPSTREAM_FILE=index.m3u8
start_pull_upstream() { # serves a small HLS rendition for the configured pull input to dial
  local network; network="$(docker network ls --format '{{.Name}}' | grep -E '_frameworks$' | head -1)"
  [ -n "$network" ] || fail "compose network not found"
  local dir="$repo_root/.two-cell-pull-upstream"
  # A previous run's upstream (left behind by a failed proof) still has this
  # directory bind-mounted; remove it before recreating the playlist.
  docker rm -f "$PULL_UPSTREAM" >/dev/null 2>&1 || true
  rm -rf "$dir" && mkdir -p "$dir"
  docker run --rm -v "$repo_root/infrastructure/demo-recordings:/srv:ro" -v "$dir:/out" \
    --entrypoint ffmpeg "$MIST_IMAGE" -hide_banner -loglevel error \
    -i /srv/vod/202605181545117110e4095d14f6b6.mkv -t 30 -c copy -f hls -hls_time 2 -hls_list_size 0 \
    -hls_segment_filename /out/seg%03d.ts /out/index.m3u8 >/dev/null 2>&1 || true
  [ -s "$dir/index.m3u8" ] || fail "could not build the pull upstream playlist"
  docker rm -f "$PULL_UPSTREAM" >/dev/null 2>&1 || true
  docker run -d --name "$PULL_UPSTREAM" --network "$network" \
    -v "$dir:/usr/share/nginx/html:ro" nginx:1.30.1-alpine >/dev/null
  wait_for 60 "the pull upstream file server" docker run --rm --network "$network" \
    --entrypoint curl "$MIST_IMAGE" -fsS -m 5 -o /dev/null "http://$PULL_UPSTREAM_HOST/$PULL_UPSTREAM_FILE"
}
media_flows() { # media_flows <playlist-url>: true when a video frame can be read
  # Runs inside the compose network: the playlist URL names an edge by its service name.
  local network; network="$(docker network ls --format '{{.Name}}' | grep -E '_frameworks$' | head -1)"
  docker run --rm --network "$network" --entrypoint ffmpeg "$MIST_IMAGE" \
    -hide_banner -loglevel error -rw_timeout 10000000 -i "$1" -map 0:v:0 -frames:v 1 -f null -
}
policy_state() { # prints "revision parentRevision rolloutStatus"
  gql 'query($scope: MediaPlacementScopeInput!){ mediaPlacementPolicy(scope:$scope){ __typename ... on MediaPlacementPolicyState { revision parentRevision rollout { status } } ... on MediaPlacementError { message } } }' '{"scope":{"kind":"TENANT"}}' \
    | jq -r '.data.mediaPlacementPolicy | if .__typename=="MediaPlacementPolicyState" then "\(.revision) \(.parentRevision) \(.rollout.status)" else error(.message) end'
}
apply_policy() { # apply_policy <INGEST|SERVE> <rules-json>
  local verb=$1 rules=$2 state rev parent review token acks
  state="$(policy_state)"; rev="${state%% *}"; parent="$(echo "$state" | awk '{print $2}')"
  review="$(gql 'query($input: ReviewMediaPlacementChangeInput!){ reviewMediaPlacementChange(input:$input){ __typename ... on MediaPlacementReview { reviewToken differences { path } warnings { id acknowledgementRequired } } ... on MediaPlacementError { message code fields { path } } } }' \
    "$(jq -cn --arg rev "$rev" --arg parent "$parent" --arg verb "$verb" --argjson rules "$rules" '{input:{scope:{kind:"TENANT"},expectedRevision:$rev,expectedParentRevision:$parent,updates:[{verb:$verb,kind:"SET",rules:$rules}]}}')")"
  # Reviewing rules identical to the saved policy is rejected as a no-op change
  # (INVALID_INPUT without field errors); that means the desired policy is already saved.
  if [ "$(echo "$review" | jq -r '.data.reviewMediaPlacementChange | select(.__typename=="MediaPlacementError") | "\(.code) \(.fields|length)"')" = "INVALID_INPUT 0" ]; then
    log "$verb policy already saved at revision $rev; waiting for it to be effective"
  else
  token="$(echo "$review" | jq -r '.data.reviewMediaPlacementChange | if .__typename=="MediaPlacementReview" then .reviewToken else error(.message) end')"
  acks="$(echo "$review" | jq -c '[.data.reviewMediaPlacementChange.warnings[].id]')"
  gql 'mutation($input: ApplyMediaPlacementChangeInput!){ applyMediaPlacementChange(input:$input){ __typename ... on MediaPlacementChange { revision rollout { status } } ... on MediaPlacementError { message } } }' \
    "$(jq -cn --arg rev "$rev" --arg parent "$parent" --arg token "$token" --arg key "two-cell-$(date +%s%N)" --arg verb "$verb" --argjson rules "$rules" --argjson acks "$acks" '{input:{scope:{kind:"TENANT"},expectedRevision:$rev,expectedParentRevision:$parent,updates:[{verb:$verb,kind:"SET",rules:$rules}],reviewToken:$token,idempotencyKey:$key,acknowledgedWarningIds:$acks}}')" \
    | jq -r '.data.applyMediaPlacementChange | if .__typename=="MediaPlacementChange" then "applied revision \(.revision) rollout \(.rollout.status)" else error(.message) end'
  fi
  # Activation needs every cell to acknowledge the delivery; the proof asserts placement
  # behaviour, not delivery latency, so allow for a busy dev host (image rebuilds and
  # transcodes have starved the dev Postgres for minutes during this proof).
  wait_for 420 "placement rollout to become effective" bash -c "$(declare -f gql policy_state); BRIDGE=$BRIDGE API_TOKEN=$API_TOKEN; [ \"\$(policy_state | awk '{print \$3}')\" = EFFECTIVE ]"
}
apply_serve_policy() { apply_policy SERVE "$1"; }
apply_ingest_policy() { apply_policy INGEST "$1"; }
PREFER_B_FALLBACK_A='{"schemaVersion":1,"constraints":{"deny":[]},"preferences":{"groups":[{"id":"own","match":{"clusterIds":["demo-selfhosted"]},"spillover":"CAPACITY_ONLY"},{"id":"platform","match":{"clusterIds":["demo-media"]}}]}}'
INGEST_A_ONLY='{"schemaVersion":1,"constraints":{"deny":[]},"preferences":{"groups":[{"id":"platform-origin","match":{"clusterIds":["demo-media"]},"spillover":"NEVER"}]}}'
PRIVATE_ONLY='{"schemaVersion":1,"constraints":{"allow":{"any":[{"clusterIds":["demo-selfhosted"]}]},"deny":[]},"preferences":{"groups":[{"id":"own","match":{"clusterIds":["demo-selfhosted"]},"spillover":"NEVER"}]}}'
# One all-matching group (a selector without fields matches everything): every permitted
# node forms one pool, so a known good node is used while another is down. Conditional
# spillover cannot do that on unknown capacity, and an explicit empty group list denies all.
UNRESTRICTED='{"schemaVersion":1,"constraints":{"deny":[]},"preferences":{"groups":[{"id":"any","match":{}}]}}'

mist_a_idle() { # true once Mist A reports no active stream
  compose exec -T mistserver curl -fsS --get \
    --data-urlencode 'command={"active_streams":{"streams":["live+"],"fields":["status"],"longform":true}}' \
    http://localhost:4242/api2 2>/dev/null | jq -e '(.active_streams // {}) | length == 0' >/dev/null
}
stop_publisher() {
  docker rm -f "$PUBLISHER" >/dev/null 2>&1 || true
  # Cold-start scenarios require the previous buffer to be gone. The separate
  # fast-reconnect scenario deliberately retains that buffer and checks its PID.
  wait_for 60 "cell A to drop the previous publisher" mist_a_idle
}

publisher_generation() {
  # Registry keys and the location map are keyed by control cell, not by the
  # virtual media cluster, so match on the publisher's own node rather than on
  # a cell name this proof would otherwise have to hardcode.
  local key
  while IFS= read -r key; do
    # </dev/null matters: docker compose exec inherits this loop's stdin, and
    # without it the first iteration swallows the rest of the key list, so only
    # whichever key SCAN happened to return first is ever examined.
    compose exec -T foghorn-redis redis-cli --raw GET "$key" </dev/null | jq -er --arg playback "$PLAYBACK_ID" \
      'select(.PlaybackID == $playback) | .Locations | to_entries
       | map(select(.value.SourceActive and .value.OwnerNodeID == "edge-node-1" and (.value.SourceGeneration // "") != ""))
       | .[0].value.SourceGeneration // empty' 2>/dev/null && return 0
  done < <(compose exec -T foghorn-redis redis-cli --raw --scan --pattern '*:registry:source:*')
  return 1
}

mist_a_buffer() {
  compose exec -T mistserver curl -fsS --get \
    --data-urlencode 'command={"active_streams":{"streams":["live+"],"fields":["pid","lastms","inputs"],"longform":true}}' \
    http://localhost:4242/api2 2>/dev/null | jq -c '.active_streams // {}'
}

# wait_for runs its command in this shell, so the predicate is an ordinary
# function: wrapping it in bash -c would expand the "$@" and "$key" inside
# publisher_generation's body at quoting time instead of at call time.
publisher_generation_projected() { publisher_generation >/dev/null 2>&1; }

fast_reconnect_publisher() {
  local old_generation old_buffer old_pid old_lastms new_generation new_buffer new_pid new_lastms
  # Step 8 restarts cell B's edge and reapplies a policy immediately before this,
  # so the publisher can still be between projections when we first look. Every
  # other assertion in this proof waits; this one sampled once and failed the run
  # for a state that arrives a second or two later.
  wait_for 120 "the publisher's source generation to be projected" publisher_generation_projected
  old_generation="$(publisher_generation)" || fail "publisher has no active source generation before fast reconnect"
  old_buffer="$(mist_a_buffer)"
  old_pid="$(echo "$old_buffer" | jq -er 'to_entries | select(length == 1) | .[0].value.pid | select(. > 0)')"
  old_lastms="$(echo "$old_buffer" | jq -er 'to_entries[0].value.lastms')"
  # Interrupt only ffmpeg, not its container or Mist. The existing publisher loop
  # reconnects after three seconds, inside Mist's twelve-second resume window.
  docker exec "$PUBLISHER" pkill -TERM -x ffmpeg
  local deadline=$(( $(date +%s) + 10 ))
  while true; do
    new_generation="$(publisher_generation || true)"
    new_buffer="$(mist_a_buffer)"
    new_pid="$(echo "$new_buffer" | jq -r 'to_entries[0].value.pid // 0')"
    [ "$new_pid" = "$old_pid" ] || fail "fast reconnect replaced Mist's buffer instead of resuming it"
    new_lastms="$(echo "$new_buffer" | jq -r 'to_entries[0].value.lastms // 0')"
    if [ -n "$new_generation" ] && [ "$new_generation" != "$old_generation" ] && \
      [ "$new_lastms" -gt "$old_lastms" ] && echo "$new_buffer" | jq -e 'to_entries[0].value.inputs > 0' >/dev/null; then
      break
    fi
    [ "$(date +%s)" -lt "$deadline" ] || fail "publisher did not resume with a new generation inside the buffer window"
    sleep 0.25
  done
  wait_for 180 "cell A to place the resumed publisher on cell B" edge_location_is "$FOGHORN_A" "$EDGE_B_HOST"
  wait_for 180 "cell B to place the resumed publisher on its private edge" edge_location_is "$FOGHORN_B" "$EDGE_B_HOST"
  wait_for 180 "media at cell B after fast reconnect" media_flows "$(resolve_location "$FOGHORN_B")"
}
start_publisher() {
  stop_publisher
  local network resolved resolved_a resolved_b publish_url; network="$(docker network ls --format '{{.Name}}' | grep -E '_frameworks$' | head -1)"
  [ -n "$network" ] || fail "compose network not found"
  resolved_a="$(curl -fsS -m 20 "$FOGHORN_A/ingest/$STREAM_KEY?protocol=rtmp")"
  resolved_b="$(curl -fsS -m 20 "$FOGHORN_B/ingest/$STREAM_KEY?protocol=rtmp")"
  for resolved in "$resolved_a" "$resolved_b"; do
    [ "$(echo "$resolved" | jq -r '.primary.clusterId // empty')" = demo-media ] || fail "RTMP resolver escaped the ingest policy"
    [ "$(echo "$resolved" | jq -r '.primary.nodeId // empty')" = edge-node-1 ] || fail "RTMP resolver did not select the exact platform origin node"
  done
  publish_url="$(echo "$resolved_a" | jq -er '.primary.rtmpUrl | select(type == "string" and length > 0)')" || fail "RTMP resolver returned no publishing URL"
  [ "$publish_url" = "$(echo "$resolved_b" | jq -er '.primary.rtmpUrl')" ] || fail "control cells disagreed on the RTMP publishing destination"
  # The push is retried: a rejection while an edge is still registering must not end the
  # publisher, and step 8 relies on it reconnecting after the container is removed.
  # no_metadata: ffmpeg's FLV onMetaData becomes a one-frame JSON track in Mist whose
  # single multi-minute frame keeps the stream buffer classified DRY, so no placement
  # publisher claim ever becomes present.
  docker run -d --name "$PUBLISHER" --network "$network" -e PUBLISH_URL="$publish_url" --entrypoint sh "$MIST_IMAGE" -c \
    'ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=320x180:rate=15 -f lavfi -i sine=frequency=440:sample_rate=48000 -t 20 -c:v libx264 -g 15 -pix_fmt yuv420p -c:a aac /tmp/source.mp4 && while true; do ffmpeg -hide_banner -loglevel warning -re -stream_loop -1 -i /tmp/source.mp4 -map 0:v:0 -map 0:a:0 -c copy -flvflags no_metadata -f flv "$PUBLISH_URL"; sleep 3; done' >/dev/null
}
edge_location_is() { # edge_location_is <foghorn> <edge-host>
  local loc; loc="$(resolve_location "$1")"; [[ "$loc" == *"$2"* ]]
}
reenroll_edges() { # drop both edge registrations so recreated Helmsman containers can enroll again
  # Helmsman withholds its enrollment token once a receipt exists in its state dir; the
  # registration would otherwise be refused with ENROLLMENT_REQUIRED after the rows are gone.
  compose exec -T helmsman sh -c 'rm -rf /var/lib/frameworks/helmsman/enrollment' >/dev/null
  compose exec -T helmsman-b sh -c 'rm -rf /var/lib/frameworks/helmsman/enrollment' >/dev/null
  psql_db quartermaster "DELETE FROM quartermaster.ingress_sites WHERE node_id IN ('edge-node-1','edge-node-b'); DELETE FROM quartermaster.service_instances WHERE node_id IN ('edge-node-1','edge-node-b'); DELETE FROM quartermaster.node_fingerprints WHERE node_id IN ('edge-node-1','edge-node-b'); DELETE FROM quartermaster.infrastructure_nodes WHERE node_id IN ('edge-node-1','edge-node-b')" >/dev/null
  compose restart helmsman helmsman-b >/dev/null
  sleep 20
  compose restart foghorn foghorn-2 foghorn-b >/dev/null
  sleep 15
  compose restart helmsman helmsman-b >/dev/null
  wait_for 120 "foghorn A" http_any "$FOGHORN_A/play/$PLAYBACK_ID/hls"
  wait_for 120 "foghorn B" http_any "$FOGHORN_B/play/$PLAYBACK_ID/hls"
}

if [ "${TWO_CELL_RESUME:-}" = 1 ]; then
  log "resuming on the running, activated stack (TWO_CELL_RESUME=1)"
  if [ "${TWO_CELL_REENROLL:-}" = 1 ]; then
    log "re-enrolling edge-node-1 and edge-node-b (TWO_CELL_REENROLL=1)"
    reenroll_edges
  fi
else
log "1/9 bring up both cells (MIST_IMAGE=$MIST_IMAGE)"
# Only the services this proof needs (compose starts their dependencies); the
# website/tray images are irrelevant here and take minutes to build.
compose up -d --build --remove-orphans postgres foghorn-redis foghorn-redis-b quartermaster purser commodore bridge decklog storage-init \
  foghorn foghorn-2 foghorn-b helmsman helmsman-b mistserver mistserver-b edge-proxy-a edge-proxy-b nginx
wait_for 120 "postgres" compose exec -T postgres pg_isready -q
if ! compose exec -T postgres psql -U "$(grep '^POSTGRES_USER=' .env | cut -d'"' -f2)" -d postgres -Atc "SELECT 1 FROM pg_database WHERE datname='foghorn_b'" | grep -q 1; then
  fail "database foghorn_b is missing: the two-cell profile needs a fresh dev volume (docker compose down -v, then rerun)"
fi

log "2/9 seed the demo fixture and the second cell"
make --no-print-directory seed-demo-postgres >/dev/null
compose exec -T postgres psql -v ON_ERROR_STOP=1 -U quartermaster -d quartermaster < pkg/database/sql/seeds/demo/postgres/two-cell/quartermaster.sql >/dev/null
# The S3 descriptor one-shot only fills a cluster row that exists; on a fresh volume
# the seed creates the row after the first attempt, so establish it again now.
compose up -d --no-deps storage-init >/dev/null
# A freshly enrolled edge stays unroutable until Foghorn probes its HTTPS domain
# (not possible in dev) or the node reconnects with its resolved fingerprint; the
# Helmsman restart below is that reconnect. Give first-boot enrollment time to land.
sleep 20
compose restart foghorn foghorn-2 foghorn-b >/dev/null
sleep 15
compose restart helmsman helmsman-b >/dev/null
wait_for 120 "foghorn A" http_any "$FOGHORN_A/play/$PLAYBACK_ID/hls"
wait_for 120 "foghorn B" http_any "$FOGHORN_B/play/$PLAYBACK_ID/hls"
wait_for 120 "bridge" http_ok "$BRIDGE/health"

log "3/9 wait for both cells to attest placement enforcement and for the tenant to receive schema-2 authority"
wait_for 240 "cell attestations" bash -c "[ \"\$(docker compose --profile two-cell exec -T postgres psql -U commodore -d commodore -Atc \"SELECT count(*) FROM commodore.media_cell_placement_capabilities WHERE enforcement_ready AND cell_id IN ('central-primary','us-primary')\")\" = 2 ]"
wait_for 240 "schema-2 tenant authority" bash -c "[ \"\$(docker compose --profile two-cell exec -T postgres psql -U commodore -d commodore -Atc \"SELECT v.payload_schema_version FROM commodore.media_authority_current c JOIN commodore.media_authority_versions v ON v.authority_kind=c.authority_kind AND v.authority_id=c.authority_id AND v.authority_version=c.authority_version WHERE c.authority_kind='tenant' AND c.authority_id='$TENANT_ID'\")\" = 2 ]"
psql_db commodore "SELECT cell_id, enforcement_ready, live_replicas, attested_at FROM commodore.media_cell_placement_capabilities ORDER BY cell_id"
fi

# Cross-cell placement needs each cell's Foghorn leader to have published its Quartermaster
# peer snapshot with the other cell's control-cell address. The leader refreshes that snapshot
# on acquiring leadership and every five minutes; a peer whose Foghorn was not yet healthy at
# the first refresh becomes addressable only on the next one.
peer_hints_ready() { # peer_hints_ready <redis-container> <peer-cell-id>
  local key; key="$(docker exec "$1" redis-cli --scan --pattern '*peer_hints*' 2>/dev/null | head -1)"
  [ -n "$key" ] && docker exec "$1" redis-cli get "$key" 2>/dev/null | grep -q "\"control_cell_id\":\"$2\""
}
wait_for 420 "cell A to learn cell B's Foghorn address" peer_hints_ready frameworks-foghorn-redis us-primary
wait_for 420 "cell B to learn cell A's Foghorn address" peer_hints_ready frameworks-foghorn-redis-b central-primary

log "4/9 prefer the private cell for viewers, spilling to the platform cluster only on capacity"
apply_serve_policy "$PREFER_B_FALLBACK_A"
apply_ingest_policy "$INGEST_A_ONLY"

log "5/9 resolve ingest through both cells and publish into cell A; viewers arriving at either cell must land on the private edge in cell B with real media"
start_publisher
# The policy prefers the private cluster, so cell A must hand its viewer to cell B's edge
# (cross-cell discovery) rather than substituting its own local node.
wait_for 120 "cell A to place its viewer on the private edge" edge_location_is "$FOGHORN_A" "$EDGE_B_HOST"
wait_for 120 "cell B to prepare its own edge for the viewer" edge_location_is "$FOGHORN_B" "$EDGE_B_HOST"
location_b="$(resolve_location "$FOGHORN_B")"
log "viewer via A -> $(resolve_location "$FOGHORN_A")"
log "viewer via B -> $location_b"
wait_for 180 "media segments at cell B" media_flows "$location_b"
if compose logs --since 5m foghorn-b 2>/dev/null | grep -qiE 'origin pull|prepared|dtsc'; then log "foghorn-b arranged the source pull for edge-node-b"; fi

log "6/9 a configured pull input is placed and served like a pushed stream"
# The upstream is a plain file server, not another Mist: a media cell's own output
# is itself placement-governed, so pointing the input at it would test the policy
# against itself rather than testing a configured input. Nothing pushes into this
# stream — the destination Mist dials the upstream once placement permits it.
start_pull_upstream
pull_created="$(gql 'mutation($input: CreateStreamInput!){ createStream(input:$input){ __typename ... on Stream { streamId playbackId ingestMode } ... on ValidationError { message } ... on AuthError { message } } }' \
  "$(jq -cn --arg uri "http://$PULL_UPSTREAM_HOST/$PULL_UPSTREAM_FILE" '{input:{name:"two-cell pull proof",ingestMode:"PULL",pullSource:{sourceUri:$uri,enabled:true}}}')")"
PULL_PLAYBACK_ID="$(echo "$pull_created" | jq -r '.data.createStream | if .__typename=="Stream" then .playbackId else error(.message) end')"
[ -n "$PULL_PLAYBACK_ID" ] && [ "$PULL_PLAYBACK_ID" != null ] || fail "pull stream creation returned no playback id: $pull_created"
log "pull stream $PULL_PLAYBACK_ID created against http://$PULL_UPSTREAM_HOST/$PULL_UPSTREAM_FILE"
# Its signed source authority has to reach the cells before any destination may be
# prepared for it, so allow the same activation budget the policy path uses.
wait_for 420 "a viewer destination for the configured pull input" bash -c "[ \"\$(curl -s -m 10 -o /dev/null -w '%{http_code}' $FOGHORN_A/play/$PULL_PLAYBACK_ID/hls)\" = 307 ]"
# The serving policy in force still prefers the private cluster, and a configured
# input is placed by the same policy as a pushed one. Asserting only that some
# destination answered would pass even if the pull were served from whichever
# cell happened to dial it, which is the thing this step exists to rule out.
wait_for 180 "the configured pull input to be served from the preferred private edge" pull_location_is "$FOGHORN_A" "$EDGE_B_HOST"
pull_viewer_location="$(pull_location "$FOGHORN_A")"
log "pull viewer via A -> $pull_viewer_location"
wait_for 180 "media segments for the configured pull input" media_flows "$pull_viewer_location"

log "7/9 private-only policy with the private cell down refuses instead of silently spilling"
apply_serve_policy "$PRIVATE_ONLY"
compose stop edge-proxy-b mistserver-b helmsman-b >/dev/null
wait_for 180 "cell B to be refused" resolve_refused "$FOGHORN_B"
resolve_refused "$FOGHORN_A" || fail "cell A did not refuse a private-only viewer with a placement refusal: $(resolve_status "$FOGHORN_A") $(resolve_body "$FOGHORN_A")"
# Stored media obeys the same policy. The seeded VOD's bytes are warm on cell A's
# platform edge, which this policy does not permit, so having the bytes locally
# must not serve them.
wait_for 120 "stored media to be refused under a private-only policy" stored_refused "$FOGHORN_A"

log "8/9 fallback while the private edge is down: capacity-only spill refuses (its capacity is unknown, not exhausted); an unrestricted policy serves from the platform edge"
apply_serve_policy "$PREFER_B_FALLBACK_A"
# A down node has unknown capacity; conditional spillover needs known exhaustion.
wait_for 120 "capacity-only preference to refuse while the private edge is down" resolve_refused "$FOGHORN_B"
resolve_refused "$FOGHORN_A" || fail "cell A spilled a capacity-only preference onto platform capacity while the private node's capacity was unknown: $(resolve_status "$FOGHORN_A") $(resolve_body "$FOGHORN_A")"
apply_serve_policy "$UNRESTRICTED"
wait_for 120 "unrestricted policy to use cell A's edge" edge_location_is "$FOGHORN_B" "$EDGE_A_HOST"
edge_location_is "$FOGHORN_A" "$EDGE_A_HOST" || fail "cell A did not serve its own permitted edge under an unrestricted policy"
wait_for 60 "media at the fallback edge" media_flows "$(resolve_location "$FOGHORN_B")"
# The same stored media the private-only policy refused is now permitted, and its
# storage affinity still decides which destination serves the bytes.
wait_for 120 "stored media to be permitted again" bash -c "[ \"\$(curl -s -m 10 -o /dev/null -w '%{http_code}' $FOGHORN_A/play/$STORED_PLAYBACK_ID/hls)\" = 307 ]"
stored_media_location="$(stored_location "$FOGHORN_A")"
[[ "$stored_media_location" == *"$EDGE_A_HOST"* ]] || fail "stored media left the edge holding its bytes: $stored_media_location"
wait_for 60 "media segments for stored media" media_flows "$stored_media_location"
compose start helmsman-b mistserver-b edge-proxy-b >/dev/null
apply_serve_policy "$PREFER_B_FALLBACK_A"
wait_for 180 "cell B back in service with the private edge preferred again" edge_location_is "$FOGHORN_B" "$EDGE_B_HOST"

log "9/9 publisher reconnect, edge/Foghorn restarts and control-plane outage"
fast_reconnect_publisher
start_publisher
wait_for 120 "the reconnected publisher to be placed from cell A onto the private edge" edge_location_is "$FOGHORN_A" "$EDGE_B_HOST"
# Readiness must survive control-channel churn: Mist keeps serving, Helmsman's next report
# carries Mist's buffer level, and Foghorn's state is in Redis. Neither restart may leave the
# live publisher unplaceable.
compose restart helmsman >/dev/null
wait_for 120 "placement to hold across a Helmsman restart" edge_location_is "$FOGHORN_A" "$EDGE_B_HOST"
compose restart foghorn foghorn-2 >/dev/null
wait_for 180 "placement to hold across a cell A Foghorn restart" edge_location_is "$FOGHORN_A" "$EDGE_B_HOST"
wait_for 180 "media at cell B after the restarts" media_flows "$(resolve_location "$FOGHORN_B")"
wait_for 120 "cell B serving the reconnected publisher" edge_location_is "$FOGHORN_B" "$EDGE_B_HOST"
wait_for 180 "media at cell B after reconnect" media_flows "$(resolve_location "$FOGHORN_B")"
compose stop commodore >/dev/null
sleep 5
edge_location_is "$FOGHORN_B" "$EDGE_B_HOST" || { compose start commodore >/dev/null; fail "cell B stopped serving during a control-plane outage"; }
wait_for 60 "media at cell B during the outage" media_flows "$(resolve_location "$FOGHORN_B")"
compose start commodore >/dev/null
wait_for 120 "commodore back" http_ok "$BRIDGE/health"
docker rm -f "$PUBLISHER" "$PULL_UPSTREAM" >/dev/null 2>&1 || true

log "PASS: two-cell media proof complete (publish A -> viewer B media, configured pull input, stored-media policy, private-only refusal, capacity fallback, reconnect, outage)"
