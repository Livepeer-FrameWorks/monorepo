# Navigator (DNS & Certificate Manager)

Automates public DNS records, TLS certificate issuance, and internal service certificates for platform-managed domains. Navigator currently uses Cloudflare APIs for public DNS and Cloudflare-backed DNS-01 ACME challenges; the self-hosted anycast/PowerDNS path is planned architecture, not a drop-in provider switch today.

## Why Navigator?

- **Provider boundary**: DNS logic is isolated behind an internal Cloudflare client interface, but only Cloudflare is implemented today
- **Self-hosted path**: Architecture supports a future move to owned anycast DNS infrastructure once the provider implementation exists
- **Managed service automation**: Public service records, cluster-scoped media records, load balancer health checks, and TLS material are reconciled from control-plane inventory

## What it does

- Syncs DNS records for managed public service types from Quartermaster node/service inventory
- "Smart Record" logic: single healthy node -> A record, multiple healthy nodes -> Cloudflare Load Balancer
- Issues TLS certificates via Let's Encrypt DNS-01 challenges using Cloudflare
- Issues and stores internal gRPC certificates from Navigator's internal CA
- Auto-renewal via background worker

## Run (dev)

- Start the full stack from repo root: `docker-compose up -d`
- Or run just Navigator: `cd api_dns && go run ./cmd/navigator`

Configuration comes from the top-level `config/env` stack. Generate `.env` with `make env` and customise `config/env/secrets.env` for secrets. Do not commit secrets.

## Optional env vars

- `ACME_ENV` (`production` or `staging`, default: `production`)
- `NAVIGATOR_PROXY_SERVICES` (comma-separated service types to proxy via Cloudflare; default: `bridge,chandler,chartroom,chatwoot,foredeck,listmonk,logbook,steward`)
- `NAVIGATOR_CERT_ALLOWED_SUFFIXES` (comma-separated domain suffix allowlist; default: `BRAND_DOMAIN`)
- `NAVIGATOR_DNS_RECONCILE_INTERVAL_SECONDS` (default: `60`)
- `NAVIGATOR_DNS_TTL_A_RECORD` / `NAVIGATOR_DNS_TTL_LB` (default: `60`)
- `NAVIGATOR_CF_MONITOR_INTERVAL`, `NAVIGATOR_CF_MONITOR_TIMEOUT`, `NAVIGATOR_CF_MONITOR_RETRIES`
- `NAVIGATOR_INTERNAL_CA_*` file or base64 envs for managed internal CA material

## Health & ports

- Health: `GET /health` (HTTP) or `grpc.health.v1.Health/Check` (gRPC)
- HTTP: 18010 (health/metrics, `/status`, and private `/internal/tls-bundles/:bundleID`)
- gRPC: 18011
