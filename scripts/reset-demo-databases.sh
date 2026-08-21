#!/bin/sh
set -eu

mode="${1:-apply}"
if [ "$mode" != "apply" ] && [ "$mode" != "--plan" ]; then
  echo "usage: $0 [--plan]" >&2
  exit 2
fi

compose_config=$(docker compose config --format json)
postgres_volume=$(printf '%s' "$compose_config" | jq -r '.volumes.postgres18_data.name // empty')
clickhouse_volume=$(printf '%s' "$compose_config" | jq -r '.volumes.clickhouse_data.name // empty')

case "$postgres_volume" in
  ?*_postgres18_data) ;;
  *) echo "refusing reset: unexpected PostgreSQL volume name '$postgres_volume'" >&2; exit 1 ;;
esac
case "$clickhouse_volume" in
  ?*_clickhouse_data) ;;
  *) echo "refusing reset: unexpected ClickHouse volume name '$clickhouse_volume'" >&2; exit 1 ;;
esac

echo "PostgreSQL volume: $postgres_volume"
echo "ClickHouse volume:  $clickhouse_volume"
if [ "$mode" = "--plan" ]; then
  exit 0
fi

docker compose stop postgres clickhouse
docker compose rm -f postgres clickhouse
for volume in "$postgres_volume" "$clickhouse_volume"; do
  if docker volume inspect "$volume" >/dev/null 2>&1; then
    docker volume rm "$volume"
  fi
done
docker compose up -d --wait postgres clickhouse
make seed-demo
