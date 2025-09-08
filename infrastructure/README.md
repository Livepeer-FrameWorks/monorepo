# FrameWorks Infrastructure (Dev Configs)

Dev-only configuration used by the root `docker-compose.yml`. These files help you run the full stack locally. Production deployment uses the CLI’s templates and docs under `docs/provisioning/`.

## What’s Here (used by dev compose)
- `nginx/default.conf` — Dev reverse proxy for app, GraphQL, websocket and media endpoints
- `prometheus/*` — Prometheus config and rules for local metrics
- `grafana/*` — Provisioning and dashboards for local Grafana
- `clickhouse/*` — ClickHouse users and server config used by dev compose
- `mistserver.conf` — MistServer config for local media tests

## How To Use It (local dev)
- From the repo root, start the stack: `docker-compose up -d`
- The compose file mounts these configs directly; edit and restart the affected container to apply changes
- Ports and endpoints are listed in the root `README.md`

## Production
- Production stacks, environment files, and system configs are generated and deployed via the FrameWorks CLI
- See `docs/provisioning/` for requirements and deployment guides

## Related
- Root `README.md` — services and ports overview
- `docs/provisioning/` — deployment methods, SSL, WireGuard, DNS
