# FrameWorks Web Application

The FrameWorks web application dashboard — a SvelteKit frontend for managing streams, analytics, and account settings.

## Quick Start

### Prerequisites
- Node.js 18+
- npm or yarn
- Docker and Docker Compose (for backend services)

### Backend Services
From the monorepo root:
```bash
cd monorepo
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
App: http://localhost:18030

## Configuration
Copy `env.example` to `.env` and adjust as needed. Use the comments in `env.example` as the source of truth. Do not commit secrets.

## Architecture
- **SvelteKit** frontend with server-side rendering
- **GraphQL** client connecting to Bridge API Gateway
- **Authentication** handled via JWT tokens with Bridge auth proxy
- **Real-time updates** via GraphQL subscriptions over WebSocket
- **State management** using Svelte stores

Troubleshooting: ensure backend services are up and ports match the root README.
