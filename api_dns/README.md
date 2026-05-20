# Navigator (DNS & Certificate Manager)

Automates public DNS records, TLS certificate issuance, and internal service certificates for platform-managed domains. Navigator uses Cloudflare for root/API/web/admin DNS and Bunny DNS for delegated media cluster zones.

## Why Navigator?

- **Provider boundary**: Cloudflare remains the root provider; Bunny owns cluster-scoped media/edge zones such as `media-eu.example.com`, global media entrypoint zones such as `edge-ingest.example.com`, and the shared tenant alias zone `cdn.example.com`
- **Self-hosted path**: Media cluster DNS is delegated by cluster, so edge growth does not consume Cloudflare Load Balancing endpoints
- **Managed service automation**: Public service records, cluster-scoped media records, load balancer health checks, and TLS material are reconciled from control-plane inventory

## What it does

- Syncs root/API/web/admin records in Cloudflare from Quartermaster node/service inventory
- Syncs cluster-scoped media/edge records, global media entrypoints, and paid tenant aliases in Bunny DNS from Quartermaster node/service inventory
- Cloudflare root/global load balancers are split into one pool per Quartermaster cluster and use proximity steering when at least two pools have coordinates
- Bunny media records are A-record sets under `<cluster>.<root>` zones with geolocation Smart Routing when node coordinates are available
- Foghorn is published at both `foghorn.<cluster>.<root>` and the zone apex `<cluster>.<root>` so the cluster domain remains the default playback/routing entrypoint
- Each Bunny media cluster gets a `cluster:<cluster>` TLS bundle for `<cluster>.<root>` and `*.<cluster>.<root>`. Foghorn loads the same bundle for public/control TLS and distributes the wildcard site address to connected edges via ConfigSeed.
- Global Bunny entrypoints use two platform TLS bundles: `platform:pool-multi` for `foghorn/chandler/livepeer.<root>` and `platform:edge-multi` for `edge*.<root>`.
- Tenant aliases under `cdn.<root>` use `tenant:<tenant_id>` bundles covering `<tenant>.cdn.<root>`, `*.<tenant>.cdn.<root>`, and verified custom domains.
- Issues TLS certificates via Let's Encrypt DNS-01 challenges using Cloudflare or Bunny based on the delegated zone
- Issues and stores internal gRPC certificates from Navigator's internal CA
- Auto-renewal via background worker

## Run (dev)

- Start the full stack from repo root: `docker-compose up -d`
- Or run just Navigator: `cd api_dns && go run ./cmd/navigator`

Configuration comes from the top-level `config/env` stack. Generate `.env` with `make env` and customise `config/env/secrets.env` for secrets. Do not commit secrets.

## Optional env vars

- `ACME_ENV` (`production` or `staging`, default: `production`)
- `BUNNY_API_KEY` (required for Bunny-managed media cluster DNS, global media entrypoints, and tenant aliases; when unset, Navigator logs an explicit warning and uses Cloudflare cluster-scoped fallback where possible)
- `BUNNY_API_BASE_URL` (optional Bunny API override for tests)
- `NAVIGATOR_GOOGLE_TRUST_EAB_KID` / `NAVIGATOR_GOOGLE_TRUST_EAB_HMAC_KEY` (enables Google Trust Services ACME fallback for rate-limit headroom; when unset, Navigator uses Let's Encrypt only)
- `NAVIGATOR_ACME_CA_ORDER` (optional override for CA order; default is Let's Encrypt, with Google Trust added automatically when EAB secrets are present)
- `NAVIGATOR_GOOGLE_TRUST_DIRECTORY_URL` (optional override; defaults to Google Trust Services production ACME)
- `NAVIGATOR_PROXY_SERVICES` (comma-separated service types to proxy via Cloudflare; default: `bridge,chartroom,chatwoot,foredeck,grafana,listmonk,logbook,metabase,steward`)
- `NAVIGATOR_CERT_ALLOWED_SUFFIXES` (comma-separated domain suffix allowlist; default: `BRAND_DOMAIN`)
- `NAVIGATOR_DNS_RECONCILE_INTERVAL_SECONDS` (default: `60`)
- `NAVIGATOR_DNS_TTL_A_RECORD` / `NAVIGATOR_DNS_TTL_LB` (default: `60`)
- `NAVIGATOR_CF_MONITOR_INTERVAL`, `NAVIGATOR_CF_MONITOR_TIMEOUT`, `NAVIGATOR_CF_MONITOR_RETRIES`
- `NAVIGATOR_INTERNAL_CA_*` file or base64 envs for managed internal CA material

## Health & ports

- Health: `GET /health` (HTTP) or `grpc.health.v1.Health/Check` (gRPC)
- HTTP: 18010 (health/metrics, `/status`, and private `/internal/tls-bundles/:bundleID`)
- gRPC: 18011
