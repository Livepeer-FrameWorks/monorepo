# Decklog (Event Firehose)

Event ingress over gRPC. Validates, batches, and publishes to Kafka with tenant headers.

## What it does
- Receives batched events from Helmsman and others (gRPC streaming)
- Validates schemas and maps to hyphenated event types
- Publishes to `analytics_events` with `tenant_id` header

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Decklog: `cd api_firehose && go run ./cmd/decklog`

## Port
- gRPC: 18006

## Health
- gRPC: `decklog.DecklogService/CheckHealth` (see docker-compose healthcheck example)

Configuration lives in `config/env/base.env` and `config/env/secrets.env`. Generate `.env` with `make env` or `frameworks config env generate`, and keep secrets in the git-ignored `config/env/secrets.env`. See `docs/configuration.md` for details. Do not commit secrets.

## Related
- Root `README.md` (ports, stack overview)
- `docs/IMPLEMENTATION.md` (event headers/types)

Development:
- `make proto` to generate stubs
- `make build` to build

Cross‑refs: see docs/IMPLEMENTATION.md for event headers and types. 
