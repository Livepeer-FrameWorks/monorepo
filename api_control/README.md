# Commodore (Control Plane)

Commodore is the control API. It owns users, streams, API tokens and exposes tenant‑scoped HTTP endpoints.

## What it does
- User authentication and authorization
- Stream management and metadata
- Tenant/stream API surface for the web app
- Service endpoints used by Helmsman (e.g. `GET /resolve-internal-name/:internal_name`)

## Architecture
- Routing: uses Quartermaster for cluster/tenant context
- Database: PostgreSQL/YugabyteDB for tenants, users, streams, API tokens
- Auth: JWT for users, service tokens for S2S

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Commodore: `cd api_control && go run ./cmd/commodore`

Configuration: copy `env.example` to `.env` and use the inline comments as reference. Do not commit secrets.

Health: `GET /health`.

Cross‑refs: see root README “Ports” and docs/IMPLEMENTATION.md for event and boundary details. 
