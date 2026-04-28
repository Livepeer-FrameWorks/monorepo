# Decklog (Event Firehose)

Event ingress over gRPC. Validates analytics and service events, then publishes to Kafka with tenant headers.

## Why Decklog?

- **Tenant-scoped events**: Every event carries `tenant_id` header—strict isolation from ingestion through storage
- **Self-hosted ready**: Run your own Kafka cluster for complete event ownership
- **No external dependencies**: Pure Go, no cloud services required

## What it does

- Receives enriched Mist trigger events from Foghorn via unary gRPC `SendEvent`
- Receives service-plane events via unary gRPC `SendServiceEvent`
- Validates tenant attribution and maps trigger payloads to analytics event types
- Publishes analytics events to `analytics_events` and service-plane events to `service_events` with `tenant_id` headers

## Run (dev)

- Start the full stack from repo root: `docker-compose up -d`
- Or run just Decklog: `cd api_firehose && go run ./cmd/decklog`

## Port

- gRPC: 18006
- HTTP health/metrics: 18026 (`DECKLOG_METRICS_PORT`)

## Health

- gRPC: `grpc.health.v1.Health/Check` (see docker-compose healthcheck example)
- HTTP: `GET /health` on the metrics port

Configuration lives in `config/env/base.env` and `config/env/secrets.env`. Generate `.env` with `make env` or `frameworks config env generate`, and keep secrets in the git-ignored `config/env/secrets.env`. Do not commit secrets.

Development:

- `make proto` to generate stubs
- `make build` to build
