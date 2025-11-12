# FrameWorks Web Application

The FrameWorks web application dashboard — a SvelteKit frontend for managing streams, analytics, and account settings.

## Quick Start

### Prerequisites

- Node.js 18+
- npm
- Docker and Docker Compose (for backend services)

### Backend Services

From the monorepo root:

```bash
docker-compose up -d
```

This starts (ports per root README):

- PostgreSQL (5432)
- ClickHouse (8123/9000)
- Kafka + Zookeeper (29092/9092, 2181)
- MistServer (4242, 1935, 8080)
- Bridge (18000), Commodore (18001), Quartermaster (18002), Purser (18003)
- Periscope‑Query (18004), Periscope‑Ingest (18005), Decklog (18006)
- Helmsman (18007), Foghorn (18008), Signalman (18009)
- Nginx gateway (18090)

### Frontend Setup

```bash
cd monorepo/website_application
npm install
cp env.example .env
npm run dev
```

App URLs:

- Local dev server (npm run dev): http://localhost:3000
- Docker (webapp service in compose): http://localhost:18030

## GraphQL Usage & Tenant Context

- HTTP: the app adds `Authorization: Bearer <JWT>` when logged in.
- WebSocket: JWT is passed in the connection init payload for subscriptions.
- Tenant scope: when a user is logged in, the app includes `X-Tenant-ID` on requests to simplify scoping in control‑plane handlers. Public marketing/player calls do not set this header.

## Configuration

Copy `env.example` to `.env` and adjust as needed. Do not commit secrets.

Key variables:

- `VITE_TURNSTILE_AUTH_SITE_KEY` – Cloudflare Turnstile site key used for registration and login forms. Use the Cloudflare test key (`1x0000000000000000000000000000000AA`) during local development.

## Architecture

- **SvelteKit** frontend with server-side rendering
- **GraphQL** client connecting to Bridge API Gateway
- **Authentication** handled via JWT tokens with Bridge auth proxy
- **Real-time updates** via GraphQL subscriptions over WebSocket
- **State management** using Svelte stores

Troubleshooting: ensure backend services are up and ports match the root README.
