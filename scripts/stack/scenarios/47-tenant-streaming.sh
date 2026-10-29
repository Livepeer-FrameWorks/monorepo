#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, creates a tenant and enrolls a disposable private edge
# A normal Free tenant publishes RTMP and SRT on platform capacity and its own
# newly created private cluster. No seeded tenant, cluster health override,
# operator billing grant or synthetic publisher trigger may supply admission.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 docker openssl psql || finish
[ -S /var/run/docker.sock ] || { blocked 'private edge enrollment needs the stack Docker socket'; finish; }
run="tenant47-$(date +%s)-$RANDOM"
email=$(test_email "$run")
password="Stack-$(openssl rand -hex 8)"
cookies="$STACK_STATE_DIR/$run.cookies"
tenant="" stream="" publisher="" edge="$COMPOSE_PROJECT_NAME-$run" proxy="$COMPOSE_PROJECT_NAME-$run-proxy"
cleanup() {
  [ "${cleaned:-0}" = 0 ] || return 0
  cleaned=1
  [ -z "$publisher" ] || kill "$publisher" 2>/dev/null
  [ -z "$stream" ] || delete_stream "$stream"
  docker logs "$edge" >"$STACK_STATE_DIR/$run-edge.log" 2>&1 || true
  docker rm -fv "$proxy" "$edge" >/dev/null 2>&1 || true
  # The account is disposable with this stack's volumes, like scenario 08.
  # Streams and containers are removed while the rest of the suite runs.
  return 0
}
end_scenario() { cleanup || fail 'disposable tenant cleanup failed'; finish; }
on_exit() { local rc=$?; cleanup || rc=1; exit "$rc"; }
trap on_exit EXIT

log 'sign up, verify email and log in as an ordinary tenant'
code=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" \
  '{email:$e,password:$p,first_name:"Stack",last_name:"Streaming"} + $b' |
  curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/register")
check 'tenant registration accepted' [ "$code" = 201 ]
[ "$code" = 201 ] || end_scenario
eventually 60 'verification email delivered' mail_seen "$email" || end_scenario
verification=$(mail_token "$email")
code=$(jq -cn --arg t "$verification" '{token:$t}' |
  curl -s -m 20 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/verify")
check 'verification accepted' [ "$code" = 200 ]
code=$(jq -cn --arg e "$email" --arg p "$password" --argjson b "$(bot_fields)" '{email:$e,password:$p} + $b' |
  curl -s -m 20 -c "$cookies" -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login")
check 'tenant login accepted' [ "$code" = 200 ]
STACK_API_TOKEN=$(awk '$6=="access_token"{print $7}' "$cookies" | tail -1)
[ -n "$STACK_API_TOKEN" ] || { fail 'login returned no session'; end_scenario; }
tenant=$(python3 - "$STACK_API_TOKEN" <<'PY'
import base64,json,sys
p=sys.argv[1].split('.')[1]
print(json.loads(base64.urlsafe_b64decode(p+'='*(-len(p)%4)))['tenant_id'])
PY
)
export STACK_API_TOKEN STACK_TENANT_ID="$tenant"
free_plan() { [ "$(pg purser "SELECT bt.tier_name FROM purser.tenant_subscriptions ts JOIN purser.billing_tiers bt ON bt.id=ts.tier_id WHERE ts.tenant_id='$tenant' AND ts.status='active'")" = free ]; }
eventually 90 'ordinary signup has an active Free plan' free_plan || end_scenario

# Constrain both publishing and viewing through the user-facing review/apply
# contract. A successful private test must not silently use a platform edge.
set_placement() {
  local class=$1 policy revision parent updates review input applied warnings token
  scope=$(jq -cn --arg id "$2" '{kind:"STREAM",streamId:$id}')
  policy=$(gql 'query($s:MediaPlacementScopeInput!){mediaPlacementPolicy(scope:$s){__typename ... on MediaPlacementPolicyState{revision parentRevision}}}' "$(jq -cn --argjson s "$scope" '{s:$s}')")
  revision=$(echo "$policy" | jq -r '.data.mediaPlacementPolicy.revision // empty')
  parent=$(echo "$policy" | jq -r '.data.mediaPlacementPolicy.parentRevision // empty')
  [ -n "$revision" ] && [ -n "$parent" ] || { echo "$policy"; return 1; }
  updates=$(jq -cn --arg c "$class" '["INGEST","SERVE"]|map({verb:.,kind:"SET",rules:{schemaVersion:1,constraints:{allow:{any:[{classes:[$c]}]}}}})')
  input=$(jq -cn --argjson s "$scope" --arg r "$revision" --arg p "$parent" --argjson u "$updates" \
    '{scope:$s,expectedRevision:$r,expectedParentRevision:$p,updates:$u}')
  review=$(gql 'query($i:ReviewMediaPlacementChangeInput!){reviewMediaPlacementChange(input:$i){__typename ... on MediaPlacementReview{reviewToken warnings{id acknowledgementRequired}} ... on MediaPlacementError{message}}}' "$(jq -cn --argjson i "$input" '{i:$i}')")
  token=$(echo "$review" | jq -r '.data.reviewMediaPlacementChange.reviewToken // empty')
  [ -n "$token" ] || { echo "$review"; return 1; }
  warnings=$(echo "$review" | jq -c '[.data.reviewMediaPlacementChange.warnings[]?|select(.acknowledgementRequired)|.id]')
  input=$(echo "$input" | jq -c --arg t "$token" --arg k "$run-$class-$(date +%s)" --argjson w "$warnings" '.+{reviewToken:$t,idempotencyKey:$k,acknowledgedWarningIds:$w}')
  applied=$(gql 'mutation($i:ApplyMediaPlacementChangeInput!){applyMediaPlacementChange(input:$i){__typename ... on MediaPlacementChange{revision} ... on MediaPlacementError{message}}}' "$(jq -cn --argjson i "$input" '{i:$i}')")
  json_has "$applied" '.data.applyMediaPlacementChange.__typename == "MediaPlacementChange"' || { echo "$applied"; return 1; }
  policy_revision=$(echo "$applied" | jq -r '.data.applyMediaPlacementChange.revision')
  policy_parent=$parent
}
stream_ended() { [ "$(pg "$1" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$tenant' AND stream_internal_name IN ('$internal','live+$internal') AND ended_at IS NULL")" = 0 ]; }
stream_active() { [ "$(pg "$1" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$tenant' AND stream_internal_name IN ('$internal','live+$internal') AND ended_at IS NULL AND projection_state='active' AND node_id='$2' AND ingest_cluster_id='$3'")" = 1 ]; }
policy_effective() {
  local policy
  policy=$(gql 'query($s:MediaPlacementScopeInput!){mediaPlacementPolicy(scope:$s){... on MediaPlacementPolicyState{revision activeRevision parentRevision activeParentRevision rollout{status requiredRecipients appliedRecipients}}}}' "$(jq -cn --argjson s "$scope" '{s:$s}')")
  json_has "$policy" '.data.mediaPlacementPolicy | .rollout.status == "EFFECTIVE" and .revision == .activeRevision and .parentRevision == .activeParentRevision and .rollout.requiredRecipients == .rollout.appliedRecipients'
}
preview_ready() {
  local preview
  preview=$(gql 'query($i:PreviewMediaPlacementInput!){previewMediaPlacement(input:$i){__typename ... on MediaPlacementPreview{complete selected{nodeId clusterId} reason} ... on MediaPlacementError{message}}}' \
    "$(jq -cn --argjson s "$scope" --arg p "$1" --arg r "$policy_revision" --arg a "$policy_parent" '{i:{scope:$s,verb:"INGEST",protocol:$p,expectedRevision:$r,expectedParentRevision:$a}}')")
  printf '%s\n' "$preview" >"$STACK_STATE_DIR/$run-preview.json"
  json_has "$preview" '.data.previewMediaPlacement | .__typename == "MediaPlacementPreview" and .complete and .selected.clusterId != null' || return 1
  # Platform node identities are intentionally hidden from ordinary tenants.
  if [ "$2" = TENANT_PRIVATE ]; then
    [ "$(echo "$preview" | jq -r '.data.previewMediaPlacement.selected.nodeId')" = "$3" ]
  fi
}
private_media_at() {
  local location
  location=$(location_at "$1" "$2")
  [ -n "$location" ] || return 1
  python3 - "$location" "$3:8082" <<'PY'
import sys, urllib.parse, urllib.request

url, expected_host = sys.argv[1:]
try:
    for _ in range(4):
        if urllib.parse.urlsplit(url).netloc != expected_host:
            sys.exit(1)
        with urllib.request.urlopen(url, timeout=10) as response:
            if response.status != 200 or urllib.parse.urlsplit(response.geturl()).netloc != expected_host:
                sys.exit(1)
            body = response.read()
        if urllib.parse.urlsplit(url).path.endswith('.m3u8'):
            entries = [line.strip() for line in body.decode().splitlines() if line.strip() and not line.startswith('#')]
            if not entries:
                sys.exit(1)
            url = urllib.parse.urljoin(url, entries[0])
            continue
        valid = len(body) >= 3 * 188 and all(body[offset] == 0x47 for offset in (0, 188, 376))
        sys.exit(0 if valid else 1)
except Exception:
    sys.exit(1)
sys.exit(1)
PY
}
stream_flow() {
  local class=$1 host=$2 cell=$3 node=$4 cluster=$5 protocol=$6 base=$7 s pb key internal uuid viewer
  s=$(create_stream "stack-$run-$class-$protocol" false)
  stream=$(echo "$s" | jq -r '.id // empty')
  pb=$(echo "$s" | jq -r '.playbackId // empty')
  key=$(echo "$s" | jq -r '.streamKey // empty')
  uuid=$(echo "$s" | jq -r '.streamId // empty')
  [ -n "$stream" ] || { fail 'create stream as normal tenant'; return 1; }
  internal=$(internal_name_of "$uuid")
  if ! set_placement "$class" "$uuid"; then fail "$class $protocol placement policy applies"; return 1; fi
  pass "$class $protocol placement policy applies"
  eventually 90 "$class $protocol placement revision is effective" policy_effective || return 1
  eventually 60 "$class $protocol preview has fresh placement evidence" preview_ready "$protocol" "$class" "$node" || return 1
  if [ "$protocol" = rtmp ]; then
    publisher=$(publish "rtmp://$host:1935/live" "$key" 320x240 10 600)
  else
    ffmpeg -hide_banner -loglevel error -re -f lavfi -i 'testsrc2=size=320x240:rate=10' \
      -f lavfi -i 'sine=frequency=440:sample_rate=48000' -t 600 -c:v libx264 -preset veryfast -g 20 \
      -pix_fmt yuv420p -c:a aac -b:a 96k -f mpegts "srt://$host:8889?mode=caller&streamid=$key" \
      >"$STACK_STATE_DIR/$run-srt.log" 2>&1 &
    publisher=$!
  fi
  eventually 90 "$class $protocol first publisher admits to the expected node and cluster" stream_active "$cell" "$node" "$cluster" || return 1
  if [ "$class" = TENANT_PRIVATE ]; then
    for viewer in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
      eventually 60 "$class $protocol HLS manifests and media via $viewer stay on the private edge" private_media_at "$viewer" "$pb" "$host" || return 1
    done
  else
    eventually 60 "$class $protocol serves real HLS media" media_at "$base" "$pb" || return 1
  fi
  check "$class $protocol publisher stays connected" kill -0 "$publisher"
  kill "$publisher" 2>/dev/null
  publisher=""
  eventually 90 "$class $protocol session ends on disconnect" stream_ended "$cell" || return 1
  delete_stream "$stream"
  stream=""
}

log 'normal Free tenant publishes on platform capacity'
platform_node=$(pg quartermaster "SELECT node_id FROM quartermaster.infrastructure_nodes WHERE cluster_id='$CELL_A_CLUSTER' AND node_type='edge' AND status='active' ORDER BY node_id LIMIT 1")
for protocol in rtmp srt; do
  stream_flow PLATFORM_OFFICIAL "$EDGE_A_HOST" "$FOGHORN_A_DB" "$platform_node" "$CELL_A_CLUSTER" "$protocol" "${FOGHORN_A_URLS%% *}" || end_scenario
done

log 'create private cluster through the tenant API and enroll a fresh edge'
control_cluster=$(pg quartermaster "SELECT COALESCE(NULLIF(control_cell_id,''),NULLIF(cell_id,''),cluster_id) FROM quartermaster.infrastructure_clusters WHERE cluster_id='$CELL_A_CLUSTER'")
created=$(gql 'mutation($i:CreateEdgeClusterInput!){createEdgeCluster(input:$i){__typename ... on CreateEdgeClusterResponse{cluster{id clusterId healthStatus} bootstrapToken{token}} ... on ValidationError{message}}}' \
  "$(jq -cn --arg n "$run" --arg c "$control_cluster" '{i:{clusterName:$n,controlClusterId:$c}}')")
cluster=$(echo "$created" | jq -r '.data.createEdgeCluster.cluster.clusterId // empty')
enrollment=$(echo "$created" | jq -r '.data.createEdgeCluster.bootstrapToken.token // empty')
[ -n "$cluster" ] && [ -n "$enrollment" ] || { fail "create private cluster: $(echo "$created" | jq -c 'del(.data.createEdgeCluster.bootstrapToken)')"; end_scenario; }
check 'tenant-created cluster starts unknown' json_has "$created" '.data.createEdgeCluster.cluster.healthStatus == "unknown"'
network=$(docker inspect -f '{{range $name,$config := .NetworkSettings.Networks}}{{$name}}{{end}}' "$(container_of edge)")
image=$(docker inspect -f '{{.Image}}' "$(container_of edge)")
proxy_image=$(docker inspect -f '{{.Image}}' "$(container_of edge-proxy-a)")
node="$run-edge"
# Use the official edge image and independent identity/config/state. The HTTP
# proxy is the same loopback-Mist adapter as edge-proxy-a, since the stack does
# not run public DNS/Navigator certificates.
mist_password=$(openssl rand -hex 16)
docker create --name "$edge" --hostname "$node" --network "$network" --network-alias "$edge" \
  --shm-size 1g --memory 1536m --cpus 2 \
  -e EDGE_DOMAIN=localhost -e NODE_ID="$node" -e EDGE_ENROLLMENT_TOKEN="$enrollment" \
  -e FOGHORN_CONTROL_ADDR=foghorn:18029,foghorn-2:18029 -e EDGE_PUBLIC_URL="http://$edge:8082" \
  -e MIST_API_USERNAME=frameworks -e MIST_API_PASSWORD="$mist_password" -e PORT=18007 -e LOG_LEVEL=info \
  -e GRPC_ALLOW_INSECURE=true \
  -e MISTSERVER_URL=http://localhost:4242 -e MISTSERVER_HTTP_URL=http://localhost:8080 \
  -e HELMSMAN_WEBHOOK_URL=http://localhost:18007 "$image" >/dev/null || { fail 'create private edge'; end_scenario; }
docker cp "$STACK_REPO/infrastructure/two-cell/mistserver-b.conf" "$edge:/etc/frameworks/mistserver.seed.conf" || { fail 'seed Mist protocols'; end_scenario; }
docker create --name "$proxy" --network "container:$edge" --cpus 1 --memory 128m \
  -e NGINX_ENTRYPOINT_WORKER_PROCESSES_AUTOTUNE=1 "$proxy_image" >/dev/null || { fail 'create private edge HTTP proxy'; end_scenario; }
docker cp "$STACK_REPO/infrastructure/two-cell/edge-a.conf" "$proxy:/etc/nginx/conf.d/default.conf" || { fail 'configure private edge HTTP proxy'; end_scenario; }
docker start "$edge" "$proxy" >/dev/null || { fail 'start private edge'; end_scenario; }
# Match up.sh's enrollment transport: the stack has no public DNS/TLS issuer,
# so reconnect the enrolled identity after ConfigSeed, before testing media.
private_seeded() { docker exec "$edge" test -s /data/state/config-seed.pb; }
# Foghorn refreshes served-cluster assignments every five minutes; enrollment
# retries and ConfigSeed delivery need another bounded window after that tick.
eventually 420 'private edge enrolls and persists ConfigSeed' private_seeded || end_scenario
docker exec "$edge" /command/s6-svc -r /run/service/helmsman || { fail 'reconnect enrolled private edge'; end_scenario; }
private_ready() {
  [ "$(pg quartermaster "SELECT count(*) FROM quartermaster.infrastructure_clusters c JOIN quartermaster.infrastructure_nodes n ON n.cluster_id=c.cluster_id WHERE c.owner_tenant_id='$tenant' AND c.cluster_id='$cluster' AND c.health_status='healthy' AND n.node_id='$node' AND n.status='active'")" = 1 ]
}
eventually 180 'fresh private edge reports ready without a health override' private_ready || end_scenario
for protocol in rtmp srt; do
  stream_flow TENANT_PRIVATE "$edge" "$FOGHORN_A_DB" "$node" "$cluster" "$protocol" "${FOGHORN_A_URLS%% *}" || end_scenario
done
check 'streaming did not change the Free plan' free_plan
end_scenario
