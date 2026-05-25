# Analytics Pipeline (Periscope)

This document explains the FrameWorks analytics pipeline end-to-end:

```
MistServer → Helmsman → Foghorn → Decklog → Kafka → Periscope-Ingest → ClickHouse → Periscope-Query → Bridge (GraphQL) → UI
                                                                                          ↓
                                                                                  Kafka (billing.usage_reports) → Purser
```

It is written as a “how the system works” reference (vs an audit checklist).

## Glossary (Fields That Show Up Everywhere)

- `tenant_id`: Tenant UUID. **All analytics data must be partitioned and queried by tenant_id.**
- `stream_id`: Public, stable stream identifier exposed via GraphQL (safe to share with tenants/users).
- `internal_name`: Canonical stream identifier inside FrameWorks (not the external stream key). Some upstream payloads may include prefixes like `live+` / `vod+`; ingest normalizes by stripping those known prefixes via `mist.ExtractInternalName(...)` before storing. This value is **not exposed** publicly.
- `node_id`: Node identifier (MistServer instance / edge node).
- `session_id`: Viewer session identifier from MistServer (connect/disconnect lifecycle). Session IDs are node-scoped, so `viewer_sessions_current` keys on `node_id` plus `session_id` to avoid cross-node collisions.
- `event_id`: Event identifier used for pagination/cursors and uniqueness in ClickHouse.
- `request_id`: Clip/DVR workflow identifier.

## Services (What Each Does)

- `api_sidecar` (“Helmsman”): Receives MistServer webhooks + polls MistServer APIs; emits typed protobuf triggers into the platform.
- `api_balancing` (“Foghorn”): Enriches triggers (tenant/user/geo), applies redaction rules where appropriate, forwards events for storage/broadcast, and handles some orchestration.
- `api_firehose` (“Decklog”): Normalizes + publishes analytics events to Kafka (`analytics_events` topic).
- `api_analytics_ingest` (“Periscope Ingest”): Consumes Kafka analytics events and writes to ClickHouse.
- `api_analytics_query` (“Periscope Query”): Reads ClickHouse and serves gRPC analytics APIs; also produces billing usage reports.
- `api_gateway` (“Bridge”): GraphQL gateway to the control/query plane; calls Periscope Query and other services.
- `api_realtime` (“Signalman”): Realtime subscription hub (WebSocket/streaming); redacts client IPs for broadcast.
- `api_billing` (“Purser”): Consumes billing usage reports, stores usage records, exposes billing APIs.

Bridge exposes analytics through three access scopes: public topology, tenant analytics, and cluster operations. Tenant analytics stay keyed to the caller's `tenant_id`; cluster operations require ownership of the relevant cluster or node; public topology only exposes official cluster-level status. See [analytics-access-scopes.md](analytics-access-scopes.md).

## 1) Emission: MistServer → Helmsman

Helmsman is the boundary where raw MistServer signals become typed protobuf messages (`pkg/proto`) that the rest of the platform can reason about.

### A) MistServer Webhooks (event-driven)

Examples of “push” signals:

- Viewer connects/disconnects (`USER_NEW`, `USER_END`)
- Stream buffer/health updates (`STREAM_BUFFER`)
- Recording lifecycle (`RECORDING_*`)

Helmsman receives these over HTTP, parses them, and forwards to Foghorn as a typed `pb.MistTrigger`.

For final/accounting triggers (`USER_END`, `STREAM_END`, `PUSH_END`, `RECORDING_END`, `RECORDING_SEGMENT`, `LIVEPEER_SEGMENT_COMPLETE`, `PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE`), Helmsman persists the parsed trigger to a local write-ahead log before responding 200 OK to Mist, and a background forwarder retries until Foghorn returns a `MistTriggerAck` (which Foghorn only sends after Decklog's Kafka publish commits). See [trigger-durability.md](trigger-durability.md). The source id used for ack correlation is `sha256(node_id || NUL || trigger_type || NUL || payload_raw)` in `MistTrigger.RequestId`; Decklog derives the typed Kafka `event_id` from that source id as a deterministic UUID so Periscope's UUID-based fact tables stay idempotent.

Once a final/accounting trigger lands in Kafka, Periscope ingests it into `raw_mist_triggers` (durable audit) and then projects it into per-meter `*_final` tables (`viewer_sessions_final`, `stream_sessions_final`, `processing_segments_final`). Those `*_final` tables are the billing source of truth for Mist-derived meters; storage billing integrates canonical `storage_snapshots` directly. Five-minute canonical ledgers (`viewer_usage_5m`, `stream_runtime_5m`, `storage_gb_seconds_5m`, `processing_5m`, `api_usage_5m`) feed analytics and dashboard rollups. See [finalized-fact-tables.md](finalized-fact-tables.md) and [meter-contracts.md](meter-contracts.md).

### B) MistServer Poller (state snapshots)

Some “state” is better polled (or only available via Mist APIs), e.g.:

- Stream lifecycle snapshots (`STREAM_LIFECYCLE_UPDATE`)
- Client lifecycle snapshots (`CLIENT_LIFECYCLE_UPDATE`)
- Node/system metrics (`NODE_LIFECYCLE_UPDATE`)

Poller outputs are still delivered as typed protobuf triggers, but **often need tenant enrichment downstream** (because Mist doesn’t know FrameWorks tenants).

## 2) Enrichment + Routing: Helmsman → Foghorn

Foghorn is where events become "platform-shaped":

- Ensures `tenant_id` / `user_id` are attached where possible (e.g., from Commodore stream lookups).
- Adds geo enrichment (GeoIP lookup + H3 bucketing).
- Passes raw client IPs through for storage; redaction happens at broadcast/API boundaries (Signalman, Bridge).

### Geographic enrichment (H3 bucketing)

Viewer geographic data is normalized using Uber's H3 hexagonal spatial index:

```
Client IP → GeoIP lookup → (country, city, raw lat/lon)
                                       ↓
                              H3 bucket (resolution 5)
                                       ↓
                              Cell centroid lat/lon → ClickHouse
```

**Implementation:**

- GeoIP lookup: `pkg/geoip` (MMDB database)
- H3 bucketing: `api_balancing/internal/geo`
- Resolution 5 = ~253 km² hexagons (~8.5 km edge length)

**Proto contract** (`pkg/proto`):

```protobuf
message GeoBucket {
  uint64 h3_index = 1;   // H3 cell id encoded as uint64
  uint32 resolution = 2; // H3 resolution (e.g., 5)
}
```

The centroid coordinates (not raw lat/lon) are written to ClickHouse. This provides regional analytics capability while preventing precise viewer location tracking.

Events enriched with geo data:

- `USER_NEW` / `USER_END` (viewer connect/disconnect): `api_balancing/internal/triggers`
- Routing decisions: `api_balancing/internal/handlers`, `api_balancing/internal/grpc`

### Tenant attribution (critical)

Many downstream tables and queries rely on tenant_id being correct.

Rule of thumb:

- If an event is associated with a stream, Foghorn should be able to derive tenant context from `internal_name` (or stream key validation paths).
- “Missing tenant” events should not silently sink into a “zero UUID” tenant.

## 3) Kafka Backbone: Foghorn → Decklog → Kafka

Decklog publishes analytics events to Kafka as a single envelope type (see `api_firehose`), including:

- `EventID` (UUID)
- `EventType` (string; canonicalized)
- `TenantID`
- `Timestamp`
- `Source`
- `Data` (a transparent JSON representation of the underlying protobuf payload)

This provides:

- durable buffering and replay
- fan-out to multiple consumers (ClickHouse ingest, realtime pipelines, etc.)

## 4) Ingest: Kafka → ClickHouse (Periscope Ingest)

Periscope Ingest consumes `analytics_events` and maps each event type into one or more ClickHouse inserts.
It also consumes `service_events` for API usage/audit events (notably `api_request_batch`).

### Kafka event types → ingest handlers → ClickHouse writes

Periscope Ingest routes on Kafka `event_type` (the canonical strings emitted by Decklog):

| Kafka `event_type`                     | Ingest handler                | Primary ClickHouse writes                                                               |
| -------------------------------------- | ----------------------------- | --------------------------------------------------------------------------------------- |
| `viewer_connect` / `viewer_disconnect` | `processViewerConnection`     | `viewer_connection_events` (`event_type` stored as `connect` / `disconnect`)            |
| `stream_buffer`                        | `processStreamBuffer`         | `stream_event_log` + `stream_health_samples`                                            |
| `stream_end`                           | `processStreamEnd`            | `stream_state_current` (`status='offline'`) + `stream_event_log`                        |
| `push_rewrite`                         | `processPushRewrite`          | `stream_event_log`                                                                      |
| `play_rewrite`                         | `skipEvent`                   | _(no ClickHouse write; non-canonical)_                                                  |
| `stream_source`                        | `skipEvent`                   | _(no ClickHouse write; non-canonical)_                                                  |
| `push_end` / `push_out_start`          | `skipEvent`                   | _(no ClickHouse write; non-canonical)_                                                  |
| `stream_track_list`                    | `processTrackList`            | `track_list_events`                                                                     |
| `recording_complete`                   | `skipEvent`                   | _(no ClickHouse write; non-canonical)_                                                  |
| `recording_segment`                    | `skipEvent`                   | _(no ClickHouse write; non-canonical)_                                                  |
| `stream_lifecycle_update`              | `processStreamLifecycle`      | `stream_state_current` (current state) + `stream_event_log` (history)                   |
| `node_lifecycle_update`                | `processNodeLifecycle`        | `node_state_current` (current state) + `node_metrics_samples` (history)                 |
| `client_lifecycle_batch`               | `processClientLifecycleBatch` | `client_qoe_samples` (one ClickHouse insert per batch; see "Client QoE sampling" below) |
| `load_balancing`                       | `processLoadBalancing`        | `routing_decisions`                                                                     |
| `clip_lifecycle`                       | `processClipLifecycle`        | `artifact_state_current` (current state) + `artifact_events` (history)                  |
| `dvr_lifecycle`                        | `processDVRLifecycle`         | `artifact_state_current` (current state) + `artifact_events` (history)                  |
| `storage_lifecycle`                    | `processStorageLifecycle`     | `storage_events`                                                                        |
| `storage_snapshot`                     | `processStorageSnapshot`      | `storage_snapshots`                                                                     |
| `process_billing`                      | `processProcessBilling`       | `processing_events` diagnostic telemetry                                                |
| `vod_lifecycle`                        | `processVodLifecycle`         | `artifact_state_current` + `artifact_events` (`content_type='vod'`)                     |
| `federation_event`                     | `processFederationEvent`      | `federation_events`                                                                     |
| `api_request_batch`                    | `processAPIRequestBatch`      | `api_requests`                                                                          |

_Note: `api_request_batch` also arrives via `service_events` and is written to `api_requests` (with audit rows in `api_events`)._

If you need to add a new event type, the switch lives in `api_analytics_ingest/internal/handlers` (`HandleAnalyticsEvent`).

### Core tables written by ingest

The exact schema is in `pkg/database/sql/clickhouse`, but conceptually:

- `viewer_connection_events`: Viewer connect/disconnect session events retained for support diagnostics. The rated billing source is `viewer_sessions_final`, projected from durable `USER_END` triggers.
- `stream_event_log`: Stream lifecycle + notable stream events (start/end/errors, etc.).
- `stream_health_samples`: QoE / buffer health samples (bitrate/fps/codec/buffer state, issues).
- `client_qoe_samples`: Client lifecycle samples; input for rollups like `client_qoe_5m`. Diagnostic-only — see "Client QoE sampling" below for cadence and the explicit non-authority over viewer counts / billing.
- `node_metrics_samples` and `node_state_current`: Node telemetry and “current state” snapshots.
- `stream_state_current`: Current per-stream snapshot (including derived fields like `current_viewers`).
- `artifact_events` and `artifact_state_current`: Clip/DVR lifecycle events + current artifact state.
- `routing_decisions`: Load balancing decision telemetry (routing maps, etc.).
- `processing_events`: Transcoding/processing usage telemetry retained for diagnostics. The rated processing source is `processing_segments_final`, projected from durable Livepeer and AV segment-complete triggers.
- `storage_snapshots` and `storage_events`: Storage capacity snapshots and lifecycle actions.
- `federation_events`: Cross-cluster federation telemetry (peering, replication, artifact access, redirects) with geo coordinates (`local_lat`/`local_lon`/`remote_lat`/`remote_lon`).
- `api_requests` and `api_events`: GraphQL/API usage aggregates + audit trail (from `service_events` / `api_request_batch`).

### Error handling: commits must not skip failures

If an insert fails (schema mismatch, non-null constraint violation, etc.), committing Kafka offsets past that message can permanently drop data.

The shared Kafka consumer behavior lives in `pkg/kafka`.

## 5) Storage: ClickHouse (Periscope schema)

ClickHouse is the platform’s time-series/event store for analytics, optimized for:

- high write rate
- time-range queries
- rollups via materialized views

### Materialized views (rollups)

Rollups exist so dashboard queries do not scan canonical ledgers for common 24h/7d/30d ranges. They are not billing inputs. Billing walks finalized facts and canonical ledgers directly.

Public rollup names such as `tenant_usage_hourly`, `viewer_hours_hourly`, `viewer_geo_hourly`, `storage_usage_hourly`, `processing_hourly`, and `api_usage_hourly` are deduped read surfaces. If a refreshable materialized view uses `APPEND`, it writes into an internal store table and the public name collapses refresh versions before resolvers read it. A resolver must not query raw append targets.

Realtime-like derived data:

- `viewer_sessions_current` merges connect + disconnect into a single session record for current viewer calculations.

Operational rollups:

- `client_qoe_5m`, `stream_health_5m`, and other \*\_5m MVs for fast dashboard queries.

### Client QoE sampling

The client-side QoE pipeline is rate-shaped at two points:

- **Helmsman polls MistServer's clients API every 60 seconds**, not every 10s. The 10s monitor tick still drives node and stream lifecycle, but `emitClientLifecycle` runs once per 6 ticks. Helmsman generates a UUID per sample so `client_qoe_samples.event_id` is a stable replay-dedup key.
- **Foghorn batches enriched `ClientLifecycleUpdate`s per `(tenant_id, stream_id, node_id)`** into `ClientLifecycleBatch` triggers (flushed at 1 s or 1000 samples). Decklog publishes one Kafka record per batch; Periscope does one ClickHouse `INSERT INTO client_qoe_samples` per batch. A batch flush failure is logged + counted on `foghorn_client_lifecycle_batch_drops_total` and dropped after one retry — QoE telemetry is intentionally lossy at the edge to keep the trigger processor unblocked.

This makes `client_qoe_samples` (and the derived `client_qoe_5m` MV) **diagnostic-only**, not the source of truth for viewer counts or billing:

- `client_qoe_5m.active_sessions = count(DISTINCT session_id)` per 5-minute bucket is a _sampled_ metric. A session whose entire lifetime falls between two 60s polls produces no QoE rows and is invisible to this rollup.
- For live viewer counts, use final connection lifecycle state derived from `USER_NEW` / `USER_END`. For billing, read `viewer_sessions_final` and derived canonical ledgers — sampled QoE rows are never authoritative.

## 6) Query: Periscope Query (gRPC API)

Periscope Query is the read API that sits in front of ClickHouse. It provides:

- stream analytics (health, viewers, geo breakdowns)
- platform overview
- connection events (for UX analysis and/or support tooling)
- rollup access (5-minute aggregates, daily tiers, etc.)

Most list endpoints use cursor-based (keyset) pagination for time-series tables.

### Frequently used endpoints (and backing tables)

These live in `api_analytics_query/internal/grpc`:

- `GetStreamStatus` / `GetStreamsStatus`: reads `stream_state_current` for near-realtime stream state + quality fields.
- `GetStreamAnalyticsSummary`: finalized viewer usage facts plus current session state for range viewer/session totals, with support rollups such as `stream_health_5m`, `client_qoe_5m`, and `quality_tier_daily` for QoE and quality breakdowns.
- `GetStreamHealthMetrics`: reads `stream_health_samples` (detailed QoE samples) with cursor pagination.
- `GetConnectionEvents`: reads `viewer_connection_events` (includes raw `connection_addr`; redact at API boundary).
- `GetPlatformOverview`: uses `stream_state_current` (snapshot), `client_qoe_5m` (peak bandwidth), `viewer_usage_5m_v` (viewer totals), and `stream_runtime_5m_v` (runtime/peak concurrency).

## 7) API Exposure: Bridge (GraphQL)

Bridge exposes Periscope Query (and other services) via GraphQL:

- GraphQL schema: `pkg/graphql`
- gqlgen mapping: `api_gateway`
- Resolver implementations: `api_gateway/graph`

### Live + historical shape

- Historical analytics read from ClickHouse (Periscope Query).
- Live analytics events stream via Signalman subscriptions.
- GraphQL presents a **single canonical shape** (edges/nodes + subscriptions) so UIs can merge live + historical consistently.

### Demo mode data

Bridge can serve demo/mock analytics data. When adding new analytics fields, also update:

- `api_gateway/internal/demo`

Otherwise demo mode will return `null`/zero for new fields even if prod mode works.

### Privacy + sensitive fields

Privacy policy intent (from schema comments) is:

- Raw client IPs may be stored internally (ClickHouse), but must be redacted from API responses.
- Realtime paths already redact before broadcast (Signalman).

- GraphQL resolvers redact `connectionAddr` / `host` (returning `null`) while raw IPs remain stored in ClickHouse for internal analysis.
- File paths, S3 URLs, node IPs, and stream payloads are **admin/owner‑only**; resolvers return `null` for non‑privileged users.

## 8) Realtime: Signalman (Subscriptions)

Signalman is the realtime hub for dashboard subscriptions:

- viewer metric updates (`ViewerMetrics` / client lifecycle)
- node health events
- stream lifecycle events

Key rule: do not broadcast raw client IP fields; redact before emit.

## 9) Billing / Metering

Billing's canonical source is the finalized-fact tables (`viewer_sessions_final`, `stream_sessions_final`, `processing_segments_final`) plus direct hold-constant integration of canonical `storage_snapshots`. Default-priced meters are `delivered_minutes` and `storage_gb_seconds_cold`; `ingress_gb`, `egress_gb`, `storage_gb_seconds_hot`, and `media_seconds` are priceable but unrated by default. `media_seconds` carries plain codec and joint process/codec breakdowns in usage details so pricing can apply per-codec or per-workload multipliers without inventing one usage type per codec. See [meter-contracts.md](meter-contracts.md).

```
USER_END / STREAM_END / segment-complete
  → raw_mist_triggers (audit journal, see trigger-durability.md)
  → *_final tables (append-only MergeTree, materialized via min/argMax on projection_version_ms)
  → 5-min canonical ledgers (viewer_usage_5m, stream_runtime_5m, processing_5m, storage_gb_seconds_5m, api_usage_5m)
  → Periscope billing scheduler (5-min aligned cursor, 2-min settlement lag, walks billable_at_ms)
  → Kafka billing.usage_reports
  → Purser (Postgres usage_records, value_kind='delta')
  → rating engine → invoice line items
```

Dashboards (24h/7d/30d) read separate refreshable-MV rollups (`tenant_usage_hourly/daily`, `viewer_geo_hourly/daily`, etc.) sourced from the same canonical ledgers. Billing never reads dashboard rollups; dashboards never read the billing cursor source. See [finalized-fact-tables.md](finalized-fact-tables.md).

The dashboard rollup contract is part of the billing safety model: replacement refreshes must cover their full published retention, while append refreshes must write to internal versioned stores hidden behind deduped public names. A bounded replacement refresh silently truncates history and is not an acceptable release shape.

Notes:

- Use `docs/standards/metrics.md` for unit conversions (`_bps`, `_gb`, `_bytes`, etc.). Storage is stored as GiB-seconds internally and rated as GiB-hours (rating engine divides by 3600).
- "Peak bandwidth" is default-unrated and derived from `client_qoe_5m.avg_bw_out`; custom/marketplace pricing can opt in with an explicit meter rule.

### Cross-cluster billing attribution

In multi-cluster deployments, viewer/session billing events carry `cluster_id` (serving cluster) and `origin_cluster_id` (where the stream was ingested) when that context is known. Other analytics families carry the cluster fields that match their domain; for example, stream lifecycle rows currently store the emitting `cluster_id`, while routing decisions store local and remote cluster IDs. Periscope Query generates per-cluster usage records for Purser from finalized facts and canonical ledgers, enabling settlement of inter-cluster traffic.

See `docs/architecture/cross-cluster-billing.md` for the full attribution model, ClickHouse schema additions (`cluster_id` + `origin_cluster_id` on viewer connection events and viewer rollups; `cluster_id` only on stream lifecycle rows), and the settlement query.

## Gotchas

- **Session vs envelope event types**: Kafka uses `viewer_connect`/`viewer_disconnect`, but `viewer_connection_events.event_type` stores `connect`/`disconnect`.
- **Clip stages**: ClickHouse stores lowercase stages (`done`, `failed`, …); don't query for enum strings like `STAGE_DONE`.
- **ClickHouse placeholders**: Use `?` placeholders; `$1/$2/...` style is driver-incompatible in this codebase.

## Verification / Debugging Playbook

### A) ClickHouse sanity checks

Tenant poisoning (volume by tenant, look for all-zero UUID):

```sql
SELECT tenant_id, count() FROM periscope.stream_state_current GROUP BY tenant_id ORDER BY count() DESC;
SELECT tenant_id, count() FROM periscope.client_qoe_samples GROUP BY tenant_id ORDER BY count() DESC;
SELECT tenant_id, count() FROM periscope.stream_event_log GROUP BY tenant_id ORDER BY count() DESC;
```

Connection event completeness:

```sql
SELECT event_type, count() FROM periscope.viewer_connection_events GROUP BY event_type;
SELECT min(timestamp), max(timestamp) FROM periscope.viewer_connection_events;
```

Viewer session MV health:

```sql
SELECT countIf(disconnected_at IS NULL) AS still_connected, count() AS total FROM periscope.viewer_sessions_current;
```

Canonical billing facts and dashboard rollups populated:

```sql
SELECT count(), min(billable_at_ms), max(billable_at_ms) FROM periscope.viewer_sessions_final_v;
SELECT window_start, sum(seconds_observed), sum(up_bytes_observed + down_bytes_observed) FROM periscope.viewer_usage_5m_v GROUP BY window_start ORDER BY window_start DESC LIMIT 12;
SELECT toDate(window_start) AS day, sum(seconds_observed) / 3600.0, sum(down_bytes_observed) / 1073741824.0 FROM periscope.viewer_usage_5m_v GROUP BY day ORDER BY day DESC LIMIT 14;
SELECT hour, sum(viewer_count), sum(egress_gb) FROM periscope.viewer_geo_hourly GROUP BY hour ORDER BY hour DESC LIMIT 48;
```

### B) Kafka health

If billing or analytics data appears to “freeze”, check:

- consumer group lag for the relevant group (Periscope Ingest, Purser)
- service logs around handler errors (schema mismatches often show here first)
