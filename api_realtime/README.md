# Signalman (Realtime WebSocket Hub)

Consumes analytics events from Kafka and broadcasts them to WebSocket channels for dashboards and realtime UIs.

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Signalman: `cd api_realtime && go run ./cmd/signalman`

Configuration: copy `env.example` to `.env` and use the inline comments as reference. Do not commit secrets.

## WebSocket endpoints
- `/ws/streams` — stream lifecycle
- `/ws/analytics` — client lifecycle, viewer metrics
- `/ws/system` — node, routing, infra
- `/ws` — all events

## Tenant isolation
- Events with a tenant scope are only broadcast to clients in the same tenant context. System channel is global.

Cross‑refs: docs/IMPLEMENTATION.md for channel mapping and headers. 
