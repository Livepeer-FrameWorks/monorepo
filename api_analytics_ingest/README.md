# Periscope‑Ingest (Analytics Write Path)

Consumes analytics events from Kafka and writes time‑series to ClickHouse. When configured with `DATABASE_URL`, also reduces
events into PostgreSQL (`stream_analytics`) for stream state tracking.

## Responsibilities
- Consume `analytics_events` with tenant headers
- Validate/normalize event payloads (Decklog already validates)
- Insert into ClickHouse tables: `stream_events`, `connection_events`, `stream_health_metrics`, `track_list_events`, `node_metrics`, `usage_records`
- Reduce stream state into PostgreSQL `stream_analytics`

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

Configuration: copy `env.example` to `.env` and use the inline comments as reference. Do not commit secrets.

## Related
- Root `README.md` (ports, stack overview)
- `docs/DATABASE.md` (schemas, MVs)

Cross‑refs: docs/DATABASE.md (schemas, MVs); docs/IMPLEMENTATION.md (event headers/types).
