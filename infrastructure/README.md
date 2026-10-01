# FrameWorks Infrastructure (Dev Configs)

Dev-only configuration used by the root dev compose configuration. These files help you run the full stack locally. Production deployment uses the CLI provisioners, Ansible roles, and generated stack templates.

## What's Here

**Used by dev compose:**

- `nginx/default.conf` — Dev reverse proxy for app, GraphQL, websocket and media endpoints
- `clickhouse/*` — ClickHouse users and server config
- `mistserver.conf` — read-only MistServer seed config of the dev `edge` service, mounted at `/etc/frameworks/mistserver.seed.conf`
- `edge/machine-id` — stable machine identity for the dev edge's enrollment fingerprint
- `two-cell/*` — second media cell for the `two-cell` profile

**Referenced by CLI deployments (staging/prod):**

- `prometheus/*` — Prometheus config and rules
- `grafana/*` — provisioning and dashboards

## How To Use It (local dev)

- From the repo root, start the stack: `docker compose up -d` (profiles and presets: `CONTRIBUTING.md`)
- `mistserver.conf` (and `two-cell/mistserver-b.conf` for `edge-b`) is a read-only seed. On an edge's first boot, `seed-edge` copies it to `/etc/frameworks/mistserver.conf` in the edge's `/etc/frameworks` volume with the controller listener set, and Mist saves its runtime state (API account digest, streams, triggers) there, never to the tracked file. Edits to the seed reach an edge only after its volume is recreated (`docker compose down -v`, or `scripts/stack/down.sh` for a stack slot). Helmsman reconciles managed streams and triggers through the Mist API; change the Helmsman config manager rather than hand-editing managed trigger entries here.
- Ports and endpoints are listed in the root `README.md`

## Production

- Production stacks, environment files, and system configs are generated and deployed via `frameworks cluster ...` and `frameworks edge ...`
- **DNS & Certificates**: Public DNS management (by Navigator, `api_dns`) and automated certificate issuance (ACME) are now handled by a dedicated service. This paves the way for our upcoming self-hosted Anycast DNS.
- **Networking**: Internal WireGuard mesh orchestration and local DNS are managed by Privateer (`api_mesh`), ensuring secure, automated service-to-service communication.
- See `cli/README.md` and `website_docs/` for deployment guides

## Related

- Root `README.md` — services and ports overview
- `cli/README.md` — operator CLI command groups
- `website_docs/` — deployment guides, DNS, WireGuard
