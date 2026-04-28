# FrameWorks Marketing Website

React/Vite marketing site for FrameWorks product pages, pricing, contact forms, and docs/app entry points.

## Development

Use the root pnpm workspace:

```bash
pnpm install
cp website_marketing/env.example website_marketing/.env
pnpm --dir website_marketing dev
```

The standalone dev server runs on `http://localhost:9004` by default. In the local compose stack, the marketing container listens on `http://localhost:18031` and the nginx route is `http://localhost:18090/marketing`.

## Configuration

Key browser variables live in `env.example`:

- `VITE_CONTACT_API_URL` points contact and newsletter forms at Steward (`api_forms`).
- `VITE_TURNSTILE_FORMS_SITE_KEY` enables Cloudflare Turnstile for public forms.
- `VITE_APP_URL` points calls to action at the web application.

## Build

```bash
pnpm --dir website_marketing build
```
