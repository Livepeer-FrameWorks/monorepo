# FrameWorks Implementation Guide

This document provides comprehensive technical details about FrameWorks architecture, implementation, and integration patterns.

## 🏛️ Architecture Overview

FrameWorks is a **distributed microservices platform** for **multi-tenant video streaming SaaS**, built on **event-driven architecture** with **multi-plane separation** of concerns.

### Architecture Patterns
- **Microservices**: Independent, containerized services with API boundaries
- **Multi-tenant SaaS**: Tenant isolation with hybrid deployment support
- **Event-driven**: Asynchronous processing via Kafka event streaming
- **Multi-plane separation**: Control, Data, Media, and Support planes with distinct responsibilities
- **Edge-distributed**: Regional deployment with cluster-aware routing
- **CQRS**: Command Query Responsibility Segregation for analytics

## Multi-Plane Architecture

### 1. Control Plane
**Purpose**: Business logic, orchestration, and immediate consistency operations

**Services**:
- **Bridge** (`api_gateway`): GraphQL API Gateway - unified service aggregation
  - Single GraphQL endpoint aggregating all FrameWorks services
  - Client authentication and request routing
  - Query optimization and response caching
  - Real-time subscriptions via GraphQL websockets
- **Commodore** (`api_control`): Core business logic and tenant routing
  - User authentication and account management
  - Stream metadata and configuration
  - Tenant-to-cluster routing
  - API access control
- **Quartermaster** (`api_tenants`): Tenant management and isolation
  - Tenant provisioning
  - Cluster assignment
  - Resource allocation
  - Multi-tier deployment
- **Purser** (`api_billing`): Billing and subscription management
  - Plan management
  - Usage-based billing
  - Payment processing (including crypto)
  - Invoice generation

**Planned Services** 🚧:
- **Privateer** (`api_mesh`): WireGuard mesh orchestration agent
  - Token-based secure mesh joining
  - Bootstrap peer architecture for initial connections
  - Integration with Quartermaster node registry
  - Zero-downtime configuration updates and peer discovery

**Deferred Services** ⏸️:
- **Seawarden** (`api_certmgr`): Certificate management
  - **Deferred because**: Let's Encrypt + Certbot already handles SSL automation effectively, exploring handling this with Caddy or Terraform+Ansible
  - **Current solution**: Custom Nginx build with automatic certificate renewal
- **Navigator** (`api_dnsmgr`): DNS management
  - **Deferred because**: Cloudflare DNS API provides all necessary functionality
  - **Current solution**: Manual DNS configuration via Cloudflare dashboard, exploring handling this with Caddy or Terraform+Ansible

**Characteristics**:
- Lower volume, high-value operations
- Critical commands requiring immediate consistency
- **Direct HTTP communication** for immediate state propagation
- Multi-tenant and multi-cluster aware

### 2. Data Plane
**Purpose**: Analytics, metrics collection, and eventual consistency processing

**Write Path**:
- **Decklog** (`api_firehose`): Regional event ingest service
  - Batches events from edge nodes and other services
  - Adds tenant/cluster context to Kafka headers
  - Validates event schemas
  - Routes to appropriate Kafka topics
- **Periscope-Ingest** (`api_analytics_ingest`): Analytics write service
  - Consumes from Kafka topics
  - Writes time-series data to ClickHouse
  - Reduces stream state to PostgreSQL (`stream_analytics`) when `DATABASE_URL` is configured
  - Handles tenant data isolation
  - Processes metrics and aggregations

**Read Path**:
- **Periscope-Query** (`api_analytics_query`): Analytics read service
  - Serves HTTP APIs for analytics
  - Tenant-scoped queries
  - Historical data access
  - Aggregated metrics
  - Update Purser with usage logs
  - Queries YugabyteDB for state
  - Queries ClickHouse for time-series
- **Signalman** (`api_realtime`): Real-time WebSocket updates
  - Tenant-aware connections
  - Live metrics and status
  - Dashboard updates

**Infrastructure**:
- **Kafka**: Event streaming backbone
  - Topic-based message routing
  - Event persistence
  - Stream processing
- **PostgreSQL/YugabyteDB**: State & configuration database
  - Multi-tenant data storage
  - Stream configuration
  - User accounts
  - Billing records
  - Current state
- **ClickHouse**: Time-series analytics database
  - High-performance analytics
  - Time-series metrics
  - Viewer analytics
  - Node health metrics
  - Routing decisions
  - Stream health metrics

**Characteristics**:
- High-volume, analytical workloads
- **Event-driven via Kafka** for scalability
- Tenant isolation via Kafka headers
- Regional deployment for minimal latency

### 3. Media Plane
**Purpose**: Media processing, routing, and content delivery

**Services**:
- **Helmsman** (`api_sidecar`): Edge sidecar service
  - Stream validation via Commodore
  - Event forwarding to Decklog
  - Enhanced client metrics collection
  - Node health monitoring
- **MistServer**: Media processing nodes
  - Multi-protocol ingest (RTMP, SRT, WHIP)
  - Multi-format delivery (HLS, DASH, WebRTC)
  - Stream processing and transcoding
- **Foghorn** (`api_balancing`): Load balancer for media requests
  - Stream routing
  - Load distribution
  - Health checks
  - Geographic routing
- **Livepeer Gateway** 
  - GPU transcoding and AI jobs via Livepeer network
  - Deployed on edge nodes alongside MistServer
  - Simple deployment: Docker/binary, L2 RPC access, funded ETH wallet
  - Keys require activation and deposit management

**Characteristics**:
- **Direct media protocols** for low-latency streaming
- **Autonomous routing decisions** via Foghorn
- Self-discovering topology
- **Independent operation** from Control/Data planes

### 4. Support Services & Interface
**Purpose**: User interfaces and supporting functionality

**Services**:
- **Web Console** (`website_application`): Main application interface
  - Stream management
  - Analytics dashboard
  - Account settings
  - Billing interface
- **Marketing Site** (`website_marketing`): Public website
  - Product information
  - Documentation
  - Pricing
  - Contact forms
- **Forms API**: Contact form handling
  - Email notifications
  - Spam protection
  - Lead tracking

**Infrastructure**:
- **Discourse**: Community forum
  - Self-hosted forum platform
  - SSO integration
  - Custom theme
  - API integration
- **Prometheus**: Metrics collection
  - Service metrics
  - Node monitoring
  - Alert rules
  - PromQL queries
- **Grafana**: Metrics visualization
  - Custom dashboards
  - Real-time monitoring
  - Alert management
  - Multi-datasource support

**Planned Services** 🚧:
- **Lookout** (`api_incidents`): Incident management
  - Alert aggregation from monitoring systems
  - Smart deduplication and incident creation
  - Escalation policies and multi-channel notifications
  - Public status page integration
- **Parlor** (`api_rooms`): Interactive room service
  - Room hierarchy with persistent communities
  - Multi-modal interaction (chat, voice, video, games)
  - Channel credit economy and prediction markets
  - Zoom-like group calls with broadcasting modes
- **Deckhand** (`api_ticketing`): Support ticketing
  - Stream-aware ticket creation with diagnostics
  - Multi-channel intake (email, web, chat, API)
  - SLA management and agent routing
  - Knowledge base integration

**Infrastructure**:
- **Nginx**: Reverse proxy and routing
  - SSL termination
  - Request routing
  - Load balancing
  - Geographic routing

## Multi-Tenant Architecture

### Deployment Models
- **Global Shared**: Default tier, shared infrastructure
- **Premium Shared**: Enhanced resources, still shared
- **Self-Hosted Edge**: Customer's own edge nodes
- **Enterprise Managed**: Dedicated infrastructure

### Tenant Isolation
- **Control Plane**: 
  - Tenant-scoped database queries
  - JWT/API token tenant context
  - Role-based access control
- **Data Plane**:
  - Tenant ID in Kafka headers
  - Isolated analytics storage
  - Tenant-aware WebSocket connections
- **Media Plane**:
  - Tenant-specific edge nodes
  - Isolated stream processing
  - Dedicated resources (optional)

### Cluster Router
- **Purpose**: Route tenants to appropriate clusters
- **Capabilities**:
  - Capacity-aware routing (streams, viewers, bandwidth)
  - Health-based failover
  - Multi-tier support
  - Hybrid deployment handling

## Event Pipeline

### Kafka Topic
- `analytics_events`: unified analytics and routing events

### Event Headers
- `tenant_id`: Tenant identifier (Kafka header)
- `event_type`: Event classification
- `schema_version`: Event format version

### Event Types
1. **Stream Events**:
   - `stream-ingest`: New stream started
   - `stream-view`: Viewer connected
   - `stream-lifecycle`: Stream state changes

2. **User Events**:
   - `user-connection`: Viewer connection lifecycle
   - `push-lifecycle`: Push connection lifecycle
   - `recording-lifecycle`: Recording state changes

3. **System Events**:
   - `node-lifecycle`: Node health and metrics
   - `load-balancing`: Load balancer decisions

### Event Flow
1. **Edge Events**: Helmsman → Decklog (batched)
2. **Event Processing**: Decklog → Kafka (with headers)
3. **Analytics Write**: Kafka → Periscope-Ingest → Postgres/ClickHouse (`DATABASE_URL` required for Postgres state)
4. **Real-time Updates**: Kafka → Signalman → WebSocket

### Diagram

```
┌─────────────────┐    Stream Key     ┌─────────────────┐
│   OBS Studio    │ ────────────────> │   MistServer    │
│   (RTMP Push)   │   sk_abc123...    │  (PUSH_REWRITE) │
└─────────────────┘                   └─────────────────┘
                                              ▲
                                              │ Stream rewrite (live+{stream_uuid})
                                              ▼ 
                                    ┌─────────────────┐
                                    │    Helmsman     │
                                    │ (Validation &   │
                                    │ Event Routing)  │
                                    └─────────────────┘
                                      ▲           │
                                      │           │
                                      ▼ (HTTP)    ▼ (Batched HTTP)
                               ┌─────────────┐   ┌─────────-─────────┐
                               │ Commodore   │   │ Regional Firehose │
                               │ (Ctrl API)  │   │    (Decklog)     │
                               └─────────────┘   └─────────────-─────┘
                                      ▲                   │
                                      │                   ▼ (Kafka Producer)
                               Stream rewrite           ┌──────────────────┐
                               (live+{stream_uuid})     │  Kafka Topics    │
                                      │                 │ • stream-analytics│
                                      ▼                 │ • user-connections│
┌─────────────────┐   Playback ID    ┌─────────────────┐│ • realtime-ui     │
│    Viewer       │ -──────────────> │   MistServer    │└──────────────────┘
│   (HLS/...)     │   abc123def...   │ (DEFAULT_STREAM)│         │
└─────────────────┘                  └─────────────────┘         ▼ (Consumers)
                                              ▲         ┌──────────────────┐
                                              │         │  ┌─────────────┐ │
                                              ▼         │  │ Periscope   │ │
                                    ┌─────────────────┐ │  │(Analytics)  │ │
                                    │    Foghorn      │ │  └─────────────┘ │
                                    │(Load Balancer)  │ │  ┌─────────────┐ │
                                    └─────────────────┘ │  │ Signalman   │ │
                                              │        │  │(Real-time UI)│ │
                                              ▼        │  └─────────────┘ │
                                    ┌─────────────────┐└──────────────────┘
                                    │    Decklog      │         │
                                    │(Event Ingest)   │         ▼ WebSocket
                                    └─────────────────┘┌──────────────────┐
                                              │       │  Live Dashboard  │
                                              ▼       │     Updates      │
                                    ┌─────────────────┐└──────────────────┘
                                    │ Routing Events  │
                                    │    (Kafka)      │
                                    └─────────────────┘
```

---

## Database Architecture

### Control Plane Schema (YugabyteDB)
- Tenant-aware tables with `tenant_id`
- Role-based access control
- API token management
- Stream configuration
- User accounts
- Billing records

### Analytics Schema (ClickHouse)
- Time-series optimized tables
- Materialized views for aggregations
- Automatic data TTL
- Tenant isolation
- Efficient time-series queries
- Geographic analysis

### Deployment Options
- **Shared Database**: Default for most tenants
- **Dedicated Database**: Enterprise option
- **Analytics Isolation**: Per-cluster analytics storage

## Security Implementation

### Authentication
- JWT-based user authentication
- API tokens for service access
- Tenant context in all tokens
- Bot protection system

### Authorization
- Role-based access control
- Tenant-scoped permissions
- Service-to-service auth
- Edge node validation

# Implementation Details

## Event Flow

### Event Ingestion (Decklog)
- **Protocol**: gRPC with streaming support
- **Security**: TLS encryption using Let's Encrypt certificates
- **Development**: Optional insecure mode for local development

#### Event Types
1. Stream Ingest Events
   - Published by Helmsman when streams start
   - Contains stream key, protocol, ingest URL
   - Required for analytics tracking

2. Stream View Events
   - Tracks viewer connections
   - Includes viewer IP, user agent
   - Used for viewer analytics

3. Stream Lifecycle Events
   - Tracks stream state changes (started, ended, live, offline)
   - Used for stream status and duration tracking

4. User Connection Events
   - Tracks viewer connect/disconnect events
   - Used for concurrent viewer counting

5. Push Lifecycle Events
   - Tracks RTMP push status
   - Used for push target analytics

6. Recording Lifecycle Events
   - Tracks recording status
   - Used for storage analytics

7. Stream Metrics Events
   - Basic stream health metrics
   - Bandwidth, viewer count

8. Stream Metrics Detailed Events
   - Detailed stream performance metrics
   - CPU, memory, packet loss, etc.

9. Node Monitoring Events
   - Node health metrics
   - Resource utilization tracking

10. Load Balancing Events
    - Foghorn's balancing decisions
    - Used for routing analytics
