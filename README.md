# FrameWorks

> Warning: This stack is pre‑release and experimental. Do not deploy to production. Interfaces and schemas change frequently. Use for local development and evaluation only.

**Sovereign SaaS for live video.** Run on your infrastructure, ours, or both—no vendor lock-in.

An open streaming stack for live video: apps, real‑time APIs, and analytics. Services are narrowly scoped. Frontend uses GraphQL; service-to-service uses HTTP/gRPC APIs; analytics and realtime use Kafka events. Each service owns its data (no cross‑DB access).

## NPM packages

FrameWorks ships official player and browser-ingest packages:

**FrameWorks Player**

[![npm](https://img.shields.io/npm/v/%40livepeer-frameworks%2Fplayer-react?label=@livepeer-frameworks%2Fplayer-react)](https://www.npmjs.com/package/@livepeer-frameworks/player-react)
[![npm](https://img.shields.io/npm/v/%40livepeer-frameworks%2Fplayer-svelte?label=@livepeer-frameworks%2Fplayer-svelte)](https://www.npmjs.com/package/@livepeer-frameworks/player-svelte)
[![npm](https://img.shields.io/npm/v/%40livepeer-frameworks%2Fplayer-core?label=@livepeer-frameworks%2Fplayer-core)](https://www.npmjs.com/package/@livepeer-frameworks/player-core)

**StreamCrafter (browser ingest)**

[![npm](https://img.shields.io/npm/v/%40livepeer-frameworks%2Fstreamcrafter-react?label=@livepeer-frameworks%2Fstreamcrafter-react)](https://www.npmjs.com/package/@livepeer-frameworks/streamcrafter-react)
[![npm](https://img.shields.io/npm/v/%40livepeer-frameworks%2Fstreamcrafter-svelte?label=@livepeer-frameworks%2Fstreamcrafter-svelte)](https://www.npmjs.com/package/@livepeer-frameworks/streamcrafter-svelte)
[![npm](https://img.shields.io/npm/v/%40livepeer-frameworks%2Fstreamcrafter-core?label=@livepeer-frameworks%2Fstreamcrafter-core)](https://www.npmjs.com/package/@livepeer-frameworks/streamcrafter-core)

## Architecture at a glance

![Microservices Architecture](website_docs/src/assets/diagrams/Microservices_Architecture.png)

- Gateway
  - Bridge (`api_gateway`): GraphQL gateway, aggregates all services
- Control plane (business logic)
  - Commodore (`api_control`): auth, streams, business logic
  - Quartermaster (`api_tenants`): tenants, clusters, nodes
  - Purser (`api_billing`): usage, invoices, payments
- Data plane (events & analytics)
  - Periscope Ingest (`api_analytics_ingest`): consumes Kafka, writes ClickHouse
  - Periscope Query (`api_analytics_query`): serves analytics & usage summaries
  - Decklog (`api_firehose`): gRPC ingress → Kafka
  - Kafka: event backbone
  - PostgreSQL: state & aggregates
  - ClickHouse: time‑series
- Media plane
  - Foghorn (`api_balancing`): regional load balancer & media pipeline orchestrator
  - Helmsman (`api_sidecar`): edge sidecar, MistServer management via Foghorn
  - MistServer: ingest/processing/edge delivery
  - Livepeer Gateway (golivepeer): transcoding/AI processing
- Network & Infrastructure
  - Navigator (`api_dns`): public DNS automation & certificate issuance
  - Privateer (`api_mesh`): WireGuard mesh agent & local DNS
- Realtime
  - Signalman (`api_realtime`): WebSocket hub for live updates
- Interfaces
  - Web Console (`website_application`): main dashboard
  - Marketing Site (`website_marketing`): public site
  - Documentation (`website_docs`): Astro Starlight docs
  - Forms API (`api_forms`): contact forms, newsletter (Listmonk)

Principles
- Strict service boundaries (no cross‑DB reads)
- Time‑serie/data-plane in ClickHouse; control/aggregates in Postgres
- Type safety by reusing the gRPC types straight from the emitter. Passthrough and leave source data intact as much as possible, with optional enrichment fields

## Quick Start

### Development Setup (docker-compose)

For local development and testing:

```bash
git clone https://github.com/Livepeer-FrameWorks/monorepo.git
cd monorepo
cp config/env/secrets.env.example config/env/secrets.env  # edit values as needed
make env  # writes .env from config/env
docker-compose up
```

The Compose stack loads `${ENV_FILE:-.env}` automatically. Override `ENV_FILE` (and pass `--env-file` to docker compose) when you want to use a different generated env file (for example `.env.staging`).

Endpoints (local)
- GraphQL Gateway: http://localhost:18090/graphql
- GraphQL WebSocket: ws://localhost:18090/graphql/ws (subscriptions)
- App via Nginx: http://localhost:18090
- Web Console: http://localhost:18030
- Marketing site: http://localhost:18031
- Listmonk (Admin): http://localhost:9001
- MistServer: http://localhost:4242 (RTMP: 1935, HTTP: 8080)
- Kafka (external): localhost:29092
- Postgres: localhost:5432
- ClickHouse: 8123 (HTTP), 9000 (native)

## Ports

| Plane | Service | Port | Notes |
| --- | --- | --- | --- |
| Gateway | Bridge | 18000 | GraphQL Gateway |
| Control | Commodore | 18001 | Health/Metrics |
| Control | Commodore (gRPC) | 19001 | gRPC API |
| Control | Quartermaster | 18002 | Health/Metrics |
| Control | Quartermaster (gRPC) | 19002 | gRPC API |
| Control | Purser | 18003 | Health/Metrics |
| Control | Purser (gRPC) | 19003 | gRPC API |
| Data | Periscope Query | 18004 | HTTP health/metrics only |
| Data | Periscope Query (gRPC) | 19004 | gRPC API |
| Data | Periscope Ingest | 18005 | Kafka consumer |
| Data | Decklog | 18006 | gRPC |
| Data | Decklog (metrics) | 18026 | Prometheus metrics |
| Data | Kafka (external) | 29092 | Host access |
| Data | Kafka (internal) | 9092 | Cluster access |
| Data | Zookeeper | 2181 | Kafka coordination |
| Data | PostgreSQL | 5432 | Primary database |
| Data | ClickHouse (HTTP) | 8123 | Analytics database |
| Data | ClickHouse (Native) | 9000 | Analytics database |
| Network | Navigator | 18010 | Public DNS management & ACME |
| Network | Navigator (gRPC) | 18011 | gRPC API |
| Network | Privateer | 18012 | WireGuard mesh agent & Local DNS |
| Media | Helmsman | 18007 | Edge API |
| Media | Foghorn | 18008 | Balancer |
| Media | Foghorn (control) | 18019 | gRPC control API |
| Media | MistServer (control) | 4242 | Control API |
| Media | MistServer (RTMP) | 1935 | Ingest |
| Media | MistServer (HTTP) | 8080 | HLS/WebRTC delivery |
| Media | Livepeer Gateway (CLI) | 18016 | golivepeer control (compute gateway; integration WIP; not in dev compose) |
| Media | Livepeer Gateway (RPC/HTTP) | 18017 | golivepeer public API (compute gateway; integration WIP; not in dev compose) |
| Realtime | Signalman | 18009 | WebSocket hub |
| Realtime | Signalman (gRPC) | 19005 | gRPC API |
| Support | Nginx | 18090 | Reverse proxy |
| Support | Prometheus | 9091 | Metrics (CLI deployment only) |
| Support | Grafana | 3000 | Dashboards (CLI deployment only) |
| Support | Metabase | 3001 | BI Analytics (CLI deployment only) |
| Support | Listmonk | 9001 | Newsletter Admin |
| Support | Chatwoot | 18092 | Support dashboard (via Nginx: /support) |
| UI | Web Console | 18030 | Application UI |
| UI | Marketing Site | 18031 | Public site |
| Support | Forms API | 18032 | Contact forms |
| Deferred | Lookout (api_incidents) | 18013 | Incident management (use Prometheus/Grafana instead) |
| Planned | Parlor (api_rooms) | 18014 | Channel rooms for interactive features |
| Support | Deckhand (api_ticketing) | 18015 | Support ticketing |
| Support | Deckhand (gRPC) | 19006 | Support gRPC API |

## Configuration

### GeoIP

Foghorn (api_balancing) can determine geography from either:
- Proxy-injected geo headers (e.g., Cloudflare’s CF-IPCountry or similar), or
- A local MMDB file (any vendor providing a compatible City/Country database).

It is recommended to point it to a local MMDB file, which ensures all events are enriched with Geo data. Only events originating from the Load Balancer can be enriched via geo headers.

To use a local database, set `GEOIP_MMDB_PATH` to the path of your MMDB file. If neither headers nor MMDB are available, Foghorn operates without geo routing data.

## Docs

See `website_docs/` for full documentation (Astro Starlight site):

- **Streamers** — Quick start, encoder setup, API reference, playback
- **Operators** — Architecture, deployment, DNS, CLI reference, WireGuard mesh
- **Hybrid** — Self-hosted edge nodes with managed control plane
- **Roadmap** — Feature status and priorities
- **Blog** — Changelog, technical posts
