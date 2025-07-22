# Decklog (Event Firehose)

Event ingress over gRPC. Validates, batches, and publishes to Kafka with tenant headers.

## What it does
- Receives batched events from Helmsman and others (gRPC streaming)
- Validates schemas and maps to hyphenated event types
- Publishes to `analytics_events` with `tenant_id` header

## Configuration
- `PORT` — gRPC port (default 18006)
- `KAFKA_BROKERS` — `host:port` list
- `KAFKA_TOPIC` — default `analytics_events`
- `ALLOW_INSECURE` — local dev only

Development:
- `make proto` to generate stubs
- `make build` to build

Cross‑refs: see docs/IMPLEMENTATION.md for event headers and types. 