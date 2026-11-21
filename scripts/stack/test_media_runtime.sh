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


eval "$(sed -n '/^no_upload_degradation() {/,/^}/p' "$here/scenarios/79-rotated-upload.sh")"
FIXTURE_LOGS=''
log_json() { printf '%s\n' "$FIXTURE_LOGS" | jq -c "$2"; }
if no_upload_degradation hash now; then echo 'empty logs falsely proved no degradation' >&2; exit 1; fi
FIXTURE_LOGS='{"msg":"Stream transcode failed; local replacement awaits job admission","stream":"processing+hash","artifact_hash":"hash"}'
if no_upload_degradation hash now; then echo 'deferred fallback escaped degradation assertion' >&2; exit 1; fi
FIXTURE_LOGS='{"msg":"ordinary processing event","stream":"processing+hash","artifact_hash":"hash"}'
no_upload_degradation hash now || { echo 'ordinary processing event refused' >&2; exit 1; }
echo 'rotation degradation assertion requires readable logs and detects deferred failures'


eval "$(sed -n '/^gateway_present() {/,/^}/p' "$here/scenarios/69-vod-ladder-geometry.sh")"
FIXTURE_NOW=100
FIXTURE_GATEWAY=healthy
GATEWAY_HEALTHY_AT=0
date() { printf '%s\n' "$FIXTURE_NOW"; }
gateway_state() { printf '%s\n' "$FIXTURE_GATEWAY"; }
if gateway_present; then echo 'healthy gateway skipped consumer refresh window' >&2; exit 1; fi
FIXTURE_NOW=106
if gateway_present; then echo 'six seconds falsely proved gateway pool refreshed' >&2; exit 1; fi
FIXTURE_NOW=165
gateway_present || { echo 'stable gateway never became usable after refresh window' >&2; exit 1; }
FIXTURE_GATEWAY=unhealthy
gateway_present && { echo 'unhealthy gateway retained readiness' >&2; exit 1; }
FIXTURE_NOW=200
FIXTURE_GATEWAY=healthy
if gateway_present; then echo 'gateway health flap skipped a fresh refresh window' >&2; exit 1; fi
echo 'gateway readiness covers consumer refresh and resets after a health flap'
