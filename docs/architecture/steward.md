# Steward - Marketing Forms Intake

Steward (`api_forms/`, port 18032) is a minimal, stateless intake service for the
marketing site: contact form submissions and newsletter signups. It is
marketing-plane only — no database, no tenant model, no JWT — and is intentionally
absent from the product feature registry (`docs/platform-features.yaml`); it is
plumbing for Foredeck, not a product capability.

## Architecture

```
Foredeck (website_marketing)
  VITE_CONTACT_API_URL
        │
        ▼
┌───────────────────┐   POST /api/contact    ┌──────┐
│      Steward      │───────────────────────▶│ SMTP │ → TO_EMAIL inbox
│    (api_forms)    │                        └──────┘
│  Turnstile verify │   POST /api/subscribe  ┌──────────┐
│  honeypot/behavior│───────────────────────▶│ Listmonk │ → mailing list
└───────────────────┘   (only when           └──────────┘
                         LISTMONK_URL set)
```

## Endpoints

| Endpoint              | Purpose                                                                                  |
| --------------------- | ---------------------------------------------------------------------------------------- |
| `POST /api/contact`   | Validate submission, send email via SMTP to `TO_EMAIL`                                   |
| `POST /api/subscribe` | Subscribe email to the configured Listmonk list; registered only when `LISTMONK_URL` set |
| `GET /health`         | Standard health endpoint                                                                 |
| `GET /metrics`        | Prometheus counters (`contact_requests_total`, `subscribe_requests_total` by status)     |

## Bot gating

- **Cloudflare Turnstile** when `TURNSTILE_FORMS_SECRET_KEY` is set (test secret
  `1x0000000000000000000000000000000AA` for local dev).
- **Fallback when Turnstile is not configured:** honeypot field check plus
  behavioral validation (`internal/validation/submission.go`) — form-shown vs
  submitted timing and mouse/typing signals.
- Submitter PII (name/email) is redacted in logs (`internal/handlers/redaction.go`).

## Configuration

Environment shape is documented in `api_forms/env.example`; key variables:
`PORT` (default 18032), `TURNSTILE_FORMS_SECRET_KEY`, `ALLOWED_ORIGINS` (CORS),
`SMTP_HOST/PORT/USER/PASSWORD`, `FROM_EMAIL`, `TO_EMAIL`, `LISTMONK_URL`,
`LISTMONK_API_USERNAME`, `LISTMONK_API_TOKEN`, `DEFAULT_MAILING_LIST_ID`.

## Key Files

- `api_forms/cmd/steward/main.go` - wiring: SMTP, Turnstile, optional Listmonk, metrics
- `api_forms/internal/handlers/` - contact/subscribe handlers, log redaction
- `api_forms/internal/validation/submission.go` - honeypot + behavioral bot checks
- `pkg/clients/listmonk/` - Listmonk API client (duplicate-subscription handling)

## Gotchas

- The subscribe route does not exist unless `LISTMONK_URL` **and** the Listmonk
  credentials (`LISTMONK_API_USERNAME`, `LISTMONK_API_TOKEN`) are all configured —
  a 404 there is a configuration signal, not a bug.
- Steward has no persistence: a submission that passes validation but fails SMTP
  delivery is lost apart from logs/metrics. Acceptable for its marketing-plane
  scope; do not route anything durable through it.
