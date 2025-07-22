# FrameWorks Web Application

The FrameWorks web application dashboard — a SvelteKit frontend for managing streams, analytics, and account settings.

## 🚀 Quick Start

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
- Commodore (18001), Quartermaster (18002), Purser (18003)
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
App: http://localhost:3000

## 🔧 Configuration
Copy `env.example` to `.env` and configure:

| Variable | Description | Default |
|----------|-------------|---------|
| `VITE_API_URL` | Commodore API URL | `http://localhost:18001` |
| `VITE_MARKETING_SITE_URL` | Marketing website URL | `http://localhost:18031` |
| `VITE_RTMP_DOMAIN` | RTMP ingest host:port | `localhost:1935` |
| `VITE_HTTP_DOMAIN` | MistServer HTTP host:port | `localhost:8080` |
| `VITE_CDN_DOMAIN` | Delivery domain | `localhost:8080` |
| `VITE_RTMP_PATH` | RTMP path | `/live` |
| `VITE_HLS_PATH` | HLS path | `/hls` |
| `VITE_WEBRTC_PATH` | WebRTC path | `/webrtc` |
| `VITE_EMBED_PATH` | Embed player path | `/embed` |

## 🏗️ Architecture
- Svelte stores for auth
- Axios client pointing to Commodore
- JWT handling with interceptors

Troubleshooting: ensure backend services are up and ports match the root README.
