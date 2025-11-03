# FrameWorks

> Warning: This stack is pre‑release and experimental. Do not deploy to production. Interfaces and schemas change frequently. Use for local development and evaluation only.

An open streaming stack for live video: apps, real‑time APIs, and analytics. Services are narrowly scoped. Frontend uses GraphQL; service-to-service uses HTTP/gRPC APIs; analytics and realtime use Kafka events. Each service owns its data (no cross‑DB access).

## Architecture at a glance

- Control plane (business logic)
  - Commodore (`api_control`): auth, streams, API surface
  - Quartermaster (`api_tenants`): tenants, clusters, routing
  - Purser (`api_billing`): usage, invoices, payments
  - Foghorn (`api_balancing`): media‑aware load balancing
- Data plane (events & analytics)
  - Periscope Ingest (`api_analytics_ingest`): consumes Kafka, writes ClickHouse
  - Periscope Query (`api_analytics_query`): serves analytics & usage summaries
  - Decklog (`api_firehose`): gRPC ingress → Kafka
  - Kafka: event backbone
  - PostgreSQL: state & aggregates
  - ClickHouse: time‑series
- Media plane (edge)
  - Helmsman (`api_sidecar`): MistServer integration, metrics, event emission
  - MistServer: ingest/processing/edge
  - Livepeer Gateway (golivepeer): transcoding/AI processing
- Realtime & UI
  - Bridge (`api_gateway`): GraphQL gateway, aggregates all services
  - Signalman (`api_realtime`): WebSocket hub
  - Web Console (`website_application`)

Principles
- Strict service boundaries (no cross‑DB reads)
- Shared types in `pkg/models`
- Time‑series in ClickHouse; control/aggregates in Postgres

## Quick Start

### Edge Node Deployment (CLI)

For deploying edge streaming nodes, use the FrameWorks CLI:

```bash
# Install CLI
curl -L https://github.com/frameworks/cli/releases/latest/download/frameworks -o frameworks
chmod +x frameworks
sudo mv frameworks /usr/local/bin/

# Deploy edge node
frameworks edge bootstrap --domain stream.example.com --token YOUR_TOKEN
frameworks edge up
```

See [CLI documentation](./cli/) for details.

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

Prefer the CLI? You can generate env files with:

```bash
frameworks config env generate --context dev --output .env
```

Endpoints (local)
- GraphQL Gateway: http://localhost:18090/graphql
- GraphQL WebSocket: ws://localhost:18090/graphql/ws (subscriptions)
- App via Nginx: http://localhost:18090
- Web Console: http://localhost:18030
- Marketing site: http://localhost:18031
- Grafana: http://localhost:3000 (admin/frameworks_dev)
- Prometheus: http://localhost:9091
- MistServer: http://localhost:4242 (RTMP: 1935, HTTP: 8080)
- Kafka (external): localhost:29092
- Postgres: localhost:5432
- ClickHouse: 8123 (HTTP), 9000 (native)

## Ports

| Plane | Service | Port | Notes |
| --- | --- | --- | --- |
| Regional | Bridge | 18000 | GraphQL Gateway |
| Control | Commodore | 18001 | API |
| Control | Quartermaster | 18002 | API |
| Control | Purser | 18003 | API |
| Data | Periscope Query | 18004 | API |
| Data | Periscope Ingest | 18005 | Kafka consumer |
| Data | Decklog | 18006 | gRPC |
| Data | Decklog (metrics) | 18026 | Prometheus metrics |
| Data | Kafka (external) | 29092 | Host access |
| Data | Kafka (internal) | 9092 | Cluster access |
| Data | Zookeeper | 2181 | Kafka coordination |
| Data | PostgreSQL | 5432 | Primary database |
| Data | ClickHouse (HTTP) | 8123 | Analytics database |
| Data | ClickHouse (Native) | 9000 | Analytics database |
| Media | Helmsman | 18007 | Edge API |
| Media | Foghorn | 18008 | Balancer |
| Media | Foghorn (control) | 18019 | gRPC control API |
| Media | MistServer (control) | 4242 | Control API |
| Media | MistServer (RTMP) | 1935 | Ingest |
| Media | MistServer (HTTP) | 8080 | HLS/WebRTC delivery |
| Media | Livepeer Gateway (CLI) | 18016 | golivepeer control (compute gateway; integration WIP; not in dev compose) |
| Media | Livepeer Gateway (RPC/HTTP) | 18017 | golivepeer public API (compute gateway; integration WIP; not in dev compose) |
| Realtime | Signalman | 18009 | WebSocket hub |
| Support | Nginx | 18090 | Reverse proxy |
| Support | Prometheus | 9091 | Metrics |
| Support | Grafana | 3000 | Dashboards |
| UI | Web Console | 18030 | Application UI |
| UI | Marketing Site | 18031 | Public site |
| Support | Forms API | 18032 | Contact forms (not in dev compose) |
| Deferred | Seawarden | 18010 | Certificate management (use nginx/caddy instead) |
| Deferred | Navigator | 18011 | DNS management (manual for now, future MVP in Quartermaster) |
| Deferred | Lookout (api_incidents) | 18013 | Incident management (use Prometheus/Grafana instead) |
| Planned | Privateer (api_mesh) | 18012 | WireGuard mesh orchestration |
| Planned | Parlor (api_rooms) | 18014 | Channel rooms for interactive features |
| Planned | Deckhand (api_ticketing) | 18015 | Support ticketing |

## Configuration

### GeoIP

Foghorn (api_balancing) can determine geography from either:
- Proxy-injected geo headers (e.g., Cloudflare’s CF-IPCountry or similar), or
- A local MMDB file (any vendor providing a compatible City/Country database).

It is recommended to point it to a local MMDB file, which ensures all events are enriched with Geo data. Only events originating from the Load Balancer can be enriched via geo headers.

To use a local database, set `GEOIP_MMDB_PATH` to the path of your MMDB file. If neither headers nor MMDB are available, Foghorn operates without geo routing data.

## Docs
- [Database](docs/DATABASE.md) — Dual-database design: PostgreSQL/YugabyteDB for state/aggregates, ClickHouse for time‑series; schemas, materialized views, TTLs, and service touch‑points.
- [Architecture TL;DR](docs/TLDR.md) — High‑level map of planes, services, infra components, deployment tiers, data flows, Kafka topics, and local dev quickstart.
- [Implementation](docs/IMPLEMENTATION.md) — Technical deep‑dive: service responsibilities, APIs, shared models, validation, Kafka topics/headers, event types/flow, DB usage, and security.
- [Infrastructure](docs/INFRASTRUCTURE.md) — Infra approach today (Terraform + Ansible + Quartermaster) and the future path (Kubernetes/GitOps), with tier roles and evolution.
- [Provisioning](docs/provisioning/) — Deployment methods, prerequisites, networking (WireGuard), SSL, DNS, and production notes.
- [Roadmap](docs/ROADMAP.md) — Honest feature status: what ships today vs. planned; gaps and priorities across analytics, billing, UI, and media.
