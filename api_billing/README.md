# Purser (Billing)

Billing and subscriptions. Orchestrates usage → invoice drafts → payments. Works with self-hosted, hybrid, and fully managed deployments.

## Deployment Flexibility

- **Self-hosted**: Track usage locally, disable payment integrations if running internal infrastructure
- **Hybrid**: Bill for FrameWorks-hosted resources while self-hosting edge nodes
- **Fully managed**: Stripe/Mollie hooks and payment creation are implemented, but end‑to‑end billing flows are still being wired

## What it does
- Accepts usage summaries from Periscope‑Query
- Writes draft and final invoices to `purser.billing_invoices` (status = draft/pending/paid/etc.)
- Integrates with Stripe/Mollie webhooks and payment creation (crypto is placeholder)
- Webhooks update invoice state

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Purser: `cd api_billing && go run ./cmd/purser`

Configuration is handled via the central env files under `config/env`. Run `make env` (or `frameworks config env generate`) to build `.env`, and update `config/env/secrets.env` with local secrets. Do not commit secrets.

Health: `GET /health`.

## Health & ports
- Health: `GET /health` (HTTP) or `grpc.health.v1.Health/Check` (gRPC)
- HTTP: 18003 (health/metrics only)
- gRPC: 19003

Notes
- Some handlers are stubs or partial; see ROADMAP for missing invoicing/aggregation pieces.
