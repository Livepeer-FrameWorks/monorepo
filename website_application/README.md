# FrameWorks Web Application

The FrameWorks web application dashboard — a SvelteKit frontend for managing streams, analytics, and account settings.

## Quick Start

### Prerequisites

- Node.js 24
- pnpm 10+
- Docker and Docker Compose (for backend services)

### Backend Services

From the monorepo root:

```bash
docker-compose up -d
```

This starts (ports per root README):

- PostgreSQL (5432)
- ClickHouse (8123/9000)
- Kafka in KRaft mode (29092/9092)
- MistServer (4242, 1935, 8080)
- Bridge (18000), Commodore (18001), Quartermaster (18002), Purser (18003)
- Periscope-Query (18004), Periscope-Ingest (18005), Decklog (18006)
- Helmsman (18007), Foghorn (18008), Signalman (18009), Navigator (18010), Privateer (18012), Skipper (18013)
- Media, support, and back-office services such as MistServer, Deckhand, Chandler, and Steward
- Nginx gateway (18090)

### Frontend Setup

```bash
pnpm install
cd website_application
cp env.example .env
pnpm dev
```

The app consumes built player/studio workspace packages. For release validation,
build the app **with its dependencies** from the monorepo root:

```bash
pnpm --filter frameworks-frontend... run build
```

Set `VITE_APP_URL` to the URL/base path used by the preview. A plain build from
`website_application` alone can reuse stale SDK bundles even when SDK source
tests pass. Rebuild dependencies after player/studio changes before browser tests.

App URLs:

- Local dev server with `env.example`: http://localhost:3001
- Docker (webapp service in compose): http://localhost:18030
- Dev reverse-proxy route: http://localhost:18090/app

## GraphQL Usage & Tenant Context

- HTTP: the app adds `Authorization: Bearer <JWT>` when logged in.
- WebSocket: JWT is passed in the connection init payload for subscriptions.
- Tenant scope: when a user is logged in, the app includes `X-Tenant-ID` on requests to simplify scoping in control‑plane handlers. Public marketing/player calls do not set this header.

## Configuration

Copy `env.example` to `.env` for standalone frontend dev. Deployment/browser `VITE_*` values for the stack are generated from the root config via `make env`.

Key variables:

- `VITE_TURNSTILE_AUTH_SITE_KEY` – Cloudflare Turnstile site key used for registration and login forms. For local automated testing, use test site key `1x00000000000000000000AA` and configure Commodore's `TURNSTILE_AUTH_SECRET_KEY` with the matching test secret `1x0000000000000000000000000000000AA`. Both are required; real secrets reject test tokens. Never use test keys in production. See [Cloudflare's testing guide](https://developers.cloudflare.com/turnstile/troubleshooting/testing/).
- `DEV_PROXY_GATEWAY_URL` / `VITE_GATEWAY_URL` – Bridge/nginx URL used by the frontend during local development.
- `VITE_STREAMING_EDGE_URL` – public playback edge URL used by viewer routes.

## Architecture

- **SvelteKit** frontend with server-side rendering
- **GraphQL** client connecting to Bridge API Gateway
- **Authentication** handled via JWT tokens with Bridge auth proxy
- **Real-time updates** via GraphQL subscriptions over WebSocket
- **State management** using Svelte 5 runes and Svelte stores where existing code still uses stores

Troubleshooting: ensure backend services are up and ports match the root README.
