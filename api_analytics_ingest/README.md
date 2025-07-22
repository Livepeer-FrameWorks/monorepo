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

## Configuration
- `CLICKHOUSE_HOST`, `CLICKHOUSE_DB`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD`
- `KAFKA_BROKERS`, `KAFKA_GROUP_ID`, `KAFKA_TOPICS` (default `analytics_events`), `KAFKA_CLIENT_ID`
- `DATABASE_URL` (required to enable PostgreSQL state tracking), `LOG_LEVEL`

Cross‑refs: docs/DATABASE.md (schemas, MVs); docs/IMPLEMENTATION.md (event headers/types).
