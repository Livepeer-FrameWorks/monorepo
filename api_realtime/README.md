# Signalman (Realtime WebSocket Hub)

Consumes analytics events from Kafka and broadcasts them to WebSocket channels for dashboards and realtime UIs.

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Signalman: `cd api_realtime && go run ./cmd/signalman`

Configuration comes from the top-level `config/env` stack. Generate `.env` with `make env` (or `frameworks config env generate`) and customise `config/env/secrets.env` for secrets. Do not commit secrets.

## Tenant isolation
- Events with a tenant scope are only broadcast to clients in the same tenant context. System channel is global.

## Health & ports
- Health: `GET /health` (HTTP) or `grpc.health.v1.Health/Check` (gRPC)
- HTTP: 18009
- gRPC: 19005
