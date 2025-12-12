# Quartermaster (Tenants & Clusters)

Authoritative tenant and cluster registry. Other services query Quartermaster over HTTP; no one reads each other's DBs.

## Sovereign Multi-Tenancy

Quartermaster enables **per-tenant deployment flexibility**:

- **Shared clusters**: Multiple tenants on shared infrastructure (SaaS model)
- **Dedicated clusters**: Per-tenant isolated infrastructure for enterprise/compliance
- **Customer-managed**: Tenants can run their own clusters, federated with FrameWorks control plane

This enables B2B deployments where enterprise customers demand dedicated infrastructure without forking the platform.

## What it does
- Tenant directory (CRUD)
- Cluster assignments and tiering (shared → dedicated → customer-managed)
- Feature flags and resource limits per tenant
- Batch lookups and by‑cluster queries

## API (examples)
- `GET /api/v1/tenant/:id`
- `PATCH /api/v1/tenant/:id/cluster`
- `GET /api/v1/tenants/by-cluster/:cluster_id`

All endpoints require `Authorization: Bearer <SERVICE_TOKEN>`.

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Quartermaster: `cd api_tenants && go run ./cmd/quartermaster`

Configuration is shared via `config/env/base.env` and `config/env/secrets.env`. Use `make env` or `frameworks config env generate` to create `.env`, and customise `config/env/secrets.env` for secrets. Do not commit secrets.

Health: `GET /health`. 

## Health & ports
- Health: `GET /health` (HTTP) or `grpc.health.v1.Health/Check` (gRPC)
- HTTP: 18002
- gRPC: 19002
