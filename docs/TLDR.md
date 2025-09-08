# FrameWorks Architecture TLDR

## Multi-Plane Architecture Overview

FrameWorks is a distributed microservices platform for multi-tenant video streaming, built on clear plane separation and an event pipeline:

- **Control Plane**: Authentication, stream management, tenant/routing (immediate consistency)
- **Data Plane**: Analytics, metrics, and event processing (Kafka-driven)
- **Media Plane**: Media ingest/processing and routing (autonomous)
- **Support & Interfaces**: Web apps, marketing, docs

---

## Complete Service Stack

### Core Services

| Service | Port | Tier | Purpose |
|---------|------|------|---------|
| **Control Plane** | | | |
| Bridge | 18000 | Regional | GraphQL API Gateway (aggregates all services) |
| Commodore | 18001 | Central | Business logic & orchestration API |
| Quartermaster | 18002 | Central | Tenant management API |
| Purser | 18003 | Central | Billing API |
| **Data Plane** | | | |
| Periscope Query | 18004 | Central | Analytics & reporting API |
| Periscope Ingest | 18005 | Central | Kafka event processing |
| Decklog | 18006 | Central | gRPC event ingress → Kafka |
| Decklog (metrics) | 18026 | Central | Prometheus metrics |
| **Media Plane** | | | |
| Foghorn | 18008 | Central | Load balancer |
| Foghorn (control) | 18019 | Central | gRPC control API |
| Helmsman | 18007 | Edge | MistServer sidecar |
| **Support & Interfaces** | | | |
| Signalman | 18009 | Central | Real-time updates & WebSocket hub |
| Web Console | 18030 | Central | Main application interface |
| Marketing Site | 18031 | Central | Public website |
| Forms API | 18032 | Central | Contact form handling |
| **Planned Services** | | | |
| Privateer (api_mesh) | 18012 | Central | WireGuard mesh orchestration |
| Lookout (api_incidents) | 18013 | Central | Incident management |
| Parlor (api_rooms) | 18014 | Central | Interactive room service |
| Deckhand (api_ticketing) | 18015 | Central | Support ticketing |
| **Deferred Services** | | | |
| Seawarden | 18010 | Central | Certificate management (use Cloudflare + Let's Encrypt) |
| Navigator | 18011 | Central | DNS management (use Cloudflare DNS API) |

### Infrastructure Components

| Component | Role | Plane | Port(s) | Deploy Location |
|-----------|------|-------|---------|-----------------|
| MistServer | Media processing (ingest/transcode) | Media | 4242, 8080, 1935 | Edge |
| Livepeer | Transcoding/AI processing | Media | 18016 (CLI), 18017 (RPC/HTTP) | Edge |
| PostgreSQL/YugabyteDB | State & configuration database | Data | 5432/5433 | Central |
| ClickHouse | Time-series analytics database | Data | 8123, 9000 | Central |
| Kafka | Event streaming backbone | Data | 9092, 29092 | Regional |
| Zookeeper | Kafka cluster management | Data | 2181 | Regional |
| Nginx | Reverse proxy & routing | Support | 18090 | Central |
| Prometheus | Metrics collection | Support | 9091 | Central |
| Grafana | Metrics visualization | Support | 3000 | Central |

Note: State/aggregates live in PostgreSQL (YugabyteDB-compatible). Time‑series analytics live in ClickHouse. See [`DATABASE.md`](DATABASE.md).

### Interfaces

| Component | Role | Path | Port | Deploy Location |
|-----------|------|------|------|-----------------|
| Web Console | SvelteKit user dashboard | `website_application` | 18030 | Regional |
| Marketing Site | Sales / Marketing website | `website_marketing` | 18031 | Regional |
| Android App | Mobile broadcaster app | `app_android` | N/A | User device |

---

## Deployment Tiers

- **Central**: Commodore, Quartermaster, Periscope, Purser, PostgreSQL, ClickHouse, Foghorn
- **Regional**: Bridge, Decklog, Kafka, Signalman, Web Console, Marketing Site
- **Edge**: MistServer, Helmsman, Livepeer Gateway

---

## Data Flow Architecture

### Event-Driven Analytics Pipeline
```
MistServer → Helmsman → Decklog → Kafka → Periscope-Ingest → Postgres/ClickHouse
                                         ↘
                                           Signalman → Frontend (WebSocket)
```

### Control Plane Communications
```
Frontend → Commodore (HTTP/REST)
Helmsman → Commodore (HTTP/REST - auth, validation)
Frontend → Purser (HTTP/REST - billing)
```

### Media Pipeline
```
                Livepeer (transcoding/AI)
                     ↕
RTMP/SRT/WHIP → MistServer → HLS/WebRTC/SRT → Viewers
                     ↕
                Foghorn (load balancing)
```

---

## Event Types & Kafka Topic

- **Topic**: `analytics_events`
- **Event types** (hyphenated):
  - `stream-ingest`, `stream-view`, `stream-lifecycle`, `stream-buffer`, `stream-end`
  - `user-connection`, `push-lifecycle`, `recording-lifecycle`, `track-list`, `client-lifecycle`
  - `node-monitoring`, `load-balancing`
- All events carry `tenant_id` in Kafka headers for isolation. See `api_firehose` and `pkg/kafka`.

---

## Authentication & Security

- Control Plane (HTTP): JWT/API tokens, tenant‑scoped
- Data Plane (Kafka): service tokens, tenant headers, VPN
- Media Plane: stream validation via Control Plane

---

## Scaling Characteristics

Each tier has different scaling patterns and bottlenecks, allowing independent optimization based on workload characteristics.

| Tier | Scaling Pattern | Bottlenecks | Solutions |
|------|-----------------|-------------|-----------|
| Central | Vertical first. Horizontally enabled through tenant isolation | Database connections, CPU | Read replicas |
| Regional | Horizontal (stateless) | Kafka throughput | Partition scaling, clustering |
| Edge | Geographic distribution | Network latency, bandwidth | CDN deployment, edge caching |
