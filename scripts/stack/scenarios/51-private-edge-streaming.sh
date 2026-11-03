#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, creates a tenant and enrolls a disposable private edge
# A fresh Free signup publishes on platform capacity and on its own newly
# created private cluster. No seeded tenant, cluster health override, operator
# billing grant or synthetic publisher trigger may supply admission. RTMP and
# SRT on the private edge record and replay (RTMP), check signed lifecycle
# webhooks and final analytics, and their playback stays on the private edge
# through every Foghorn replica. Scenario 47 covers the platform-capacity
# lifecycle as the run's tenant.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 docker openssl psql timeout || finish
[ -S /var/run/docker.sock ] || { blocked 'private edge enrollment needs the stack Docker socket'; finish; }
run="tenant51-$(date +%s)-$RANDOM"
# Keep this tenant's ledger and webhook secret separate from the demo account.
export STACK_STATE_DIR="$STACK_STATE_DIR/$run"
mkdir -p "$STACK_STATE_DIR"
since=$(date +%s)
email=$(test_email "$run")
password="Stack-$(openssl rand -hex 8)"
cookies="$STACK_STATE_DIR/$run.cookies"
tenant="" stream="" publisher="" edge="$COMPOSE_PROJECT_NAME-$run" proxy="$COMPOSE_PROJECT_NAME-$run-proxy"
endpoint="" clip="" dvr_hash=""
streams=()
cleanup() {
  [ "${cleaned:-0}" = 0 ] || return 0
  cleaned=1
  [ -z "$publisher" ] || kill "$publisher" 2>/dev/null
  for id in "${streams[@]}"; do delete_stream "$id"; done
  [ -z "$clip" ] || gql 'mutation($id:ID!){deleteClip(id:$id){__typename}}' "$(jq -cn --arg id "$clip" '{id:$id}')" >/dev/null
  [ -z "$dvr_hash" ] || gql 'mutation($id:ID!){deleteDVR(dvrHash:$id){__typename}}' "$(jq -cn --arg id "$dvr_hash" '{id:$id}')" >/dev/null
  [ -z "$endpoint" ] || gql 'mutation($id:ID!){deleteWebhookEndpoint(id:$id){__typename}}' "$(jq -cn --arg id "$endpoint" '{id:$id}')" >/dev/null
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
next_session_refresh=$(($(date +%s) + 300))
free_plan() { [ "$(pg purser "SELECT bt.tier_name FROM purser.tenant_subscriptions ts JOIN purser.billing_tiers bt ON bt.id=ts.tier_id WHERE ts.tenant_id='$tenant' AND ts.status='active'")" = free ]; }
eventually 90 'ordinary signup has an active Free plan' free_plan || end_scenario
. "$STACK_LIB_DIR/tenant-lifecycle.sh"
setup_tenant_webhooks || { fail 'Free tenant creates its own webhook endpoint'; end_scenario; }
pass 'Free tenant creates its own webhook endpoint'

log 'the fresh Free tenant is admitted on platform capacity'
platform_node=$(pg quartermaster "SELECT node_id FROM quartermaster.infrastructure_nodes WHERE cluster_id='$CELL_A_CLUSTER' AND node_type='edge' AND status='active' ORDER BY node_id LIMIT 1")
stream_flow PLATFORM_OFFICIAL "$EDGE_A_HOST" "$FOGHORN_A_DB" "$platform_node" "$CELL_A_CLUSTER" rtmp "${FOGHORN_A_URLS%% *}" admission || end_scenario

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
# Ledger workers run every five minutes with a two-minute settlement lag and
# a five-minute cursor boundary. Do not refresh them artificially for this test.
eventually 900 'both ended private streams settle with exact runtime and nonzero viewer analytics' tenant_all_analytics
for id in "${streams[@]}"; do delete_stream "$id"; done
streams=()
check 'fresh Free tenant passes its own analytics, runtime, API and artifact sweep' env STACK_SWEEP_DOCTOR=0 bash "$STACK_LIB_DIR/sweep.sh" "$since"
end_scenario
