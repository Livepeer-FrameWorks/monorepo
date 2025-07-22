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

## Configuration
- `DATABASE_URL` — PostgreSQL/YugabyteDB DSN
- `SERVICE_TOKEN` — S2S auth token
- `PORT` — HTTP port (default 18002)
- `LOG_LEVEL`, `GIN_MODE`

Health: `GET /health`.

Cross‑refs: see docs/IMPLEMENTATION.md for how Commodore and Foghorn consume this API. 