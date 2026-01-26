# Navigator (DNS & Certificate Manager)

Automates public DNS records and TLS certificate issuance for platform-managed tenant subdomains. Designed for full sovereignty—currently uses Cloudflare API, with architecture enabling migration to self-hosted anycast DNS.

## Why Navigator?

- **No vendor lock-in**: DNS provider is abstracted—swap Cloudflare for self-hosted PowerDNS without code changes
- **Self-hosted path**: Architecture supports migration to fully owned anycast DNS infrastructure once ASN is acquired
- **Tenant automation**: Every paying customer gets automatic subdomain + load balancer + TLS—no manual DNS work

## What it does
- Syncs DNS records based on node inventory from Quartermaster
- "Smart Record" logic: single node -> A record, multiple nodes -> Load Balancer
- Issues TLS certificates via Let's Encrypt (DNS-01 challenge)
- Auto-renewal via background worker

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Navigator: `cd api_dns && go run ./cmd/navigator`

Configuration comes from the top-level `config/env` stack. Generate `.env` with `make env` and customise `config/env/secrets.env` for secrets. Do not commit secrets.

## Optional env vars
- `ACME_ENV` (`production` or `staging`, default: `production`)
- `NAVIGATOR_PROXY_SERVICES` (comma-separated service types to proxy via Cloudflare; default: `app,website,docs`)
- `NAVIGATOR_CERT_ALLOWED_SUFFIXES` (comma-separated domain suffix allowlist; default: `NAVIGATOR_ROOT_DOMAIN`)

## Health & ports
- Health: `GET /health` (HTTP) or `grpc.health.v1.Health/Check` (gRPC)
- HTTP: 18010 (health/metrics only)
- gRPC: 18011
