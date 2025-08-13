# FrameWorks Development Roadmap

This roadmap reflects the current implementation status. It's an honest view of what works now and what's planned.

**Legend:**
- ✅ **Complete** - Fully implemented and production-ready
- 🔄 **Partial** - Basic implementation exists but not feature-complete
- 🚧 **In Progress** - Actively being developed
- ❌ **Not Started** - Planned but no implementation yet
- 🔍 **Surface Level** - Exists but only as stubs or basic scaffolding

---

## 🎬 Core Infrastructure

Aim: rock‑solid basics. These are the services and controls everything else depends on.

### User Registration & Authentication
- **Status**: ✅ **Complete**
- **Implementation**: JWT auth, bot protection, email verification
- **UI Components**: Login/register forms, email verification flow
- **Notes**: Fully functional with proper tenant context

### Multi-Tenant Architecture
- **Status**: 🔄 **Partial (Surface Level)**
- **Implementation**: 
  - ✅ Database schema with tenant isolation
  - ✅ Tenant-aware API endpoints
  - 🔍 Cluster-per-tenant support (DB fields only, no orchestration)
  - ❌ Deployment automation
  - ❌ Tenant provisioning
- **UI Components**: Tenant-aware dashboards work
- **Missing**: Actual deployment orchestration, automated provisioning

### Tenant Management (Quartermaster)
- **Status**: 🔄 **Partial**
- **Implementation**: 
  - ✅ Basic CRUD operations
  - ✅ Tenant registry API
  - 🔍 Feature flags (JSON field, no UI)
  - 🔍 Deployment tiers (DB only, manual)
  - 🔍 Cluster assignment (basic logic)
  - ❌ Domain automation
- **UI Components**: ❌ **Missing** - No tenant management UI
- **Missing**: Automated cluster management, domain DNS/SSL automation

### CQRS Analytics (Periscope)
- **Status**: ✅ **Complete**
- **Implementation**: 
  - ✅ Split into Periscope-Ingest and Periscope-Query
  - ✅ Kafka event pipeline (fully functional)
  - ✅ PostgreSQL for state management
  - ✅ ClickHouse for time-series analytics
  - ✅ Materialized views for aggregations
  - ✅ TTL and automatic cleanup
- **UI Components**: Real-time analytics dashboard
- **Notes**: Well-implemented. DB schema's and queries can probably use some tweaking though.

### Bot Protection
- **Status**: ✅ **Complete**
- **Implementation**: Honeypot fields, human verification, timing validation
- **UI Components**: Human verification in register form
- **Notes**: Works good enough for basic protection

### Stream Management
- **Status**: ✅ **Complete**
- **Implementation**: Full CRUD operations, stream keys, playback IDs
- **UI Components**: Stream creation/deletion, URL generation
- **Notes**: Basic but fully functional

### Protocol Support
- **Status**: ✅ **Complete**
- **Implementation**: RTMP, SRT, WHIP ingest → HLS, WebRTC egress
- **UI Components**: Protocol documentation with URLs
- **Notes**: MistServer handles this well, we can enable anything we need as we go.

### Cluster Router (Foghorn)
- **Status**: 🔄 **Partial**
- **Implementation**: 
  - ✅ Load balancing with capacity awareness
  - ✅ Geographic proximity routing
  - ✅ Basic health checks
  - 🔍 Multi-tier support (DB only)
  - 🔍 Tenant-aware routing (basic)
- **UI Components**: Backend only
- **Missing**: Advanced orchestration, auto-scaling

### Payment Processing (Purser)
- **Status**: 🔄 **Partial**
- **Implementation**: 
  - ✅ Stripe integration (functional)
  - 🔄 Crypto monitoring (BTC, ETH, USDC, LPT)
  - 🔍 Mollie integration (stubs only)
  - ❌ Usage-based billing automation
- **UI Components**: ❌ **Missing** - No billing UI
- **Missing**: Automated invoicing, production crypto wallets

---

## 📊 Analytics & Monitoring

### Real-time Viewer Counts
- **Status**: ✅ **Complete**
- **Implementation**: WebSocket updates, ClickHouse aggregations
- **UI Components**: Dashboard widgets with auto-refresh

### Enhanced Client Metrics
- **Status**: ✅ **Complete**
- **Implementation**: Packet stats, bandwidth, connection quality, geo
- **UI Components**: Technical metrics dashboard

### Geographic Analytics
- **Status**: 🔍 **Surface Level**
- **Implementation**: 
  - ✅ Data captured in ClickHouse
  - ❌ No aggregation queries
  - ❌ No backend API
- **UI Components**: ❌ Mock data only
- **Missing**: Actual geographic API and visualization

### Performance Metrics
- **Status**: ✅ **Complete**
- **Implementation**: Bandwidth, latency, packet loss tracking
- **UI Components**: Real-time performance dashboard

### Usage Tracking & Billing
- **Status**: 🔍 **Surface Level**
- **Implementation**: 
  - ✅ Metrics collected
  - 🔍 Basic queries exist
  - ❌ No billing aggregation
  - ❌ No automated invoicing
- **UI Components**: ❌ **Missing** - No usage billing UI
- **Missing**: Usage-to-billing pipeline

---

## 🚀 DevOps & Infrastructure

### Infrastructure as Code
- **Status**: ❌ **Not Started**
- **Required**: 
  - Terraform configurations for cloud providers
  - Ansible playbooks for service deployment
  - Kubernetes manifests for container orchestration
- **Missing**: All IaC components

### Service Discovery & Orchestration
- **Status**: 🔍 **Surface Level**
- **Implementation**: 
  - 🔍 Manual service configuration
  - ❌ No service mesh
  - ❌ No automatic service discovery

### Monitoring & Observability
- **Status**: 🔄 **Partial**
- **Implementation**: 
  - ✅ Basic health endpoints
  - 🔍 Prometheus metrics (minimal)

### CI/CD Pipeline
- **Status**: 🔍 **Surface Level**
- **Implementation**: 
  - 🔍 Basic GitHub Actions
  - ❌ No automated testing
  - ❌ No deployment automation
- **Required**: Full CI/CD with testing, staging, production

---

## 🌐 Streaming & Distribution

### Multi-format Streaming
- **Status**: ✅ **Complete**
- **Implementation**: All protocols via MistServer

### Drop-in AV Device Discovery
- **Status**: 🔍 **Surface Level**
- **Implementation**: Capabilities exists but no deployment pipeline
- **Missing**: Integration, remote management

### Multi-stream Compositing
- **Status**: 🔍 **Surface Level**
- **Implementation**: MistServer supports but no orchestration
- **Missing**: Stream bonding, metering, UI

### Transcoding
- **Status**: 🔍 **Surface Level**
- **Implementation**: Livepeer integration planned
- **Missing**: Actual integration, DevOps work

### Multi-platform Restreaming
- **Status**: ❌ **Not Started**
- **Notes**: Considering Restream partnership

### Custom Domains
- **Status**: 🔍 **Surface Level**
- **Implementation**: 
  - ✅ Database fields
  - ❌ No DNS automation
  - ❌ No SSL automation
- **Missing**: api_dnsmgr, api_certmgr services

---

## 🎥 Content Management

### Live Recording
- **Status**: 🔍 **Surface Level**
- **Implementation**: MistServer capable but no infrastructure
- **Missing**: Storage nodes, API, metering

### VOD Management
- **Status**: ❌ **Not Started**
- **Missing**: Entire VOD infrastructure

### Live Clipping
- **Status**: 🔍 **Surface Level**
- **Implementation**: MistServer capable
- **Missing**: Storage, API, UI

### Storage Management
- **Status**: ❌ **Not Started**
- **Missing**: Storage service, quotas, management

---

## 👥 Team & Account Features

### Team Collaboration
- **Status**: ❌ **Not Started**
- **Missing**: Data model, API, UI

### Billing System
- **Status**: 🔍 **Surface Level**
- **Implementation**: 
  - ✅ Payment processing
  - ❌ Usage-based billing
  - ❌ Invoice generation
- **UI Components**: ❌ **Missing**

### API Access Management
- **Status**: 🔍 **Surface Level**
- **Implementation**: Tokens work, no management
- **UI Components**: ❌ **Missing**

### Prepaid Credits
- **Status**: ❌ **Not Started**

---

## 🔧 Developer & Integration Features

### REST API
- **Status**: ✅ **Complete**
- **Implementation**: Functional for existing features

### Webhooks
- **Status**: 🔄 **Partial**
- **Implementation**: MistServer webhooks only
- **Missing**: Customer-facing webhooks

### NPM Packages
- **Status**: ❌ **Not Started**

### Calendar Integration
- **Status**: ❌ **Not Started**

### Custom Integrations
- **Status**: ❌ **Not Started**

---

## 🚀 Advanced & Enterprise Features

### AI Processing
- **Status**: ❌ **Not Started**
- **Notes**: Testing on edge nodes but no infrastructure

---

## 📱 Mobile & Native Apps

### Android App
- **Status**: 🔍 **Surface Level**
- **Implementation**: Basic scoping only

### iOS App
- **Status**: ❌ **Not Started**
