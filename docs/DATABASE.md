# FrameWorks Database Architecture

FrameWorks uses a dual-database architecture to handle state and analytics efficiently.

## PostgreSQL (YugabyteDB-compatible) — Primary/state

Used for:
- Core state data (tenants, users, streams)
- Aggregated/current state (e.g., stream_analytics)
- Billing state (invoices, drafts)
- Configuration and settings

Key tables (control plane):
- `tenants`, `users`, `streams`
- `stream_analytics` (aggregated state)
- `billing_invoices`, `invoice_drafts`

## ClickHouse — Time-series analytics

Used for:
- Viewer metrics and session data
- Network and node performance
- Routing/load-balancer decisions
- Detailed stream health metrics
- Usage records for billing aggregation

Key tables (from schema):
- `viewer_metrics` — real-time viewer statistics
- `connection_events` — viewer session analysis
- `node_metrics` — infrastructure performance
- `routing_events` — load-balancer decisions
- `stream_health_metrics` — detailed stream health
- `stream_events` — stream-scoped events with JSON `event_data`
- `track_list_events` — live track list updates
- `usage_records` — time-series usage for billing

Materialized views:
- `viewer_metrics_5m_mv` → `viewer_metrics_5m` — five-minute viewer aggregates

## Data flow

1) State changes → PostgreSQL (authoritative)
2) Time-series events → ClickHouse (with TTL + partitions)
3) Hybrid: write state to PostgreSQL and facts to ClickHouse; correlate by event IDs

## Integration points

- Periscope-Ingest: consumes Kafka, writes ClickHouse (time-series); reduces stream state into PostgreSQL when `DATABASE_URL` is set
- Periscope-Query: reads ClickHouse for analytics; reads PostgreSQL for control/aggregates; summarizes usage for Purser
- Purser: reads usage via Periscope-Query; stores invoices/drafts in PostgreSQL
- Helmsman: emits events (no DB reads)

## Development guidance

- Use PostgreSQL for business entities and current state
- Use ClickHouse for time-series facts and high-volume analytics
- Prefer materialized views for common aggregations
