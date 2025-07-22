# FrameWorks Development Roadmap

This roadmap reflects the current implementation status. It’s an honest view of what works now and what’s planned.

---

## 🎬 Core Infrastructure

Aim: rock‑solid basics. These are the services and controls everything else depends on.

### User Registration & Authentication
- **Status**: ✅ **Complete**
- **Implementation**: JWT auth, bot protection (honeypots, behavioral analysis), email verification
- **UI Components**: Login/register forms, email verification flow
- **Marketing Promise**: "Secure user accounts with enterprise-grade authentication"

### Multi-Tenant Architecture
- **Status**: ✅ **Complete**
- **Implementation**: Tenant isolation across all services via Quartermaster, cluster-per-tenant support, hybrid deployments, tenant registry API
- **UI Components**: Tenant-aware dashboards, isolated analytics
- **Marketing Promise**: "Complete tenant isolation with flexible deployment models"

### Tenant Management (Quartermaster)
- **Status**: ✅ **Complete**
- **Implementation**: Centralized tenant registry, feature flags, deployment tiers, cluster assignment, domain management
- **UI Components**: ❌ **Missing** - No tenant management UI in webapp
- **Marketing Promise**: "Flexible tenant management with hybrid deployment support"

### CQRS Analytics (Periscope)
- **Status**: ✅ **Complete**
- **Implementation**: 
  - Split into Periscope-Ingest (write) and Periscope-Query (read)
  - Kafka event pipeline
  - YugabyteDB for state and configuration
  - ClickHouse for time-series analytics
  - Materialized views for real-time aggregations
  - Automatic data TTL and cleanup
- **UI Components**: Real-time analytics dashboard with live metrics
- **Marketing Promise**: "Real-time analytics and monitoring with WebSocket-powered dashboards"

### Bot Protection
- **Status**: ✅ **Complete**
- **Implementation**: Honeypot fields, human verification, timing validation, rate limiting
- **UI Components**: Human verification toggles, behavioral tracking
- **Marketing Promise**: "Advanced bot protection for secure registrations"

### Stream Management
- **Status**: ✅ **Complete**
- **Implementation**: Full CRUD operations, stream keys, playback IDs, real-time status
- **UI Components**: Stream creation/deletion, URL generation, live metrics
- **Marketing Promise**: "Complete stream lifecycle management"

### Protocol Support (All Formats)
- **Status**: ✅ **Complete**
- **Implementation**: RTMP, SRT, WHIP ingest → HLS, WebRTC egress (DASH planned)
- **UI Components**: Protocol documentation with dynamic URLs
- **Marketing Promise**: "Multi-protocol streaming with all industry standards"

### Enhanced Client Metrics
- **Status**: ✅ **Complete**
- **Implementation**: MistServer `/clients` API integration, packet statistics, geographic data
- **UI Components**: Detailed technical metrics, bandwidth monitoring
- **Marketing Promise**: "Comprehensive client metrics and performance data"

### Cluster Router
- **Status**: ✅ **Complete**
- **Implementation**: Capacity-aware routing via Quartermaster, multi-tier support, health-based failover, tenant-aware cluster assignment
- **UI Components**: Backend only (no admin UI yet)
- **Marketing Promise**: "Intelligent routing across global infrastructure"

### Payment Processing (Purser)
- **Status**: ✅ **Complete**
- **Implementation**: Stripe, Mollie, crypto payments (BTC, ETH, USDC, LPT)
- **UI Components**: ❌ **Missing** - No billing UI in webapp
- **Marketing Promise**: "Flexible payment options including cryptocurrency"

---

## 📊 Analytics & Monitoring

Aim: measure what matters. This powers dashboards, alerting, and billing.

### Real-time Viewer Counts
- **Status**: ✅ **Complete**
- **Implementation**: 
  - Live WebSocket updates via Signalman
  - ClickHouse materialized views for aggregations
  - Automatic viewer count rollups
- **UI Components**: Dashboard widgets with auto-refresh
- **Marketing Promise**: "Live viewer counts with instant updates"

### Enhanced Client Metrics
- **Status**: ✅ **Complete**
- **Implementation**: 
  - Packet statistics, connection quality, bandwidth monitoring
  - ClickHouse time-series storage
  - Efficient time-based queries
  - Automatic data retention
- **UI Components**: Technical metrics dashboard
- **Marketing Promise**: "Detailed performance metrics and packet statistics"

### Geographic Analytics
- **Status**: 🔄 **Partial**
- **Implementation**: 
  - Geographic data captured in Foghorn routing events (and client data where available)
  - ClickHouse geospatial functions
  - Efficient geographic aggregations
- **UI Components**: ❌ **Missing** - No geographic visualization
- **Marketing Promise**: "Geographic analytics and viewer distribution"

### Performance Metrics
- **Status**: ✅ **Complete**
- **Implementation**: 
  - Bandwidth, latency, packet loss tracking
  - ClickHouse time-series storage
  - Materialized views for performance aggregations
  - Automatic data cleanup
- **UI Components**: Real-time performance dashboard
- **Marketing Promise**: "Comprehensive performance monitoring"

### Usage Tracking
- **Status**: 🔄 **Critical Gap**
- **Implementation**: 
  - ✅ Metrics collected by Periscope
  - ✅ ClickHouse time-series storage
  - Billing aggregations via ClickHouse queries (no dedicated MVs yet)
- **UI Components**: ❌ **Missing** - No usage-based billing UI
- **Marketing Promise**: "Usage-based billing and cost tracking"

### Advanced Analytics
- **Status**: 🔄 **Partial**
- **Implementation**: 
  - Data collected in ClickHouse
  - Time-series optimized storage
  - Materialized views for aggregations
  - Geographic analysis support
  - Automatic data TTL
- **UI Components**: ❌ **Missing** - Advanced reporting UI
- **Marketing Promise**: "Advanced analytics with custom reporting"

---

## 🌐 Streaming & Distribution

Aim: get video from creators to viewers, reliably and with low latency.

### Multi-format Streaming
- **Status**: ✅ **Complete**
- **Implementation**: All protocols supported (RTMP, SRT, WHIP → HLS, WebRTC)
- **UI Components**: Protocol selection and URL generation
- **Marketing Promise**: "Support for all streaming protocols and formats"

### Drop-in AV Device Discovery
- **Status**: Discovery features in testing & review. Also requires build 
 pipelines and built-in sidecar for remote management.
- **Implementation**: Auto-discovery binary for ONVIF cameras, VISCA PTZ, NDI sources, USB webcams
- **UI Components**: Device discovery interface
- **Marketing Promise**: "Automatic discovery of cameras and AV equipment"

### Multi-stream Compositing
- **Status**: Capability exists in media server. Requires stream bonding 
 (replicating all the required ingests to 1 processing node), metering and infra 
 expansion.
- **Implementation**: Picture-in-picture, overlays, OBS-style mixing
- **UI Components**: Compositing interface
- **Marketing Promise**: "Advanced multi-stream compositing and mixing"

### Transcoding
- **Status**: 🔄 **Via Livepeer Network**, but requires some more Devops work.
- **Implementation**: Integration with Livepeer for GPU transcoding
- **UI Components**: Transcoding settings
- **Marketing Promise**: "GPU-powered transcoding via Livepeer Network"

### Multi-platform Restreaming
- **Status**: Might partner with Restream, as they're a customer of 
 MistServer so are nicely compatible.
- **Implementation**: Simultaneous streaming to multiple platforms
- **UI Components**: Platform configuration
- **Marketing Promise**: "Stream to multiple platforms simultaneously"

### Custom Domains
- **Status**: 🔄 **Partial**
- **Implementation**: ✅ Quartermaster tenant registry, ❌ DNS automation (api_dnsmgr), ❌ Certificate automation (api_certmgr)
- **UI Components**: ❌ **Missing** - Domain configuration UI
- **Marketing Promise**: "Custom branded streaming domains"

---

## 🎥 Content Management

Aim: manage live outputs and archives; currently minimal, outlined for future work.

### Live Recording
- **Status**: 🔄 **Basic**, requires mass storage nodes, metering and some API/DB work. Media server supports it effortlessly.
- **Implementation**: Recording capability exists in MistServer
- **UI Components**: ❌ **Missing** - No recording management UI
- **Marketing Promise**: "Automatic live stream recording and archival"

### VOD Management
- **Status**: ❌ **Not Implemented**
- **Implementation**: Video-on-demand storage and playback
- **UI Components**: VOD library interface
- **Marketing Promise**: "Complete video-on-demand management"

### Live Clipping
- **Status**: 🔄 **Basic**, requires storage provider, metering and some API/DB work. Media server supports it nicely.
- **Implementation**: Real-time clip creation during streams
- **UI Components**: Clipping interface
- **Marketing Promise**: "Live clipping with AI segmentation"

### Storage Management
- **Status**: ❌ **Not Implemented**
- **Implementation**: Storage quota and management
- **UI Components**: Storage dashboard
- **Marketing Promise**: "Flexible storage management and quotas"

### Content Moderation
- **Status**: ❌ **Not Implemented**
- **Implementation**: Automated content moderation
- **UI Components**: Moderation dashboard
- **Marketing Promise**: "AI-powered content moderation"

---

## 👥 Team & Account Features

Aim: collaboration and governance; backend is taking shape, UI to follow.

### Team Collaboration
- **Status**: ❌ **Not Implemented**
- **Implementation**: No data model, API or UI yet
- **UI Components**: ❌ **Missing** - Team management interface
- **Marketing Promise**: "Team collaboration with role-based access"

### Billing System
- **Status**: 🔄 **Payment Only**
- **Implementation**: ✅ Payment processing, ❌ **Usage-based billing missing**
- **UI Components**: ❌ **Missing** - No billing UI in webapp
- **Marketing Promise**: "Complete billing system with usage tracking"

### API Access Management
- **Status**: 🔄 **Backend Only**
- **Implementation**: API tokens work, no management UI
- **UI Components**: ❌ **Missing** - API key management interface
- **Marketing Promise**: "Complete API access management"

### Prepaid Credits
- **Status**: ❌ **Not Implemented**
- **Implementation**: Credit-based billing system
- **UI Components**: Credit management interface
- **Marketing Promise**: "Prepaid credit system for usage-based billing"

---

## 🔧 Developer & Integration Features

Aim: integrate cleanly; APIs exist, tooling and docs to improve.

### REST API
- **Status**: ✅ **Complete**
- **Implementation**: Full REST API for all operations
- **UI Components**: API documentation page
- **Marketing Promise**: "Complete REST API for all platform features"

### Webhooks
- **Status**: 🔄 **Partial**
- **Implementation**: MistServer webhooks, limited external webhooks
- **UI Components**: ❌ **Missing** - Webhook configuration UI
- **Marketing Promise**: "Comprehensive webhook system for integrations"

### NPM Packages
- **Status**: ❌ **Not Implemented**
- **Implementation**: JavaScript SDK and components
- **UI Components**: Package documentation
- **Marketing Promise**: "NPM packages for easy integration"

### Calendar Integration
- **Status**: ❌ **Not Implemented**
- **Implementation**: Calendar-based stream scheduling
- **UI Components**: Calendar interface
- **Marketing Promise**: "Calendar integration for scheduled streams"

### Custom Integrations
- **Status**: ❌ **Not Implemented**
- **Implementation**: Custom integration framework
- **UI Components**: Integration marketplace
- **Marketing Promise**: "Custom integrations and marketplace"

---

## 🚀 Advanced & Enterprise Features

Aim: long‑term bets; listed for planning, not shipping yet.

### AI Processing
- **Status**: ❌ **Not Implemented** Feature support on the edge node are in testing, but also requires metering, devops works, scaling up infra...
- **Implementation**: Real-time speech-to-text, object detection, content classification
- **UI Components**: AI processing dashboard
- **Marketing Promise**: "Live AI processing with real-time analysis"

### Multi-stream Compositing
- **Status**: ❌ **Not Implemented** Feature support on the edge node are in testing, but also requires metering, devops works, scaling up infra...
- **Implementation**: Advanced video compositing and mixing
- **UI Components**: Compositing studio interface
- **Marketing Promise**: "Professional multi-stream compositing"

### Device Discovery
- **Status**: ❌ **Not Implemented** Feature support on the single-binary node is in testing, but requires deployment pipeline and integrated sidecar for remote mangement.
- **Implementation**: Auto-discovery binary for professional AV equipment
- **UI Components**: Device management interface
- **Marketing Promise**: "Industry-first auto-discovery for AV devices"

---

## 📱 Mobile & Native Apps

Aim: capture and control from devices; early exploratory work only.

### Android App
- **Status**: 🔄 **Basic Implementation**
- **Implementation**: Did some basic scoping.
- **UI Components**: Mobile streaming interface
- **Marketing Promise**: "Full-featured Android broadcasting app"

### iOS App
- **Status**: ❌ **Not Implemented**
- **Implementation**: iOS streaming application
- **UI Components**: iOS streaming interface
- **Marketing Promise**: "Native iOS broadcasting app"

---

