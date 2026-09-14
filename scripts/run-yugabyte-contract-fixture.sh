#!/usr/bin/env bash
set -euo pipefail

if (( $# == 0 )); then
  echo "usage: $0 command [args...]" >&2
  exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
container="fw-yugabyte-contract-${PPID}-$$"
container_started=false
started_at=$SECONDS

cleanup() {
  local status=$?
  if [[ "$container_started" == true ]] && (( status != 0 )); then
    echo "Yugabyte contract fixture failed; collecting bounded diagnostics..." >&2
    docker inspect --format 'state={{.State.Status}} exit={{.State.ExitCode}} error={{.State.Error}} started={{.State.StartedAt}} finished={{.State.FinishedAt}} ports={{json .NetworkSettings.Ports}}' "$container" >&2 || true
    docker stats --no-stream --format 'container={{.Name}} cpu={{.CPUPerc}} memory={{.MemUsage}} net={{.NetIO}} block={{.BlockIO}} pids={{.PIDs}}' "$container" >&2 || true
    docker exec -e PGCONNECT_TIMEOUT=5 -e 'PGOPTIONS=-c statement_timeout=5000' "$container" \
      ysqlsh -h "$container" -U yugabyte -d yugabyte -P pager=off -c \
      "SELECT datname, state, wait_event_type, wait_event, count(*) AS sessions FROM pg_stat_activity GROUP BY datname, state, wait_event_type, wait_event ORDER BY datname, state, wait_event_type, wait_event" >&2 || true
    docker logs --tail 200 "$container" >&2 || true
  fi
  if [[ "$container_started" == true ]]; then
    docker rm -fv "$container" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

resolve_image() {
  if [[ -n "${FRAMEWORKS_YUGABYTE_TEST_IMAGE:-}" ]]; then
    printf '%s\n' "$FRAMEWORKS_YUGABYTE_TEST_IMAGE"
    return
  fi
  awk '
    /^[[:space:]]*-[[:space:]]+name:[[:space:]]+yugabyte[[:space:]]*$/ { active = 1; next }
    active && /^[[:space:]]*-[[:space:]]+name:/ { active = 0 }
    active && /^[[:space:]]+image:/ { image = $2 }
    active && /^[[:space:]]+digest:/ { digest = $2 }
    END {
      if (image == "" || digest == "") exit 1
      print image "@" digest
    }
  ' "$repo_root/config/schema-contract-engines.yaml"
}

image="$(resolve_image)" || {
  echo "ERROR: could not resolve Yugabyte contract image from config/schema-contract-engines.yaml" >&2
  exit 1
}

docker run -d --name "$container" -P --hostname "$container" \
  "$image" \
  bash -c 'exec bin/yugabyted start --background=false --base_dir=/tmp/frameworks-yugabyte-contract-data --advertise_address="$(hostname -i)" --tserver_flags=yb_enable_read_committed_isolation=false' \
  >/dev/null
container_started=true

port=""
for (( attempt = 0; attempt < 120; attempt++ )); do
  port="$(docker inspect -f '{{(index (index .NetworkSettings.Ports "5433/tcp") 0).HostPort}}' "$container" 2>/dev/null || true)"
  if [[ -n "$port" && "$port" != "<no value>" ]]; then
    break
  fi
  sleep 1
done
if [[ -z "$port" || "$port" == "<no value>" ]]; then
  echo "ERROR: Yugabyte did not publish 5433/tcp within 120 seconds" >&2
  docker logs --tail 80 "$container" >&2 || true
  exit 1
fi

ready=false
for (( attempt = 0; attempt < 180; attempt++ )); do
  if [[ "$(docker exec "$container" ysqlsh -h "$container" -U yugabyte -d yugabyte -tAc 'SELECT 1' 2>/dev/null || true)" == "1" ]]; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  echo "ERROR: Yugabyte did not become ready within 180 seconds" >&2
  docker logs --tail 80 "$container" >&2 || true
  exit 1
fi

export FRAMEWORKS_YUGABYTE_TEST_CONTAINER="$container"
export FRAMEWORKS_YUGABYTE_TEST_DSN="postgres://yugabyte@127.0.0.1:${port}/yugabyte?sslmode=disable"
export FRAMEWORKS_YUGABYTE_TEST_RETAIN_DATABASES=1

echo "Shared Yugabyte contract engine ready in $((SECONDS - started_at))s; running: $*"
"$@"
echo "Shared Yugabyte contract suite completed in $((SECONDS - started_at))s"
