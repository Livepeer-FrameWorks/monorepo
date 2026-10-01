#!/usr/bin/env bash
# stack-scenario: default
# The real operator CLI plans both Foghorn cells. The harness replaces only
# Ansible's host action with the stack's equivalent compose restart, so the
# CLI's manifest selection, role render, and return status remain under test.
. "$(dirname "$0")/../lib.sh"

if [ "${STACK_TARGET:-stack}" = staging ]; then
  need curl ssh || finish
  [ -x "$STACK_REPO/bin/cli" ] || { blocked 'operator CLI binary missing; make build-bin-cli'; finish; }
  [ -f "${STACK_CLI_MANIFEST:-}" ] && [ -n "${STACK_CLI_CONTEXT:-}" ] || {
    blocked 'staging needs its deployed CLI manifest and operator context'
    finish
  }
  CLI="$STACK_REPO/bin/cli"
  for item in "foghorn-eu:${STACK_FOGHORN_A_SERVICE:-foghorn}" "foghorn-us:${STACK_FOGHORN_B_SERVICE:-foghorn-b}"; do
    name=${item%%:*}; service=${item#*:}
    before=$(stack_started_at "$service") || { fail "read $name start time"; continue; }
    if result=$($CLI --context "$STACK_CLI_CONTEXT" cluster restart "$name" --manifest "$STACK_CLI_MANIFEST" 2>&1); then
      pass "operator CLI restarted $name"
    else
      fail "operator CLI restart $name: $result"
      continue
    fi
    after=$(stack_started_at "$service")
    check "$name native service actually restarted" bash -c 'test -n "$1" && test "$1" != "$2"' _ "$after" "$before"
    if [ "$name" = foghorn-eu ]; then bases=$FOGHORN_A_URLS; else bases=$FOGHORN_B_URLS; fi
    for base in $bases; do
      healthy() { [ "$(curl -s -m 5 -o /dev/null -w '%{http_code}' "$base/health")" = 200 ]; }
      eventually 90 "$name serves /health via $base after CLI restart" healthy
    done
  done
  if result=$($CLI --context "$STACK_CLI_CONTEXT" cluster diagnose ports --manifest "$STACK_CLI_MANIFEST" 2>&1); then
    pass 'operator CLI cluster diagnose ports exited 0'
  else
    fail "operator CLI cluster diagnose ports: $result"
  fi
  if result=$($CLI --context "$STACK_CLI_CONTEXT" cluster doctor --deep --manifest "$STACK_CLI_MANIFEST" 2>&1); then
    pass 'operator CLI cluster doctor --deep exited 0'
  else
    fail "operator CLI cluster doctor --deep: $result"
  fi
  finish
fi

need jq curl docker || finish
[ -x "$STACK_REPO/bin/cli" ] || { blocked 'operator CLI binary missing; make build-bin-cli'; finish; }
export STACK_CLI_MANIFEST="$STACK_STATE_DIR/cli-two-cell.yaml"
# shellcheck source=scripts/stack/cli-context.sh
. "$STACK_LIB_DIR/cli-context.sh"
cat >"$STACK_STATE_DIR/cli-restart.env" <<'ENV'
SERVICE_TOKEN=stack-test-token
MEDIA_AUTHORITY_SEAL_ROOT_SECRET=abababababababababababababababababababababababababababababababab
FOGHORN_STATE_ENCRYPTION_KEY=cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd
FOGHORN_BALANCER_CAPABILITY_SECRET=stack-test-balancer
ENV
cat >"$STACK_CLI_MANIFEST" <<'YAML'
version: v1
type: cluster
profile: dev
root_domain: frameworks.test
env_files:
  - cli-restart.env
hosts:
  stack-eu:
    external_ip: 127.0.0.1
    cluster: media-eu
  stack-us:
    external_ip: 127.0.0.1
    cluster: media-us
clusters:
  media-eu:
    name: Stack Media EU
    type: edge
    cell: cell-eu
    control_cell: cell-eu
  media-us:
    name: Stack Media US
    type: edge
    cell: cell-us
    control_cell: cell-us
services:
  foghorn-eu:
    enabled: true
    mode: native
    deploy: foghorn
    host: stack-eu
    cluster: media-eu
    config:
      DATABASE_URL: postgresql://foghorn_eu@postgres:5432/foghorn_eu
  foghorn-us:
    enabled: true
    mode: native
    deploy: foghorn
    host: stack-us
    cluster: media-us
    config:
      DATABASE_URL: postgresql://foghorn_us@postgres:5432/foghorn_us
  chandler-eu:
    enabled: true
    mode: native
    deploy: chandler
    host: stack-eu
    cluster: media-eu
  chandler-us:
    enabled: true
    mode: native
    deploy: chandler
    host: stack-us
    cluster: media-us
YAML

fake_bin="$STACK_STATE_DIR/cli-ansible"
mkdir -p "$fake_bin"
cat >"$fake_bin/ansible-galaxy" <<'SH'
#!/usr/bin/env bash
exit 0
SH
cat >"$fake_bin/ansible-playbook" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
vars=
for arg in "$@"; do
  case "$arg" in
    @*.json) vars=${arg#@} ;;
    --extra-vars=@*.json) vars=${arg#--extra-vars=@} ;;
  esac
done
[ -s "$vars" ] || { echo 'stack Ansible adapter: no role vars JSON' >&2; exit 1; }
cell=$(grep -o '"MEDIA_AUTHORITY_CELL_ID":"[^"]*"' "$vars" | head -1 | cut -d '"' -f4)
case "$cell" in
  cell-eu) service=foghorn ;;
  cell-us) service=foghorn-b ;;
  *) echo "stack Ansible adapter: wrong or missing media cell $cell" >&2; exit 1 ;;
esac
docker compose -p "$COMPOSE_PROJECT_NAME" restart "$service" >/dev/null
printf 'PLAY RECAP ********************************************************\n%s : ok=1 changed=1 unreachable=0 failed=0 skipped=0 rescued=0 ignored=0\n' "$service"
SH
chmod +x "$fake_bin/ansible-galaxy" "$fake_bin/ansible-playbook"
export PATH="$fake_bin:$PATH"
export XDG_CACHE_HOME="$STACK_STATE_DIR/cli-cache"
CLI="$STACK_REPO/bin/cli"

started_at() {
  docker inspect -f '{{.State.StartedAt}}' "$(container_of "$1")"
}
for item in 'foghorn-eu:foghorn' 'foghorn-us:foghorn-b'; do
  name=${item%%:*}
  service=${item#*:}
  before=$(started_at "$service")
  if result=$($CLI cluster restart "$name" --manifest "$STACK_CLI_MANIFEST" 2>&1); then
    pass "operator CLI restarted $name"
  else
    fail "operator CLI restart $name: $result"
    continue
  fi
  after=$(started_at "$service")
  check "$name stack container actually restarted" [ "$after" != "$before" ]
  if [ "$service" = foghorn ]; then base=${FOGHORN_A_URLS%% *}; else base=${FOGHORN_B_URLS%% *}; fi
  healthy() { [ "$(curl -s -m 5 -o /dev/null -w '%{http_code}' "$base/health")" = 200 ]; }
  eventually 90 "$name serves /health after CLI restart" healthy
done
if result=$($CLI cluster diagnose ports --manifest "$STACK_CLI_MANIFEST" 2>&1); then
  pass 'operator CLI cluster diagnose ports exited 0'
else
  fail "operator CLI cluster diagnose ports: $result"
fi
doctor_manifest="$STACK_STATE_DIR/cli-doctor-two-cell.yaml"
cat >"$doctor_manifest" <<'YAML'
version: v1
type: cluster
profile: dev
root_domain: frameworks.test
hosts:
  stack-eu:
    external_ip: 127.0.0.1
    cluster: media-eu
  stack-us:
    external_ip: 127.0.0.1
    cluster: media-us
clusters:
  media-eu:
    name: Stack Media EU
    type: edge
    cell: cell-eu
    control_cell: cell-eu
  media-us:
    name: Stack Media US
    type: edge
    cell: cell-us
    control_cell: cell-us
services:
  foghorn-eu:
    enabled: true
    mode: native
    deploy: foghorn
    host: stack-eu
    cluster: media-eu
  foghorn-us:
    enabled: true
    mode: native
    deploy: foghorn
    host: stack-us
    cluster: media-us
YAML
cat >"$fake_bin/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = -G ]; then
  printf 'hostname 127.0.0.1\n'
  exit 0
fi
case " $* " in
  *' stack-eu '*) base=${FOGHORN_A_URLS%% *} ;;
  *' stack-us '*) base=${FOGHORN_B_URLS%% *} ;;
  *) echo 'stack SSH adapter: unknown host' >&2; exit 1 ;;
esac
if [[ "$*" == *'/health'* || "$*" == *'/ready'* ]]; then
  if [[ "$*" == *'/ready'* ]]; then path=/ready; else path=/health; fi
  curl -sS -m 5 -o /dev/null -w '%{http_code}' "$base$path"
else
  echo 'stack SSH adapter: unexpected doctor probe' >&2
  exit 1
fi
SH
chmod +x "$fake_bin/ssh"
if result=$($CLI cluster doctor --manifest "$doctor_manifest" 2>&1); then
  pass 'operator CLI cluster doctor exited 0'
else
  fail "operator CLI cluster doctor: $result"
fi
finish
