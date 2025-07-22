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

## Configuration

| Variable | Required | Description |
|----------|----------|-------------|
| `DATABASE_URL` | Yes | PostgreSQL/YugabyteDB DSN |
| `JWT_SECRET` | Yes | JWT signing secret |
| `SERVICE_TOKEN` | Yes | Service‑to‑service auth token |
| `PORT` | No | HTTP port (default: 18001) |
| `GIN_MODE` | No | `debug` or `release` |
| `LOG_LEVEL` | No | `debug|info|warn|error` |

Health: `GET /health`.

Cross‑refs: see root README “Ports” and docs/IMPLEMENTATION.md for event and boundary details. 