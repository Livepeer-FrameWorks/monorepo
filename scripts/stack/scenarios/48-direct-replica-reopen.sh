#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, provisions managed executable inputs and restarts destination MistServer
# A managed stream stays live on its source edge while its destination replica
# ends and is cleared. A new viewer opens the same public alias directly on
# the destination, without /play, and must receive TS bytes under a new pull.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 psql docker || finish
[ "${STACK_TARGET:-stack}" != staging ] || { blocked 'requires an isolated stack'; finish; }
WORK="$STACK_STATE_DIR/direct-reopen-$(date +%s)"
mkdir -p "$WORK"
FIXTURES=0
BILLING_FIXTURE=0
ACCESS_FIXTURES=0
SOURCE_PID=""
snapshot_cluster_access_fixture() { # tenant cluster snapshot-file
  pg quartermaster "SELECT replace(encode(convert_to(row_to_json(access)::text,'UTF8'),'base64'), E'\\n','')
    FROM quartermaster.tenant_cluster_access access WHERE tenant_id='$1' AND cluster_id='$2'" >"$3"
}
restore_cluster_access_fixture() { # tenant cluster snapshot-file
  local tenant=$1 cluster=$2 snapshot=$3 prior current
  [ -f "$snapshot" ] || { echo "missing cluster access snapshot: $snapshot" >&2; return 1; }
  prior=$(cat "$snapshot") || return 1
  if [ -z "$prior" ]; then
    pg_fixture_diagnostic quartermaster "DELETE FROM quartermaster.tenant_cluster_access WHERE tenant_id='$tenant' AND cluster_id='$cluster'" >/dev/null || return 1
  else
    # This system-tenant access row is shared with other scenarios. Restore every field,
    # including provenance and expiry, after billing cleanup reconciles access.
    pg_fixture_diagnostic quartermaster "INSERT INTO quartermaster.tenant_cluster_access
      SELECT restored.* FROM json_populate_record(NULL::quartermaster.tenant_cluster_access,
        convert_from(decode('$prior','base64'),'UTF8')::json) AS restored
      WHERE restored.tenant_id='$tenant' AND restored.cluster_id='$cluster'
      ON CONFLICT (tenant_id,cluster_id) DO UPDATE SET
        id=EXCLUDED.id,access_level=EXCLUDED.access_level,access_source=EXCLUDED.access_source,
        resource_limits=EXCLUDED.resource_limits,subscription_status=EXCLUDED.subscription_status,
        requested_at=EXCLUDED.requested_at,approved_at=EXCLUDED.approved_at,approved_by=EXCLUDED.approved_by,
        rejection_reason=EXCLUDED.rejection_reason,invite_token=EXCLUDED.invite_token,
        is_active=EXCLUDED.is_active,granted_at=EXCLUDED.granted_at,expires_at=EXCLUDED.expires_at,
        created_at=EXCLUDED.created_at,updated_at=EXCLUDED.updated_at" >/dev/null || return 1
  fi
  current=$(pg_diagnostic quartermaster "SELECT replace(encode(convert_to(row_to_json(access)::text,'UTF8'),'base64'), E'\\n','')
    FROM quartermaster.tenant_cluster_access access WHERE tenant_id='$tenant' AND cluster_id='$cluster'") || return 1
  if [ "$current" != "$prior" ]; then
    printf '%s\n' "$current" >"$snapshot.after"
    echo "cluster access snapshot mismatch for $cluster: expected $snapshot, actual $snapshot.after" >&2
    return 1
  fi
}
cleanup() {
  # Explicit cleanup and EXIT share one attempt. Failed restoration must remain
  # visible rather than being silently repeated by the exit trap.
  [ "${CLEANUP_STARTED:-0}" = 0 ] || return 0
  CLEANUP_STARTED=1
  local cluster restore_access=1
  if [ "$FIXTURES" = 1 ]; then
    stack_exec commodore /app/commodore bootstrap --file /dev/stdin <"$STACK_LIB_DIR/bootstrap.yaml" >/dev/null || fail 'remove managed playback fixtures'
  fi
  if [ "$BILLING_FIXTURE" = 1 ]; then
    if FW_USER_TOKEN="$OPERATOR_TOKEN" "$STACK_REPO/bin/cli" admin billing grant revoke --tenant-id "$TENANT" --tier free --reason 'stack managed playback cleanup' >/dev/null; then
      wait_billing_entitlement_reconcile "$TENANT" || restore_access=0
    else
      fail 'remove managed playback billing grant'
      restore_access=0
    fi
    BILLING_FIXTURE=0
  fi
  if [ "$ACCESS_FIXTURES" = 1 ] && [ "$restore_access" = 1 ]; then
    ACCESS_FIXTURES=0
    for cluster in "$CELL_A_CLUSTER" "$CELL_B_CLUSTER"; do
      if ! restore_cluster_access_fixture "$TENANT" "$cluster" "$WORK/access-$cluster.before"; then
        fail "restore original cluster access for $cluster"
      fi
    done
  fi
  [ -z "$SOURCE_PID" ] || kill "$SOURCE_PID" 2>/dev/null
  SOURCE_PID=""
}
trap cleanup EXIT

# The system operator owns managed inputs, as it does in production.
jq -cn --argjson bot "$(bot_fields)" '{email:"operator@stack.test",password:"stack-operator-password"} + $bot' |
  curl -sf -m 20 -c "$WORK/cookies" -o /dev/null -H 'Content-Type: application/json' --data-binary @- "$BRIDGE_URL/auth/login" ||
  { fail 'log in as the fixture operator'; finish; }
TENANT=$(python3 - "$WORK/cookies" <<'PY'
import base64, json, sys
for line in open(sys.argv[1]):
    fields = line.strip().split('\t')
    if len(fields) == 7 and fields[5] == 'access_token':
        payload = fields[6].split('.')[1]
        print(json.loads(base64.urlsafe_b64decode(payload + '=' * (-len(payload) % 4)))['tenant_id'])
        break
PY
)
[ -n "$TENANT" ] || { fail 'operator login returned no tenant'; finish; }

. "$STACK_LIB_DIR/cli-context.sh"
OPERATOR_TOKEN=$(awk '$6=="access_token"{print $7}' "$WORK/cookies" | tail -1)
EXPIRES=$(python3 -c 'import datetime; print((datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(minutes=30)).strftime("%Y-%m-%dT%H:%M:%SZ"))')
for cluster in "$CELL_A_CLUSTER" "$CELL_B_CLUSTER"; do
  snapshot_cluster_access_fixture "$TENANT" "$cluster" "$WORK/access-$cluster.before" ||
    { fail "snapshot original cluster access for $cluster"; finish; }
done
ACCESS_FIXTURES=1
# The account-only stack bootstrap creates no billing subscription for the
# system operator. Use a real temporary grant so billing admits its viewers.
FW_USER_TOKEN="$OPERATOR_TOKEN" "$STACK_REPO/bin/cli" admin billing grant set --tenant-id "$TENANT" --tier enterprise --base-fee 0 --waive-usage --until "$EXPIRES" --fallback-tier free --reason 'stack managed playback fixture' >/dev/null ||
  { fail 'grant the managed fixture billing access'; finish; }
BILLING_FIXTURE=1
for cluster in "$CELL_A_CLUSTER" "$CELL_B_CLUSTER"; do
  FW_USER_TOKEN="$OPERATOR_TOKEN" "$STACK_REPO/bin/cli" admin clusters access grant --tenant-id "$TENANT" --cluster-id "$cluster" --expires-at "$EXPIRES" ||
    { fail "grant the managed fixture access to $cluster"; finish; }
done

render_mp4 "$WORK/loop.mp4" 320x180 10 12 || { fail 'render managed source'; finish; }
# Keep the native input continuous across clip loops, like the production
# ts-exec encoder. The runner supplies ffmpeg; the edge needs only curl.
python3 "$STACK_LIB_DIR/native-source.py" "$WORK/loop.mp4" >"$WORK/source.log" 2>&1 &
SOURCE_PID=$!

PB_A="stack-reopen-a-$(date +%s)"
PB_B="stack-reopen-b-$(date +%s)"
cat "$STACK_LIB_DIR/bootstrap.yaml" >"$WORK/bootstrap.yaml"
cat >>"$WORK/bootstrap.yaml" <<EOF
commodore:
  mist_native_streams:
    - playback_id: $PB_A
      owner_tenant: {ref: quartermaster.system_tenant}
      title: Stack managed replica reopen A
      source: 'ts-exec:curl --no-buffer -fsS http://stack-runner:18948/loop.ts'
      source_kind: exec
      always_on: true
      monitoring: 'off'
      process_policy: []
      source_location:
        clusters: [{cluster_id: $CELL_A_CLUSTER}]
    - playback_id: $PB_B
      owner_tenant: {ref: quartermaster.system_tenant}
      title: Stack managed replica reopen B
      source: 'ts-exec:curl --no-buffer -fsS http://stack-runner:18948/loop.ts'
      source_kind: exec
      always_on: true
      monitoring: 'off'
      process_policy: []
      source_location:
        clusters: [{cluster_id: $CELL_B_CLUSTER}]
EOF
FIXTURES=1
stack_exec commodore /app/commodore bootstrap --file /dev/stdin <"$WORK/bootstrap.yaml" ||
  { fail 'provision managed playlist streams'; finish; }

replication() {
  local response
  response=$(curl -sf -m 5 -H "Authorization: Bearer $SERVICE_TOKEN" "$INTERNAL/debug/stream-registry?q=$IN&include=replications") || return 1
  jq -r --arg name "$IN" '.local_replications[]? | select(.internal_name == $name) | .attempt_id' <<<"$response"
}
source_visible() {
  local response cell
  response=$(curl -sf -m 5 -H "Authorization: Bearer $SERVICE_TOKEN" "$INTERNAL/debug/stream-registry?q=$IN&include=sources") || return 1
  cell=$(jq -r '.local_cluster_id' <<<"$response")
  # The compact registry endpoint omits edge playability. Read the disposable
  # stack's credential-free source advertisement to wait for a playable origin.
  response=$(stack_exec "$REDIS" valkey-cli --raw get "{$cell}:registry:source:$IN") || return 1
  # jq versions used by the runner can exit zero for empty input with -e.
  # Slurping makes a missing key an explicit false readiness result.
  jq -es --arg cell "$cell" --arg tenant "$TENANT" 'length == 1 and (.[0] | .TenantID == $tenant and ([.Locations | to_entries[] | select(.key != $cell and .value.IsLiveNow) | .value.EdgeCandidates[]? | select(.IsOrigin and .Playable and .DTSCURL != "")] | length > 0))' <<<"$response" >/dev/null
}
cleared() { local current; current=$(replication) && [ -z "$current" ]; }
has_ts() {
  local got
  got=$(segment_fetch "$1")
  echo "    segment: $got"
  [ "${got%% *}" = 200 ] && [ "${got#* }" -gt 0 ] 2>/dev/null
}
assert_ts() {
  if has_ts "$2"; then pass "$1"; else
    fail "$1"
    curl -sS -i -m 5 "$2"
    return 1
  fi
}

for direction in a-to-b b-to-a; do
  if [ "$direction" = a-to-b ]; then
    PB=$PB_A SOURCE=${FOGHORN_A_URLS%% *} INTERNAL=${FOGHORN_B_INTERNAL_URLS%% *}
    DEST=$STACK_EDGE_B_SERVICE HOST=$EDGE_B_HOST SERVICES=(foghorn-b foghorn-b-2)
    REDIS=foghorn-redis-b
  else
    PB=$PB_B SOURCE=${FOGHORN_B_URLS%% *} INTERNAL=${FOGHORN_A_INTERNAL_URLS%% *}
    DEST=$STACK_EDGE_A_SERVICE HOST=$EDGE_A_HOST SERVICES=(foghorn foghorn-2)
    REDIS=foghorn-redis
  fi
  IN=$(pg commodore "SELECT internal_name FROM commodore.streams WHERE tenant_id='$TENANT' AND playback_id='$PB'")
  [ -n "$IN" ] || { fail "$direction: managed stream not found"; continue; }
  if ! eventually 120 "$direction: managed origin has media" media_at "$SOURCE" "$PB"; then
    curl -sS -m 10 "$SOURCE/play/$PB/hls"
    continue
  fi
  # Peer advertisements establish source visibility without preparing or
  # opening a viewer on the destination.
  eventually 45 "$direction: destination sees the remote origin" source_visible || continue
  DIRECT="http://$HOST:8082/hls/$PB/index.m3u8"
  SEEN=$(utc_now)
  assert_ts "$direction: first direct alias playback has TS bytes" "$DIRECT" || continue
  OLD=$(replication)
  [ -n "$OLD" ] || { fail "$direction: no physical pull was recorded"; continue; }

  # Wait for destination presence before ending it. A short-lived replica can
  # still be inside the admission start grace when its final snapshot lands.
  observed() { log_has "$SEEN" 'Replication destination observed live' "${SERVICES[@]}"; }
  eventually 40 "$direction: destination observed carrying the pull" observed || continue
  stack_exec "$DEST" /command/s6-svc -r /run/service/mistserver || { fail "$direction: restart destination Mist"; continue; }
  eventually 360 "$direction: old destination pull cleared" cleared || continue
  # One new playback: retrying /play here would hide the recovery defect.
  assert_ts "$direction: same alias reopens directly with TS bytes" "$DIRECT" || continue
  NEW=$(replication)
  check "$direction: reopened replica has a new physical attempt" test -n "$NEW"
  check "$direction: retired physical attempt was not reused" test "$NEW" != "$OLD"
done
cleanup
FIXTURES=0
finish
