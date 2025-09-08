# Quartermaster (Tenants & Clusters)

Authoritative tenant and cluster registry. Other services query Quartermaster over HTTP; no one reads each other’s DBs.

## What it does
- Tenant directory (CRUD)
- Cluster assignments and tiering
- Feature flags and resource limits
- Batch lookups and by‑cluster queries

## API (examples)
- `GET /api/v1/tenant/:id`
- `PATCH /api/v1/tenant/:id/cluster`
- `GET /api/v1/tenants/by-cluster/:cluster_id`

All endpoints require `Authorization: Bearer <SERVICE_TOKEN>`.

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Quartermaster: `cd api_tenants && go run ./cmd/quartermaster`

Configuration: copy `env.example` to `.env` and use the inline comments as reference. Do not commit secrets.

Health: `GET /health`.

Cross‑refs: see docs/IMPLEMENTATION.md for how Commodore and Foghorn consume this API. 
