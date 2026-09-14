#!/usr/bin/env bash
set -euo pipefail

image=${1:?usage: test-logbook-image-health.sh IMAGE}
container="frameworks-logbook-health-$$"
response=$(mktemp)

diagnostics() {
	docker inspect "$container" >&2 || true
	docker logs "$container" >&2 || true
}

cleanup() {
	docker rm -f "$container" >/dev/null 2>&1 || true
	rm -f "$response"
}
trap cleanup EXIT

docker run --detach \
	--name "$container" \
	--publish 127.0.0.1::18033 \
	--health-interval 1s \
	--health-timeout 2s \
	--health-start-period 0s \
	--health-retries 10 \
	"$image" >/dev/null

health=""
for _ in $(seq 1 30); do
	health=$(docker inspect --format '{{.State.Health.Status}}' "$container")
	case "$health" in
	healthy) break ;;
	unhealthy)
		diagnostics
		echo "Logbook image became unhealthy" >&2
		exit 1
		;;
	esac
	sleep 1
done

if [[ "$health" != "healthy" ]]; then
	diagnostics
	echo "Logbook image did not become healthy within 30 seconds" >&2
	exit 1
fi

published=$(docker port "$container" 18033/tcp | tail -n 1)
port=${published##*:}
status=$(curl --silent --show-error --output "$response" --write-out '%{http_code}' "http://127.0.0.1:${port}/health/")

if [[ "$status" != "200" || "$(tr -d '\r\n' <"$response")" != "ok" ]]; then
	diagnostics
	echo "Logbook /health/ returned HTTP $status with unexpected content" >&2
	exit 1
fi

status=$(curl --location --max-redirs 1 --silent --show-error --output "$response" --write-out '%{http_code}' "http://127.0.0.1:${port}/health")
if [[ "$status" != "200" || "$(tr -d '\r\n' <"$response")" != "ok" ]]; then
	diagnostics
	echo "Logbook /health did not resolve to the canonical health endpoint" >&2
	exit 1
fi

echo "Logbook image health contract verified."
