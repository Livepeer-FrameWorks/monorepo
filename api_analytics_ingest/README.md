# Periscope‑Ingest (Analytics Write Path)

Consumes analytics events from Kafka and writes time‑series data exclusively to ClickHouse. **Self-hostable with on-premise ClickHouse**—your analytics data stays on your infrastructure.

## Why Periscope-Ingest?

- **Data sovereignty**: Run ClickHouse on your own servers for complete analytics data ownership
- **Tenant isolation**: All events carry `tenant_id` headers—strict per-tenant partitioning
- **No cloud dependencies**: Kafka + ClickHouse can run entirely on-premise

## Responsibilities

- Consume `analytics_events` topic with tenant headers
- Validate/normalize event payloads (Decklog already validates)
- Insert into ClickHouse tables: `stream_event_log`, `viewer_connection_events`, `stream_health_samples`, `track_list_events`, `client_qoe_samples`, `node_metrics_samples`, `stream_state_current`, `node_state_current`, `artifact_state_current`, `artifact_events`, `routing_decisions`, `storage_snapshots`, `processing_events`

## Event → table mapping

- `viewer_connect` / `viewer_disconnect` → `viewer_connection_events`
- `stream_buffer`, `stream_end`, `push_rewrite`, `play_rewrite`, `stream_source`, `push_end`, `push_out_start`, `recording_complete` → `stream_event_log`
- `stream_track_list` → `track_list_events`
- `stream_lifecycle_update` → `stream_state_current` + `stream_event_log`
- `node_lifecycle_update` → `node_state_current` + `node_metrics_samples`
- `client_lifecycle_update` → `client_qoe_samples`
- `load_balancing` → `routing_decisions`
- `clip_lifecycle`, `dvr_lifecycle` → `artifact_state_current` + `artifact_events`
- `storage_snapshot` → `storage_snapshots`
- `process_billing` → `processing_events`

## Run (dev)

- Start the full stack from repo root: `docker-compose up -d`
- Or run just Periscope‑Ingest: `cd api_analytics_ingest && go run ./cmd/periscope`

Configuration is provided via the repo-level env layers (`config/env/base.env` + `config/env/secrets.env`). Run `make env` or `frameworks config env generate` to build `.env`, then adjust `config/env/secrets.env` as needed. Do not commit secrets.

## Health & port

- Health: `GET /health`
- HTTP: 18005 (health/metrics only)
