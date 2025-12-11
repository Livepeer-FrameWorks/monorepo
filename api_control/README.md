# Commodore (Control Plane)

Commodore is the control API. It owns users, streams, API tokens and exposes tenant‑scoped gRPC endpoints (HTTP for health/metrics only).

## What it does
- User authentication and authorization
- Stream management and metadata
- Tenant/stream API surface for the web app
- Resolution endpoints used by Helmsman (internal name, playback ID)

## Architecture
- Routing: uses Quartermaster for cluster/tenant context
- Database: PostgreSQL/YugabyteDB for tenants, users, streams, API tokens
- Auth: JWT for users, service tokens for S2S

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Commodore: `cd api_control && go run ./cmd/commodore`

Configuration comes from the shared `config/env` layers. Run `make env` (or `frameworks config env generate`) to materialize `.env` before starting the stack. Update `config/env/secrets.env` for local secrets. Do not commit secrets.

Key secret:

- `TURNSTILE_AUTH_SECRET_KEY` – Cloudflare Turnstile secret used to validate registration and login requests. Optional for local development (use the Cloudflare test secret).

Health: `GET /health`.

Cross‑refs: see root README "Ports" for stack overview. 

## Health & ports
- Health: `GET /health` (HTTP) or `grpc.health.v1.Health/Check` (gRPC)
- HTTP: 18001
- gRPC: 19001
