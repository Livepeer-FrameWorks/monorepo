# Decklog (Event Firehose)

Event ingress over gRPC. Validates, batches, and publishes to Kafka with tenant headers. **High-throughput event ingestion** designed for self-hosted and hybrid deployments.

## Why Decklog?

- **Tenant-scoped events**: Every event carries `tenant_id` header—strict isolation from ingestion through storage
- **Self-hosted ready**: Run your own Kafka cluster for complete event ownership
- **No external dependencies**: Pure Go, no cloud services required

## What it does
- Receives batched events from Foghorn (gRPC streaming)
- Validates schemas and maps to hyphenated event types
- Publishes to `analytics_events` with `tenant_id` header

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Decklog: `cd api_firehose && go run ./cmd/decklog`

## Port
- gRPC: 18006

## Health
- gRPC: `decklog.DecklogService/CheckHealth` (see docker-compose healthcheck example)

Configuration lives in `config/env/base.env` and `config/env/secrets.env`. Generate `.env` with `make env` or `frameworks config env generate`, and keep secrets in the git-ignored `config/env/secrets.env`. Do not commit secrets.

Development:
- `make proto` to generate stubs
- `make build` to build 
