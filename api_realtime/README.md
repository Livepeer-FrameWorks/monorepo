# Signalman (Realtime WebSocket Hub)

Consumes analytics events from Kafka and broadcasts them to WebSocket channels for dashboards and realtime UIs.

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Signalman: `cd api_realtime && go run ./cmd/signalman`

Configuration: copy `env.example` to `.env` and use the inline comments as reference. Do not commit secrets.

## Tenant isolation
- Events with a tenant scope are only broadcast to clients in the same tenant context. System channel is global.

Cross‑refs: docs/IMPLEMENTATION.md for channel mapping and headers. 

## Health & port
- Health: `GET /health`
- HTTP: 18009
