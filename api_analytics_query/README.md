# Periscope‑Query (Analytics Read Path)

Read‑optimized analytics API. Serves tenant‑scoped queries by reading time‑series from ClickHouse and aggregates/state from PostgreSQL.

## What it does
- HTTP endpoints for analytics slices and rollups
- Reads ClickHouse for time‑series (e.g., `viewer_metrics`, `stream_events`, MVs)
- Reads PostgreSQL for control/aggregated state (`stream_analytics`)
- Produces usage summaries for Purser

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Periscope‑Query: `cd api_analytics_query && go run ./cmd/periscope`

## Health & port
- Health: `GET /health`
- HTTP: 18004

Configuration: copy `env.example` to `.env` and use the inline comments as reference. Do not commit secrets.

## Related
- Root `README.md` (ports, stack overview)
- `docs/DATABASE.md` (tables/MVs)

Cross‑refs: docs/DATABASE.md (tables/MVs), docs/IMPLEMENTATION.md (event flow), `api_billing` README (usage → invoices). 
