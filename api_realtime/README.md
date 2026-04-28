# Signalman (Realtime gRPC Hub)

Consumes analytics and service-plane events from Kafka and broadcasts them to tenant-scoped gRPC subscription streams for Gateway/dashboard realtime UIs. **Strict tenant isolation**—events never leak across tenant boundaries.

## Why Signalman?

- **Tenant-isolated broadcasts**: Each tenant's subscription streams only receive their own events
- **Self-hosted ready**: Run your own Kafka + Signalman for on-premise real-time dashboards
- **No cloud dependencies**: Kafka + Signalman can run entirely on-premise

## Run (dev)

- Start the full stack from repo root: `docker-compose up -d`
- Or run just Signalman: `cd api_realtime && go run ./cmd/signalman`

Configuration comes from the top-level `config/env` stack. Generate `.env` with `make env` (or `frameworks config env generate`) and customise `config/env/secrets.env` for secrets. Do not commit secrets.

## Tenant isolation

- Events with a tenant scope are only broadcast to clients in the same tenant context. System channel is global.
- `api_request_batch` service events are intentionally ignored and do not hit realtime channels.

## Health & ports

- Health: `GET /health` (HTTP) or `grpc.health.v1.Health/Check` (gRPC)
- HTTP: 18009 (health/metrics only)
- gRPC: 19005
