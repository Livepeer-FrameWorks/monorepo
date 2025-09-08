# FrameWorks Development Roadmap

This roadmap reflects the current implementation status. It's an honest view of what works now and what's planned.

Status legend:
- Complete — Fully implemented and production-ready
- Partial — Basic implementation exists but not feature-complete
- In progress — Actively being developed
- Not started — Planned but no implementation yet
- Basic only — Exists as stubs or scaffolding

---

## Core Infrastructure

Aim: rock‑solid basics. These are the services and controls everything else depends on.

### User Registration & Authentication
- Status: Complete
- **Implementation**: JWT auth, bot protection, email verification
- **UI Components**: Login/register forms, email verification flow
- Notes: Functional with tenant context

### Multi-Tenant Architecture
- Status: Partial (basic only)
- **Implementation**: 
  - Database schema with tenant isolation
  - Tenant-aware API endpoints
  - Cluster-per-tenant support (DB fields only, no orchestration)
  - Deployment automation (missing)
  - Tenant provisioning (missing)
- **UI Components**: Tenant-aware dashboards work
- **Missing**: Actual deployment orchestration, automated provisioning

### Tenant Management (Quartermaster)
- Status: Partial
- **Implementation**: 
  - Basic CRUD operations
  - Tenant registry API
  - Feature flags (JSON field, no UI)
  - Deployment tiers (DB only, manual)
  - Cluster assignment (basic logic)
  - Domain automation (missing)
- **UI Components**: ❌ **Missing** - No tenant management UI
- Missing: Automated cluster management; domain DNS/SSL automation

### CQRS Analytics (Periscope)
- Status: Complete
- **Implementation**: 
  - Split into Periscope-Ingest and Periscope-Query
  - Kafka event pipeline (functional)
  - PostgreSQL for state management
  - ClickHouse for time-series analytics
  - Materialized views for aggregations
  - TTL and automatic cleanup
- **UI Components**: Real-time analytics dashboard
- Notes: Stable; schema and queries may need tuning.

### Bot Protection
- Status: Complete
- **Implementation**: Honeypot fields, human verification, timing validation
- **UI Components**: Human verification in register form
- Notes: Provides basic protection.

### Stream Management
- Status: Complete
- **Implementation**: Full CRUD operations, stream keys, playback IDs
- **UI Components**: Stream creation/deletion, URL generation
- Notes: Functional.

### Protocol Support
- Status: Complete
- **Implementation**: RTMP, SRT, WHIP ingest → HLS, WebRTC egress
- **UI Components**: Protocol documentation with URLs
- Notes: Configurable via MistServer.

### Cluster Router (Foghorn)
- Status: Partial
- **Implementation**: 
  - Load balancing with capacity awareness
  - Geographic proximity routing
  - Basic health checks
  - Multi-tier support (DB only)
  - Tenant-aware routing (basic)
- **UI Components**: Backend only
- Missing: Advanced orchestration; auto-scaling

### Payment Processing (Purser)
- Status: Partial
- **Implementation**: 
  - Stripe integration (functional)
  - Crypto monitoring (BTC, ETH, USDC, LPT)
  - Mollie integration (stubs only)
  - Usage-based billing automation (missing)
- UI Components: Basic only — UI exists but backend methods return empty data
- Missing: Automated invoicing; production crypto wallets; GetInvoices/GetBillingTiers implementations

---

## Analytics and Monitoring

### Real-time Viewer Counts
- Status: Complete
- **Implementation**: WebSocket updates, ClickHouse aggregations
- **UI Components**: Dashboard widgets with auto-refresh

### Enhanced Client Metrics
- Status: Complete
- **Implementation**: Packet stats, bandwidth, connection quality, geo
- **UI Components**: Technical metrics dashboard

### Geographic Analytics
- Status: Basic only
- **Implementation**: 
  - Data captured in ClickHouse
  - No aggregation queries
  - No backend API
- UI Components: Basic only — UI exists but shows infrastructure nodes, not analytics
- Missing: Aggregation API and visualization

### Performance Metrics
- Status: Complete
- **Implementation**: Bandwidth, latency, packet loss tracking
- **UI Components**: Real-time performance dashboard

### Usage Tracking & Billing
- Status: Basic only
- **Implementation**: 
  - Metrics collected
  - Basic queries exist
  - No billing aggregation
  - No automated invoicing
- UI Components: Basic only — UI exists but no usage-to-billing pipeline
- Missing: Usage-to-billing pipeline; automated invoicing

---

## DevOps and Infrastructure

### Infrastructure as Code
- Status: Not started
- **Required**: 
  - Terraform configurations for cloud providers
  - Ansible playbooks for service deployment
  - Kubernetes manifests for container orchestration
- Missing: All IaC components

### Service Discovery & Orchestration
- Status: Basic only
- **Implementation**: 
  - Manual service configuration
  - No service mesh
  - No automatic service discovery

### Monitoring & Observability
- Status: Partial
- **Implementation**: 
  - Basic health endpoints
  - Prometheus metrics (minimal)

### CI/CD Pipeline
- Status: Basic only
- **Implementation**: 
  - Basic GitHub Actions
  - No automated testing
  - No deployment automation
- **Required**: Full CI/CD with testing, staging, production

---

## Streaming and Distribution

### Multi-format Streaming
- Status: Complete
- **Implementation**: All protocols via MistServer

### Drop-in AV Device Discovery
- Status: Basic only
- **Implementation**: Capabilities exists but no deployment pipeline
- Missing: Integration; remote management

### Multi-stream Compositing
- Status: Basic only
- **Implementation**: MistServer supports but no orchestration
- Missing: Stream bonding; metering; UI

### Transcoding
- Status: Basic only (planned)
- **Implementation**: Livepeer integration planned
- Missing: Integration and operations work

### Multi-platform Restreaming
- Status: Not started
- Notes: Considering partnership options

### Custom Domains
- Status: Basic only
- **Implementation**: 
  - Database fields
  - No DNS automation
  - No SSL automation
- Missing: api_dnsmgr and api_certmgr services

---

## Content Management

### Live Recording
- Status: Basic only
- **Implementation**: MistServer capable but no infrastructure
- Missing: Storage nodes; API; metering

### VOD Management
- Status: Not started
- Missing: Entire VOD infrastructure

### Live Clipping
- Status: Basic only
- **Implementation**: MistServer capable
- Missing: Storage; API; UI

### Storage Management
- Status: Not started
- Missing: Storage service; quotas; management

---

## Team and Account Features

### Team Collaboration
- Status: Not started
- Missing: Data model; API; UI

### Billing System
- Status: Basic only
- **Implementation**: 
  - Payment processing
  - Usage-based billing (missing)
  - Invoice generation (missing)
- UI Components: Missing

### API Access Management
- Status: Basic only
- **Implementation**: Tokens work, basic management
- UI Components: Basic only — UI exists at `/developer/api` but management functions are incomplete

### Prepaid Credits
- Status: Not started

---

## Developer and Integration Features

### REST API
- Status: Complete
- **Implementation**: Functional for existing features

### Webhooks
- Status: Partial
- Implementation: MistServer webhooks only
- Missing: Customer-facing webhooks

### NPM Packages
- Status: Not started

### Calendar Integration
- Status: Not started

### Custom Integrations
- Status: Not started

---

## Advanced and Enterprise Features

### AI Processing
- Status: Not started
- Notes: Experimentation on edge nodes; no production infrastructure

---

## Mobile and Native Apps

### Android App
- Status: Basic only
- Implementation: Basic scoping only

### iOS App
- Status: Not started
