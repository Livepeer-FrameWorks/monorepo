# Purser (Billing)

Billing and subscriptions. Orchestrates usage → invoice drafts → payments. Keeps logic small; payments go through providers.

## What it does
- Accepts usage summaries from Periscope‑Query
- Writes `invoice_drafts` and invoices to PostgreSQL
- Integrates with Stripe, Mollie, and crypto flows
- Webhooks update invoice state

## Run (dev)
- Start the full stack from repo root: `docker-compose up -d`
- Or run just Purser: `cd api_billing && go run ./cmd/purser`

Configuration: copy `env.example` to `.env` and use the inline comments as reference. Do not commit secrets.

Health: `GET /health`.

Cross‑refs: see docs/DATABASE.md (tables) and docs/IMPLEMENTATION.md (usage flow).

## Health & port
- Health: `GET /health`
- HTTP: 18003

Notes
- Some handlers are stubs or partial; see ROADMAP for missing invoicing/aggregation pieces.
