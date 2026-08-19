# Periscope‑Query (Analytics Read Path)

Read‑optimized analytics API. Serves tenant‑scoped queries by reading time‑series from ClickHouse. **All queries are tenant-isolated**—no cross-tenant data access.

## Why Periscope-Query?

- **Strict tenant isolation**: Every analytics query is scoped to the authenticated tenant
- **Self-hosted analytics**: Run on-premise with your own ClickHouse for complete data control
- **No cloud lock-in**: Same API and performance whether self-hosted or managed

## What it does

- gRPC endpoints for analytics slices and rollups
- Reads ClickHouse for canonical facts, 5-minute ledgers, diagnostic event timelines, and current-state overlays
- Reads ClickHouse for current state (`stream_state_current`, `node_state_current`, `artifact_state_current`)
- Serves analytics over gRPC only (all HTTP API routes removed; health/metrics only). Alerting/inference lives outside Periscope (bridge/webapp for now, `api_incidents` long term).

Billing scheduling is not part of the query service. The sibling
`periscope-metering` binary reads the same logical ClickHouse deployment, uses
PostgreSQL for source activation, fenced leases, cursors, and reservation keys,
then publishes dimensioned reports to Purser through Kafka. Query replicas can
scale independently from the singleton-per-source metering lease holder.

## Run (dev)

- Start the full stack from repo root: `docker-compose up -d`
- Or run just Periscope‑Query: `cd api_analytics_query && go run ./cmd/periscope`
- Or run the regional worker: `cd api_analytics_query && go run ./cmd/periscope-metering`

## Health & ports

- Health: `GET /health` (HTTP) or `grpc.health.v1.Health/Check` (gRPC)
- HTTP: 18004 (health/metrics only)
- gRPC: 19004
- Metering worker HTTP health/metrics: 18021

Configuration is managed centrally via `config/env`. Generate `.env` with `make env` or `frameworks config env generate`, and keep secrets in `config/env/secrets.env`. Do not commit secrets.
