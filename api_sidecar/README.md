# Helmsman (Edge Sidecar)

Edge sidecar for MistServer. Validates streams via Commodore, collects metrics, and forwards events to Decklog (gRPC).

## What it does
- Handles MistServer webhooks (push, default stream, recording)
- Periodic client/node metrics collection
- Resolves `tenant_id` via Commodore (`/resolve-internal-name/:internal_name`)
- Batches and sends events to Decklog with `tenant_id`

## Event types
- `stream-ingest`, `stream-view`, `stream-lifecycle`, `stream-buffer`, `stream-end`
- `user-connection`, `push-lifecycle`, `recording-lifecycle`, `track-list`, `client-lifecycle`

## Deployment model
- One instance per MistServer node
- Configured with node identity and cluster Decklog target

## Run (dev)
- Typically runs alongside MistServer. For local stack: `docker-compose up -d`
- Or run just Helmsman: `cd api_sidecar && go run ./cmd/helmsman`

Configuration is shared via the repo-level `config/env` files. Run `make env` / `frameworks config env generate` to create `.env`, then adjust `config/env/secrets.env` as needed. See `docs/configuration.md`. Do not commit secrets.

Health: `GET /health`.

Cross‑refs: docs/IMPLEMENTATION.md for event format; docs/DATABASE.md for ClickHouse sinks. 

## Health & port
- Health: `GET /health`
- HTTP: 18007
