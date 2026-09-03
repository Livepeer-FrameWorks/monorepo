#!/usr/bin/env bash
set -euo pipefail

if (( $# == 0 )); then
  echo "usage: $0 command [args...]" >&2
  exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
container="fw-yugabyte-contract-${PPID}-$$"
started_at=$SECONDS

cleanup() {
  docker rm -fv "$container" >/dev/null 2>&1 || true
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
    active && /^[[:space:]]+contract_image:/ { image = $2 }
    active && /^[[:space:]]+contract_digest:/ { digest = $2 }
    END {
      if (image == "" || digest == "") exit 1
      print image "@" digest
    }
  ' "$repo_root/config/infrastructure.yaml"
}

image="$(resolve_image)" || {
  echo "ERROR: could not resolve Yugabyte contract image from config/infrastructure.yaml" >&2
  exit 1
}

docker run -d --name "$container" -P --hostname "$container" "$image" \
  bash -c 'exec bin/yugabyted start --background=false --advertise_address="$(hostname -i)" --tserver_flags=yb_enable_read_committed_isolation=false' \
  >/dev/null

port=""
for (( attempt = 0; attempt < 300; attempt++ )); do
  port="$(docker inspect -f '{{(index (index .NetworkSettings.Ports "5433/tcp") 0).HostPort}}' "$container" 2>/dev/null || true)"
  if [[ -n "$port" && "$port" != "<no value>" ]]; then
    break
  fi
  sleep 0.1
done
if [[ -z "$port" || "$port" == "<no value>" ]]; then
  echo "ERROR: Yugabyte did not publish 5433/tcp" >&2
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
