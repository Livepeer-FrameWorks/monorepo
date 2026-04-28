# Purser (Billing)

Billing and subscriptions. Orchestrates usage → invoice drafts → payments. Works with self-hosted, hybrid, and fully managed deployments.

## Deployment Flexibility

- **Self-hosted**: Track usage locally, disable payment integrations if running internal infrastructure
- **Hybrid**: Bill for FrameWorks-hosted resources while self-hosting edge nodes
- **Fully managed**: Stripe/Mollie hooks, subscription checkout flows, prepaid top-ups, and x402 settlement paths exist; invoice finalization, enrichment, and operational payment reconciliation still need careful end-to-end validation

## What it does

- Consumes usage summaries from the `billing.usage_reports` Kafka topic produced by Periscope‑Query
- Writes draft and final invoices to `purser.billing_invoices` (status = draft/pending/paid/etc.)
- Integrates with Stripe/Mollie webhooks, subscription checkout, prepaid card top-ups, crypto deposit-address top-ups, and x402 wallet/API settlement
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

- Billing has working service surfaces across subscriptions, prepaid balance, usage records, card top-ups, crypto top-up addresses, and x402. Treat full invoice/payment reconciliation as the highest-risk integration area and verify against `api_billing/internal/handlers/jobs.go` and `api_billing/internal/grpc/server.go` before documenting a complete billing workflow.
