#!/usr/bin/env bash
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
COMPOSE_PROJECT_NAME=fixture
GATEWAY=livepeer-gateway-a
ORCHESTRATOR=livepeer-orch-a
ORCHESTRATOR_B=livepeer-orch-b
GATEWAY_B=livepeer-gateway-b
OOM_SERVICE=''
docker() {
  local service=${!#}
  case "$1" in
    compose) printf 'id-%s\n' "$service" ;;
    inspect)
      local oom=false
      [ "$service" != "id-$OOM_SERVICE" ] || oom=true
      printf '%s %s 0 running\n' "$service" "$oom"
      ;;
    *) return 1 ;;
  esac
}
container_of() { printf 'id-%s\n' "$1"; }
definition=$(sed -n '/^media_runtime_state() {/,/^}/p' "$here/scenarios/66-transcode-ladders.sh")
if [ -z "$definition" ]; then
  definition=$(sed -n '/^edge_runtime_state() {/,/^}/p' "$here/scenarios/66-transcode-ladders.sh" | sed 's/edge_runtime_state()/media_runtime_state()/')
fi
eval "$definition"
before=$(media_runtime_state all)
for service in edge edge-b "$ORCHESTRATOR" "$ORCHESTRATOR_B" "$GATEWAY" "$GATEWAY_B"; do
  if ! grep -q "id-$service " <<<"$before"; then
    echo "media runtime guard omits $service" >&2
    exit 1
  fi
done
OOM_SERVICE=$ORCHESTRATOR
after=$(media_runtime_state all)
if [ "$before" = "$after" ]; then echo 'orchestrator OOM went unnoticed' >&2; exit 1; fi
echo 'media runtime guard covers both edges, orchestrators and gateways'
