# Service Events Backbone

**Status**: Active (2025-12-27)
**Owners**: Platform Architecture
**Scope**: Service-plane telemetry, audit trail, and API usage metering

---

## 1. Purpose

The Service Events backbone provides a unified, typed telemetry stream across core services. It supports:

- **Audit trails** for lifecycle events (auth, streams, tenants, billing).
- **API usage metering** for billing and analytics.
- **Real-time messaging** routing to Signalman.
- **Operational visibility** through consistent event taxonomy.

This is **service-plane** telemetry; media-plane and Mist triggers remain on the analytics pipeline.

---

## 2. End‑to‑End Flow

```
Service Producer → Decklog (gRPC) → Kafka [service_events] → Periscope Ingest → ClickHouse
                                                                           ↓
                                           Periscope Query → Kafka [billing.usage_reports] → Purser
                                                                           ↓
                                                                Usage records / billing
```

**Messaging path (real‑time)**:

```
Deckhand → Decklog → Kafka [service_events] → Signalman → GraphQL Subscriptions → UI
```

**Note**: Signalman only forwards messaging‑related service events (e.g., message/conversation updates). `api_request_batch` is ignored.

---

## 3. Transport & Envelope

**Protobuf source of truth**: `pkg/proto/ipc.proto` (`ServiceEvent` + typed payloads).

**Kafka JSON envelope**: `pkg/kafka/events.go` (`ServiceEvent` struct).

Decklog converts protobuf `ServiceEvent` into JSON and publishes to `service_events`.

---

## 4. Event Taxonomy

Event types are string constants emitted by services. The list below reflects current producers (not exhaustive).

### 4.1 API Usage (Bridge)

- `api_request_batch`

### 4.2 API Write Events (Bridge)

**Streams**

- `api_stream_created`, `api_stream_updated`, `api_stream_deleted`
- `api_stream_key_created`, `api_stream_key_deleted`, `api_stream_key_rotated`

**Tokens**

- `api_token_created`, `api_token_revoked`

**Tenant/Cluster ops**

- `api_tenant_updated`
- `api_tenant_cluster_assigned`, `api_tenant_cluster_unassigned`
- `api_cluster_created`, `api_cluster_updated`
- `api_cluster_invite_created`, `api_cluster_invite_revoked`
- `api_cluster_subscription_requested`, `api_cluster_subscription_approved`, `api_cluster_subscription_rejected`

**Billing**

- `api_payment_created`
- `api_subscription_created`, `api_subscription_updated`
- `api_topup_created`

### 4.3 Service‑of‑Record + Lifecycle Events

**Auth (Commodore)**

- `auth_login_succeeded`, `auth_login_failed`, `auth_registered`, `auth_token_refreshed`
- `token_created`, `token_revoked`, `wallet_linked`, `wallet_unlinked`

**Tenant + Cluster (Quartermaster)**

- `tenant_created`, `tenant_updated`, `tenant_deleted`
- `tenant_cluster_assigned`, `tenant_cluster_unassigned`
- `cluster_created`, `cluster_updated`, `cluster_deleted`
- `cluster_invite_created`, `cluster_invite_revoked`
- `cluster_subscription_requested`, `cluster_subscription_approved`, `cluster_subscription_rejected`

**Streams (Commodore)**

- `stream_created`, `stream_updated`, `stream_deleted`
- `stream_key_created`, `stream_key_deleted`

**Artifacts (Commodore + Foghorn)**

- `artifact_registered` (Commodore)
- `artifact_lifecycle` (Foghorn, emitted alongside clip/DVR/VOD lifecycle analytics)

**Billing (Purser)**

- `payment_created`, `payment_succeeded`, `payment_failed`
- `subscription_created`, `subscription_updated`, `subscription_canceled`
- `invoice_paid`, `invoice_payment_failed`
- `topup_created`, `topup_credited`, `topup_failed`

**Support (Deckhand)**

- `message_received`, `message_updated`, `conversation_created`, `conversation_updated`

---

## 5. Producers & Payloads

| Producer      | Payloads                                                                                                             | Source                              |
| ------------- | -------------------------------------------------------------------------------------------------------------------- | ----------------------------------- |
| Bridge        | `APIRequestBatch`, `StreamChangeEvent`, `StreamKeyEvent`, `AuthEvent`, `TenantEvent`, `ClusterEvent`, `BillingEvent` | GraphQL mutations + usage tracker   |
| Commodore     | `AuthEvent`, `StreamChangeEvent`, `StreamKeyEvent`, `ArtifactEvent`                                                  | Auth + stream + artifact registry   |
| Quartermaster | `TenantEvent`, `ClusterEvent`                                                                                        | Tenant + cluster lifecycle          |
| Purser        | `BillingEvent`                                                                                                       | Billing lifecycle (webhooks + gRPC) |
| Deckhand      | `MessageLifecycleData`                                                                                               | Messaging lifecycle                 |
| Foghorn       | `ArtifactEvent`                                                                                                      | Artifact lifecycle (clip/DVR/VOD)   |

**Notes**

- Demo mode skips ServiceEvent emission in the Gateway.
- Only **metadata** is stored for support events; message content is excluded.
- API usage aggregates include **hashed** user/token identifiers for unique counts (no raw IDs stored).
- Foghorn emits `artifact_lifecycle` service events via the Decklog client when sending clip/DVR/VOD lifecycle analytics.

---

## 6. Storage & Rollups

**ClickHouse tables**

- `api_events` (audit log for service_events topic, sanitized for support events)
- `api_requests` (raw usage batches from `api_request_batch`)
- `api_usage_hourly` / `api_usage_daily` (rollups from `api_requests`)

**Periscope Query**

- Aggregates `api_usage_hourly` for the billing period.
- Adds `api_requests`, `api_errors`, `api_duration_ms`, `api_complexity`, and `api_breakdown` to UsageSummary.
  - `api_breakdown` includes `auth_type`, `operation_type`, `operation_name`, `unique_users`, `unique_tokens`.

**Purser**

- Stores API usage as usage_records (`api_requests`, `api_errors`, `api_duration_ms`, `api_complexity`).
- Persists `api_breakdown` in `usage_details` for analytics/debug.

---

## 7. Kafka Topics & DLQ

**Topics (names set via env)**

- `analytics_events`
- `service_events`
- `billing.usage_reports`
- `decklog_events_dlq`

**Recommended retention defaults (ops‑configurable)**

- `analytics_events`: 7 days (short replay buffer; ClickHouse is source of truth)
- `service_events`: 180 days (audit trail + API usage, compliance‑leaning)
- `billing.usage_reports`: 365 days (billing safety and dispute window)
- `decklog_events_dlq`: 90 days (triage and replay window)

**DLQ**

- Periscope Ingest and Signalman wrap Kafka handlers and publish failures to `decklog_events_dlq`.
- DLQ payloads are JSON with base64-encoded keys/values and the original headers for replay.
- Include `tenant_id` and `event_type` headers on DLQ messages to keep tenant-aware replay filters and routing intact.

**Replay**

- There is no dedicated replay service. Use Kafka tooling to consume from `decklog_events_dlq`, decode the payload, and re-publish to the original topic.
- Preserve headers (`tenant_id`, `event_type`, etc.) when replaying so downstream enrichment behaves consistently.

---

## 8. Exclusions

- No inter‑service RPC call tracking.
- No read‑only API requests beyond aggregate usage batches.
- No message content stored in analytics.

---

## 9. Source Files (Key)

- `pkg/proto/ipc.proto`
- `api_firehose/internal/grpc/server.go`
- `api_gateway/internal/middleware/usage_tracker.go`
- `api_gateway/internal/resolvers/api_events.go`
- `api_control/internal/grpc/server.go`
- `api_tenants/internal/grpc/server.go`
- `api_ticketing/internal/handlers/webhooks.go`
- `api_realtime/cmd/signalman/main.go`
- `api_analytics_ingest/internal/handlers/handlers.go`
- `api_analytics_query/internal/handlers/billing.go`
- `api_billing/internal/handlers/jobs.go`
