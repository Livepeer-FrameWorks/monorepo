# Purser (Billing)

Billing and subscriptions. Orchestrates usage → invoice drafts → payments. Keeps logic small; payments go through providers.

## What it does
- Accepts usage summaries from Periscope‑Query
- Writes `invoice_drafts` and invoices to PostgreSQL
- Integrates with Stripe, Mollie, and crypto flows
- Webhooks update invoice state

## Configuration
- `DATABASE_URL` — PostgreSQL/YugabyteDB DSN
- `SERVICE_TOKEN` — S2S auth
- `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`
- `MOLLIE_API_KEY`
- Optional crypto keys as applicable

Health: `GET /health`.

Cross‑refs: see docs/DATABASE.md (tables) and docs/IMPLEMENTATION.md (usage flow).
