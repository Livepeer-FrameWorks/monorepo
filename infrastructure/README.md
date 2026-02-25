# FrameWorks Infrastructure (Dev Configs)

Dev-only configuration used by the root dev compose configuration. These files help you run the full stack locally. Production deployment uses the CLI's edge templates.

## What's Here

**Used by dev compose:**

- `nginx/default.conf` — Dev reverse proxy for app, GraphQL, websocket and media endpoints
- `clickhouse/*` — ClickHouse users and server config
- `mistserver.conf` — MistServer config for local media tests

**Used by CLI deployments (staging/prod):**

- `prometheus/*` — Prometheus config and rules
- `grafana/*` — Provisioning and dashboards

## How To Use It (local dev)

- From the repo root, start the stack: `docker-compose up -d`
- The compose file mounts these configs directly; edit and restart the affected container to apply changes
- Ports and endpoints are listed in the root `README.md`

## Production

- Production stacks, environment files, and system configs are generated and deployed via the FrameWorks CLI
- **DNS & Certificates**: Public DNS management (by Navigator, `api_dns`) and automated certificate issuance (ACME) are now handled by a dedicated service. This paves the way for our upcoming self-hosted Anycast DNS.
- **Networking**: Internal WireGuard mesh orchestration and local DNS are managed by Privateer (`api_mesh`), ensuring secure, automated service-to-service communication.
- See `cli/README.md` and `website_docs/` for deployment guides

## Related

- Root `README.md` — services and ports overview
- `cli/README.md` — edge deployment commands
- `website_docs/` — deployment guides, DNS, WireGuard
