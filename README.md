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

## Quick start (docker-compose)

```bash
git clone https://github.com/Livepeer-FrameWorks/monorepo.git
cd monorepo
docker-compose up
```

Endpoints (local)
- GraphQL Gateway: http://localhost:18090/api/gateway
- GraphQL WebSocket: ws://localhost:18090/api/gateway (subscriptions)
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
| Media | MistServer (control) | 4242 | Control API |
| Media | MistServer (RTMP) | 1935 | Ingest |
| Media | MistServer (HTTP) | 8080 | HLS/WebRTC delivery |
| Media | Livepeer Gateway (CLI) | 18016 | golivepeer control |
| Media | Livepeer Gateway (RPC/HTTP) | 18017 | golivepeer public API |
| Realtime | Signalman | 18009 | WebSocket hub |
| Support | Nginx | 18090 | Reverse proxy |
| Support | Prometheus | 9091 | Metrics |
| Support | Grafana | 3000 | Dashboards |
| UI | Web Console | 18030 | Application UI |
| UI | Marketing Site | 18031 | Public site |
| Support | Forms API | 18032 | Contact forms |
| Deferred | Seawarden | 18010 | Certificate management (use Cloudflare + Let's Encrypt) |
| Deferred | Navigator | 18011 | DNS management (use Cloudflare DNS API from Quartermaster) |
| Planned | Privateer (api_mesh) | 18012 | WireGuard mesh orchestration |
| Planned | Lookout (api_incidents) | 18013 | Incident management |
| Planned | Parlor (api_rooms) | 18014 | Channel rooms for interactive features |
| Planned | Deckhand (api_ticketing) | 18015 | Support ticketing |

## Docs
- [Database](docs/DATABASE.md) — Dual-database design: PostgreSQL/YugabyteDB for state/aggregates, ClickHouse for time‑series; schemas, materialized views, TTLs, and service touch‑points.
- [Architecture TL;DR](docs/TLDR.md) — High‑level map of planes, services, infra components, deployment tiers, data flows, Kafka topics, and local dev quickstart.
- [Implementation](docs/IMPLEMENTATION.md) — Technical deep‑dive: service responsibilities, APIs, shared models, validation, Kafka topics/headers, event types/flow, DB usage, and security.
- [Infrastructure](docs/INFRASTRUCTURE.md) — Infra approach today (Terraform + Ansible + Quartermaster) and the future path (Kubernetes/GitOps), with tier roles and evolution.
- [Provisioning](docs/PROVISIONING.md) — Step‑by‑step production deployment: domains/DNS, WireGuard mesh, DB/Kafka clusters, custom Nginx/SSL, monitoring stack, systemd units, and hardening.
- [Roadmap](docs/ROADMAP.md) — Honest feature status: what ships today vs. planned; gaps and priorities across analytics, billing, UI, and media.
