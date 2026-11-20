#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, enrolls a disposable edge, changes capacity and blocks its S3 uploads
# The round trip uses the tenant API and Quartermaster RPC; inventory SQL is
# read-only. Live reports must agree without changing the controller process.
. "$(dirname "$0")/../lib.sh"
need ffmpeg jq curl python3 docker openssl psql timeout || finish
[ -S /var/run/docker.sock ] || { blocked 'capacity round trip needs the stack Docker socket'; finish; }
foreign_token=$(session_token) || { fail 'sign in as the unrelated demo tenant'; finish; }
run="capacity70-$(date +%s)-$RANDOM"
export STACK_STATE_DIR="$STACK_STATE_DIR/$run"
mkdir -p "$STACK_STATE_DIR"
email=$(test_email "$run")
password="Stack-$(openssl rand -hex 8)"
cookies="$STACK_STATE_DIR/$run.cookies"
tenant='' stream='' publisher='' dvr_hash=''
fault_started=0
other_mode_node='' other_mode=''
edge="$COMPOSE_PROJECT_NAME-$run" proxy="$COMPOSE_PROJECT_NAME-$run-proxy"
cleanup() {
  [ "${cleaned:-0}" = 0 ] || return 0
  cleaned=1
  [ "$fault_started" = 0 ] || netfault_stop "$run"
  if [ -n "$other_mode_node" ] && [ -n "$other_mode" ]; then
    rpc "foghorn:${FOGHORN_INTERNAL_GRPC_PORT:-18019}" foghorn.NodeControlService/SetNodeOperationalMode \
      "$(jq -cn --arg n "$other_mode_node" --arg m "$other_mode" '{nodeId:$n,mode:$m,setBy:"stack-capacity-cleanup"}')" "$SERVICE_TOKEN" >/dev/null || fail 'restore the fixture edge mode'
  fi
  [ -z "$publisher" ] || kill "$publisher" 2>/dev/null || true
  [ -z "$stream" ] || delete_stream "$stream"
  [ -z "$dvr_hash" ] || gql 'mutation($id:ID!){deleteDVR(dvrHash:$id){__typename}}' "$(jq -cn --arg id "$dvr_hash" '{id:$id}')" >/dev/null
  docker logs "$edge" >"$STACK_STATE_DIR/edge.log" 2>&1 || true
  docker rm -fv "$proxy" "$edge" >/dev/null 2>&1 || true
}
end_scenario() { cleanup; finish; }
trap cleanup EXIT
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
STACK_API_TOKEN=$(refresh_session_token "$cookies") || { fail 'refresh the owner session after enrollment'; end_scenario; }
export STACK_API_TOKEN
foreign_token=$(session_token) || { fail 'refresh the unrelated demo session'; end_scenario; }

node_id=$(printf 'InfrastructureNode:%s' "$node" | base64 | tr -d '\n')
capacity_query='query($id:ID!){node(id:$id){... on InfrastructureNode{capacity{egressBudgetBytesPerSec ingressBudgetBytesPerSec egressSource ingressSource revision appliedRevision}}}}'
capacity_vars=$(jq -cn --arg id "$node_id" '{id:$id}')
read_capacity() { gql "$capacity_query" "$capacity_vars"; }
capacity_matches() {
  read_capacity | jq -e --argjson e "$1" --argjson i "$2" --argjson r "$3" --arg s "$4" \
    '.errors == null and (.data.node.capacity | .egressBudgetBytesPerSec==$e and .ingressBudgetBytesPerSec==$i and .revision==$r and .appliedRevision==$r and .egressSource==$s)' >/dev/null
}
eventually 90 'new edge reports the visible 1 Gbit/s default' capacity_matches 125000000 125000000 0 DEFAULT || end_scenario
controller_identity() { docker exec "$edge" ps -C MistController -o pid=,lstart=; }
controller_before=$(controller_identity)
[ -n "$controller_before" ] || { fail 'Mist controller process is readable'; end_scenario; }
set_query='mutation($i:SetNodeCapacityInput!){setNodeCapacity(input:$i){__typename ... on InfrastructureNode{capacity{revision}} ... on ValidationError{message} ... on AuthError{message}}}'
set_vars=$(jq -cn --arg node "$node" '{i:{nodeId:$node,egressBudgetBytesPerSec:250000000,ingressBudgetBytesPerSec:62500000,expectedRevision:0}}')
changed=$(gql "$set_query" "$set_vars")
check 'owner writes separate egress and ingress budgets' json_has "$changed" '.errors == null and .data.setNodeCapacity.__typename=="InfrastructureNode" and .data.setNodeCapacity.capacity.revision==1'
eventually 60 'Helmsman applies the owner budget live' capacity_matches 250000000 62500000 1 OPERATOR || end_scenario
refused=$(gql "$set_query" "$set_vars" "$foreign_token")
check 'another tenant cannot change the node budget' json_has "$refused" '(.errors|length)>0 or (.data.setNodeCapacity.__typename=="AuthError")'
stale=$(gql "$set_query" "$set_vars")
check 'stale revision cannot overwrite the owner budget' json_has "$stale" '.data.setNodeCapacity.__typename=="ValidationError"'

# Mist's API is loopback-only. Send the challenge response and read its stored
# bandwidth object from inside this one disposable edge.
mist_budget() {
  MIST_TEST_PASSWORD="$mist_password" python3 - "$edge" <<'PY'
import hashlib, json, os, subprocess, sys
edge = sys.argv[1]
def call(command):
    result = subprocess.run(['docker', 'exec', edge, 'curl', '-fsS', '-m', '5', '--get',
                             '--data-urlencode', 'command='+json.dumps(command),
                             'http://localhost:4242/api2'], capture_output=True, check=True, text=True)
    return json.loads(result.stdout)
response = call({'authorize': {'username': 'frameworks', 'password': ''}})
auth = response.get('authorize', {})
if auth.get('status') == 'CHALL':
    password = hashlib.md5(os.environ['MIST_TEST_PASSWORD'].encode()).hexdigest()
    answer = hashlib.md5((password+auth['challenge']).encode()).hexdigest()
    response = call({'authorize': {'username': 'frameworks', 'password': answer}})
assert response.get('authorize', {}).get('status') == 'OK', 'Mist authentication failed'
print(call({'bandwidth': {}})['bandwidth']['limit'])
PY
}
mist_matches() { [ "$(mist_budget)" = "$1" ]; }
eventually 30 'Mist stores the same egress budget' mist_matches 250000000 || end_scenario

# grpcurl is supplied by Commodore's image; credentials stay in stdin rather
# than the docker exec argument list.
rpc() {
  local address=$1 method=$2 body=$3 credential=${4:-$STACK_API_TOKEN}
  printf '%s\n' "$credential" | stack_exec commodore sh -c '
    read -r token
    grpcurl -plaintext -max-time 10 -H "authorization: Bearer $token" -d "$3" "$1" "$2"
  ' sh "$address" "$method" "$body"
}
node_body=$(jq -cn --arg id "$node" '{nodeId:$id}')
node_health() { rpc "foghorn:${FOGHORN_INTERNAL_GRPC_PORT:-18019}" foghorn.NodeControlService/GetNodeHealth "$node_body" "$SERVICE_TOKEN"; }
health_matches() {
  node_health | jq -e --argjson b "$1" --arg r "$2" \
    '.isHealthy and .bwLimitMbps==$b and .egressUsableMbps==($b*0.85) and .capacityRevisionComposed==$r and .capacityRevisionApplied==$r' >/dev/null
}
eventually 60 'Foghorn exposes the effective budget and 85 percent usable capacity' health_matches 2000 1 || end_scenario
network_matches() {
  gql 'query{networkStatus{clusters{clusterId egressCapacityMbps egressUsableMbps defaultBudgetNodes}}}' |
    jq -e --arg id "$cluster" --argjson b "$1" \
      '.errors==null and ([.data.networkStatus.clusters[]|select(.clusterId==$id)]|length==1 and .[0].egressCapacityMbps==$b and .[0].egressUsableMbps==($b*0.85) and .[0].defaultBudgetNodes==0)' >/dev/null
}
eventually 180 'networkStatus follows the node budget' network_matches 2000 || end_scenario

# Bypass Commodore's immediate push. The periodic control reconciliation must
# discover this persisted revision through the normal Quartermaster API.
direct=$(rpc "$STACK_QUARTERMASTER_GRPC_ADDR" quartermaster.NodeService/SetNodeCapacity \
  "$(jq -cn --arg id "$node" '{nodeId:$id,egressBudgetBytesPerSec:"375000000",ingressBudgetBytesPerSec:"125000000",expectedRevision:"1"}')")
check 'direct Quartermaster write commits the next revision' json_has "$direct" '.revision=="2"'
eventually 65 'direct Quartermaster write heals without provisioning' capacity_matches 375000000 125000000 2 OPERATOR || end_scenario
eventually 30 'Mist follows the healed revision' mist_matches 375000000 || end_scenario
eventually 60 'Foghorn follows the healed revision' health_matches 3000 2 || end_scenario
check 'budget changes keep the original Mist controller process' test "$(controller_identity)" = "$controller_before"

# Record without creating a viewer. Block S3 only in this disposable node's
# namespace while measuring local writes, then restore it and prove uploads
# count as real egress. Control traffic and the encoder remain reachable.
. "$STACK_LIB_DIR/tenant-lifecycle.sh"
# Recording storage is scheduled independently from the ingest policy. Cordon
# the other fixture storage node while starting this one local-write proof.
# The EXIT cleanup restores its original mode even if the proof fails.
other_mode_node=$(stack_exec edge printenv NODE_ID)
other_health=$(rpc "foghorn:${FOGHORN_INTERNAL_GRPC_PORT:-18019}" foghorn.NodeControlService/GetNodeHealth \
  "$(jq -cn --arg n "$other_mode_node" '{nodeId:$n}')" "$SERVICE_TOKEN")
other_mode=$(echo "$other_health" | jq -r '.operationalMode // "normal"')
cordoned=$(rpc "foghorn:${FOGHORN_INTERNAL_GRPC_PORT:-18019}" foghorn.NodeControlService/SetNodeOperationalMode \
  "$(jq -cn --arg n "$other_mode_node" '{nodeId:$n,mode:"draining",setBy:"stack-capacity-fixture"}')" "$SERVICE_TOKEN")
check 'other fixture storage is cordoned for the local recording proof' json_has "$cordoned" '.mode=="draining"'

next_session_refresh=$(($(date +%s) + 300))
refresh_tenant_session || { fail 'refresh owner session before the DVR proof'; end_scenario; }
# Keep Livepeer network transcoding out of the local-disk-only interval.
s=$(create_stream "stack-$run-dvr" true OFF)
stream=$(echo "$s" | jq -r '.id // empty')
uuid=$(echo "$s" | jq -r '.streamId // empty')
key=$(echo "$s" | jq -r '.streamKey // empty')
[ -n "$stream" ] || { fail 'create DVR stream'; end_scenario; }
internal=$(internal_name_of "$uuid")
set_placement TENANT_PRIVATE "$uuid" || { fail 'restrict the recording to private capacity'; end_scenario; }
eventually 90 'private ingest policy becomes effective' policy_effective || end_scenario
fault_started=1
netfault_start "$run" "$edge" || end_scenario
s3_ip=$(python3 -c 'import socket; print(socket.gethostbyname("s3"))')
netfault "$run" iptables -A FW_FAULT_OUT -d "$s3_ip" -p tcp --dport 8080 -j REJECT --reject-with tcp-reset || { fail 'isolate local recording from S3 uploads'; end_scenario; }
ffmpeg -hide_banner -loglevel error -re -f lavfi -i 'testsrc2=size=640x360:rate=20' \
  -f lavfi -i 'sine=frequency=440:sample_rate=48000' -t 180 -c:v libx264 -preset veryfast -g 40 \
  -b:v 3M -minrate 3M -maxrate 3M -bufsize 6M -x264-params nal-hrd=cbr \
  -pix_fmt yuv420p -c:a aac -b:a 96k -f flv "rtmp://$edge:1935/live/$key" \
  >"$STACK_STATE_DIR/publisher.log" 2>&1 &
publisher=$!
eventually 90 'DVR publisher is active on the private node' stream_active "$FOGHORN_A_DB" "$node" "$cluster" || end_scenario
eventually 90 'DVR starts without a viewer' tenant_dvr_started || end_scenario
local_recording() {
  [ "$(pg "$FOGHORN_A_DB" "SELECT count(*) FROM foghorn.artifacts WHERE tenant_id='$tenant' AND artifact_hash='$dvr_hash' AND dvr_start_dispatch->>'node_id'='$node'")" = 1 ] &&
    docker exec "$edge" sh -c 'find /data/storage/dvr -type f -name "*.ts" -size +64k 2>/dev/null | grep -q .'
}
eventually 90 'DVR stores media on the measured node' local_recording || end_scenario
samples="$STACK_STATE_DIR/dvr-network.jsonl"
: >"$samples"
last_heartbeat=''
capture_sample() {
  local health heartbeat
  health=$(node_health) || return 1
  echo "$health" | jq -c '{lastHeartbeat,observedSource,observedValid,activeViewers,bandwidthDownMbps,bandwidthUpMbps}' >"$STACK_STATE_DIR/last-network-sample.json"
  heartbeat=$(echo "$health" | jq -r '.lastHeartbeat // empty')
  [ -n "$heartbeat" ] && [ "$heartbeat" != "$last_heartbeat" ] || return 1
  echo "$health" | jq -e '.observedSource=="OBSERVED_SOURCE_NIC" and .bandwidthDownMbps>2' >/dev/null || return 1
  echo "$health" | jq -c '.' >>"$samples"
  last_heartbeat=$heartbeat
}
for _ in 1 2 3 4; do
  eventually 25 'fresh NIC sample sees ingest while recording locally' capture_sample || { cat "$STACK_STATE_DIR/last-network-sample.json"; end_scenario; }
done
check 'DVR local writes do not become network egress' jq -se 'length==4 and (map(.bandwidthUpMbps // 0)|add/length)<0.5' "$samples"
check 'DVR writes a nonempty local media file' docker exec "$edge" sh -c \
  'find /data/storage/dvr -type f -name "*.ts" -size +64k 2>/dev/null | grep -q .'
netfault_stop "$run"
fault_started=0
upload_observed() {
  [ "$(pg "$FOGHORN_A_DB" "SELECT count(*) FROM foghorn.dvr_segments s JOIN foghorn.artifacts a ON a.artifact_hash=s.artifact_hash WHERE a.tenant_id='$tenant' AND s.artifact_hash='$dvr_hash' AND s.status='uploaded'")" -gt 0 ] || return 1
  node_health | jq -e '.observedSource=="OBSERVED_SOURCE_NIC" and .bandwidthUpMbps>0.5' >/dev/null
}
eventually 90 'restored S3 uploads count as real network egress' upload_observed
kill "$publisher" 2>/dev/null || true
publisher=''
end_scenario
