# Steward (Forms Service)

Minimal contact/newsletter API for the marketing site. Steward accepts contact form submissions, optionally verifies Cloudflare Turnstile tokens, sends contact email through SMTP, and optionally subscribes newsletter signups through Listmonk.

## Run (dev)

```bash
docker-compose up forms
```

Configure the marketing site to point `VITE_CONTACT_API_URL` to this service.

## Configuration

Environment variables are generated from the repo-level `config/env` files. `api_forms/env.example` documents the service-local shape.

- `PORT` - Service port (default: 18032)
- `TURNSTILE_FORMS_SECRET_KEY` - Cloudflare Turnstile verification. Use test secret `1x0000000000000000000000000000000AA` for local development.
- `ALLOWED_ORIGINS` - Comma-separated list of allowed origins for CORS
- `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD` - Email configuration
- `FROM_EMAIL` - Sender email address
- `TO_EMAIL` - Recipient for contact form submissions
- `LISTMONK_URL`, `LISTMONK_USERNAME`, `LISTMONK_PASSWORD` - Connection to Listmonk (Newsletter)
- `DEFAULT_MAILING_LIST_ID` - ID of the Listmonk list to subscribe users to (default: 1)

## Endpoints

- `POST /api/contact`: Validate a contact request and send email through SMTP.
- `POST /api/subscribe`: Subscribe email to the configured Listmonk list. Registered only when `LISTMONK_URL` is set.
- `GET /health`: Standard service health endpoint.
- `GET /metrics`: Prometheus metrics endpoint.

## Build

```bash
make build-bin-steward
```

## Current scope

- Contact form validation for name, email, and message.
- Honeypot/behavior checks when Turnstile is not configured.
- Optional Turnstile verification for contact and subscribe requests.
- SMTP email delivery for contact requests.
- Optional Listmonk subscription with duplicate handling.
- Request counters for contact and subscribe outcomes.
