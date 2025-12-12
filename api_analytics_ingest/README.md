# Periscope‑Ingest (Analytics Write Path)

Consumes analytics events from Kafka and writes time‑series data exclusively to ClickHouse. **Self-hostable with on-premise ClickHouse**—your analytics data stays on your infrastructure.

## Why Periscope-Ingest?

- **Data sovereignty**: Run ClickHouse on your own servers for complete analytics data ownership
- **Tenant isolation**: All events carry `tenant_id` headers—strict per-tenant partitioning
- **No cloud dependencies**: Kafka + ClickHouse can run entirely on-premise

## Responsibilities
- Consume `analytics_events` topic with tenant headers
- Validate/normalize event payloads (Decklog already validates)
- Insert into ClickHouse tables: `stream_events`, `connection_events`, `stream_health_metrics`, `track_list_events`, `node_metrics`, `live_streams`, `live_nodes`, `live_artifacts`

## Event → table mapping
- `stream-ingest`, `stream-view`, `stream-lifecycle`, `stream-buffer`, `stream-end` → `stream_events`
- `user-connection` → `connection_events`
- `client-lifecycle` → `stream_health_metrics`
- `node-lifecycle` → `node_metrics`
- `track-list` → `track_list_events`
- `recording-lifecycle` → `stream_events` (used in billing queries)

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Periscope‑Ingest: `cd api_analytics_ingest && go run ./cmd/periscope`

Configuration is provided via the repo-level env layers (`config/env/base.env` + `config/env/secrets.env`). Run `make env` or `frameworks config env generate` to build `.env`, then adjust `config/env/secrets.env` as needed. Do not commit secrets.

## Health & port
- Health: `GET /health`
- HTTP: 18005 (health/metrics only)
