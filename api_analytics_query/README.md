# Periscope‑Query (Analytics Read Path)

Read‑optimized analytics API. Serves tenant‑scoped queries by reading time‑series from ClickHouse.

## What it does
- gRPC endpoints for analytics slices and rollups
- Reads ClickHouse for time‑series (e.g., `stream_events`, `connection_events`, MVs)
- Reads ClickHouse for current state (`live_streams`, `live_nodes`, `live_artifacts`)
- Reads PostgreSQL for billing cursor tracking only (`billing_cursors` table)
- Produces usage summaries for Purser (billing service)
- Exposes raw stream health samples (`/analytics/stream-health`) that downstream services/apps should consume directly. Alerting/inference lives outside Periscope (bridge/webapp for now, `api_incidents` long term).

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Periscope‑Query: `cd api_analytics_query && go run ./cmd/periscope`

## Health & ports
- Health: `GET /health` (HTTP) or `grpc.health.v1.Health/Check` (gRPC)
- HTTP: 18004
- gRPC: 19004

Configuration is managed centrally via `config/env`. Generate `.env` with `make env` or `frameworks config env generate`, and keep secrets in `config/env/secrets.env`. Do not commit secrets. 
