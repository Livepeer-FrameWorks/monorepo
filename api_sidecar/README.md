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

## Configuration
- `COMMODORE_URL`, `SERVICE_TOKEN`
- `DECKLOG_GRPC_TARGET`, `DECKLOG_ALLOW_INSECURE`
- Node identity/env (region, hostname)

Health: `GET /health`.

Cross‑refs: docs/IMPLEMENTATION.md for event format; docs/DATABASE.md for ClickHouse sinks. 