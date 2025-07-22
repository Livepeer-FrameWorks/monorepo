# Signalman (Realtime WebSocket Hub)

Consumes analytics events from Kafka and broadcasts them to WebSocket channels for dashboards and realtime UIs.

## Configuration
- `KAFKA_BROKERS` — comma-separated broker list
- `KAFKA_TOPICS` — defaults to `analytics_events`
- `KAFKA_CLIENT_ID` — defaults to `signalman`
- `KAFKA_CONSUMER_GROUP` — e.g. `signalman-group`
- `PORT` — HTTP port (default 18009)
- `GIN_MODE`, `LOG_LEVEL`

## WebSocket endpoints
- `/ws/streams` — stream lifecycle
- `/ws/analytics` — client lifecycle, viewer metrics
- `/ws/system` — node, routing, infra
- `/ws` — all events

## Tenant isolation
- Events with a tenant scope are only broadcast to clients in the same tenant context. System channel is global.

Cross‑refs: docs/IMPLEMENTATION.md for channel mapping and headers. 