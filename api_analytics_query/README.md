# Periscope‑Query (Analytics Read Path)

Read‑optimized analytics API. Serves tenant‑scoped queries by reading time‑series from ClickHouse and aggregates/state from PostgreSQL.

## What it does
- HTTP endpoints for analytics slices and rollups
- Reads ClickHouse for time‑series (e.g., `viewer_metrics`, `stream_events`, MVs)
- Reads PostgreSQL for control/aggregated state (`stream_analytics`)
- Produces usage summaries for Purser

## Configuration
- `CLICKHOUSE_HOST`, `CLICKHOUSE_DB`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD`
- `DATABASE_URL` — PostgreSQL/YugabyteDB DSN
- `JWT_SECRET` (if endpoints are user‑authenticated)

Cross‑refs: docs/DATABASE.md (tables/MVs), docs/IMPLEMENTATION.md (event flow), `api_billing` README (usage → invoices). 
