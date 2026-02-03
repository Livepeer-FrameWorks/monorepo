# Analytics Pipeline (Periscope)

This document explains the FrameWorks analytics pipeline end-to-end:

```
MistServer → Helmsman → Foghorn → Decklog → Kafka → Periscope-Ingest → ClickHouse → Periscope-Query → Bridge (GraphQL) → UI
                                                                                          ↓
                                                                                  Kafka (billing.usage_reports) → Purser
```

It is written as a “how the system works” reference (vs an audit checklist).

## Related Docs / Source of Truth

- Units + semantics: `docs/standards/metrics.md`
- Trigger + realtime contracts: `pkg/proto/ipc.proto`
- Analytics query API (gRPC): `pkg/proto/periscope.proto`
- ClickHouse schema + MVs: `pkg/database/sql/clickhouse/periscope.sql`
- GraphQL schema: `pkg/graphql/schema.graphql`
- Gateway gqlgen mapping: `api_gateway/gqlgen.yml`

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

## 1) Emission: MistServer → Helmsman

Helmsman is the boundary where raw MistServer signals become typed protobuf messages (`pkg/proto/ipc.proto`) that the rest of the platform can reason about.

### A) MistServer Webhooks (event-driven)

Examples of “push” signals:

- Viewer connects/disconnects (`USER_NEW`, `USER_END`)
- Stream buffer/health updates (`STREAM_BUFFER`)
- Recording lifecycle (`RECORDING_*`)

Helmsman receives these over HTTP, parses them, and forwards to Foghorn as a typed `pb.MistTrigger`.

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

- GeoIP lookup: `pkg/geoip/geoip.go` (MMDB database)
- H3 bucketing: `api_balancing/internal/geo/bucket.go`
- Resolution 5 = ~253 km² hexagons (~8.5 km edge length)

**Proto contract** (`pkg/proto/ipc.proto`):

```protobuf
message GeoBucket {
  uint64 h3_index = 1;   // H3 cell id encoded as uint64
  uint32 resolution = 2; // H3 resolution (e.g., 5)
}
```

The centroid coordinates (not raw lat/lon) are written to ClickHouse. This provides regional analytics capability while preventing precise viewer location tracking.

Events enriched with geo data:

- `USER_NEW` / `USER_END` (viewer connect/disconnect): `api_balancing/internal/triggers/processor.go`
- Routing decisions: `api_balancing/internal/handlers/handlers.go`, `api_balancing/internal/grpc/server.go`

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

| Kafka `event_type`                     | Ingest handler            | Primary ClickHouse writes                                                    |
| -------------------------------------- | ------------------------- | ---------------------------------------------------------------------------- |
| `viewer_connect` / `viewer_disconnect` | `processViewerConnection` | `viewer_connection_events` (`event_type` stored as `connect` / `disconnect`) |
| `stream_buffer`                        | `processStreamBuffer`     | `stream_event_log` + `stream_health_samples`                                 |
| `stream_end`                           | `processStreamEnd`        | `stream_event_log`                                                           |
| `push_rewrite`                         | `processPushRewrite`      | `stream_event_log`                                                           |
| `play_rewrite`                         | `skipEvent`               | _(no ClickHouse write; non-canonical)_                                       |
| `stream_source`                        | `skipEvent`               | _(no ClickHouse write; non-canonical)_                                       |
| `push_end` / `push_out_start`          | `skipEvent`               | _(no ClickHouse write; non-canonical)_                                       |
| `stream_track_list`                    | `processTrackList`        | `track_list_events`                                                          |
| `recording_complete`                   | `skipEvent`               | _(no ClickHouse write; non-canonical)_                                       |
| `recording_segment`                    | `skipEvent`               | _(no ClickHouse write; non-canonical)_                                       |
| `stream_lifecycle_update`              | `processStreamLifecycle`  | `stream_state_current` (current state) + `stream_event_log` (history)        |
| `node_lifecycle_update`                | `processNodeLifecycle`    | `node_state_current` (current state) + `node_metrics_samples` (history)      |
| `client_lifecycle_update`              | `processClientLifecycle`  | `client_qoe_samples`                                                         |
| `load_balancing`                       | `processLoadBalancing`    | `routing_decisions`                                                          |
| `clip_lifecycle`                       | `processClipLifecycle`    | `artifact_state_current` (current state) + `artifact_events` (history)       |
| `dvr_lifecycle`                        | `processDVRLifecycle`     | `artifact_state_current` (current state) + `artifact_events` (history)       |
| `storage_lifecycle`                    | `processStorageLifecycle` | `storage_events`                                                             |
| `storage_snapshot`                     | `processStorageSnapshot`  | `storage_snapshots`                                                          |
| `process_billing`                      | `processProcessBilling`   | `processing_events`                                                          |
| `vod_lifecycle`                        | `processVodLifecycle`     | `artifact_state_current` + `artifact_events` (`content_type='vod'`)          |
| `api_request_batch`                    | `processAPIRequestBatch`  | `api_requests`                                                               |

_Note: `api_request_batch` also arrives via `service_events` and is written to `api_requests` (with audit rows in `api_events`)._

If you need to add a new event type, the switch lives in `api_analytics_ingest/internal/handlers/handlers.go` (`HandleAnalyticsEvent`).

### Core tables written by ingest

The exact schema is in `pkg/database/sql/clickhouse/periscope.sql`, but conceptually:

- `viewer_connection_events`: Viewer connect/disconnect session events (billing source of truth).
- `stream_event_log`: Stream lifecycle + notable stream events (start/end/errors, etc.).
- `stream_health_samples`: QoE / buffer health samples (bitrate/fps/codec/buffer state, issues).
- `client_qoe_samples`: Client lifecycle samples; input for rollups like `client_qoe_5m`.
- `node_metrics_samples` and `node_state_current`: Node telemetry and “current state” snapshots.
- `stream_state_current`: Current per-stream snapshot (including derived fields like `current_viewers`).
- `artifact_events` and `artifact_state_current`: Clip/DVR lifecycle events + current artifact state.
- `routing_decisions`: Load balancing decision telemetry (routing maps, etc.).
- `processing_events`: Transcoding/processing usage events (billing + analytics).
- `storage_snapshots` and `storage_events`: Storage capacity snapshots and lifecycle actions.
- `api_requests` and `api_events`: GraphQL/API usage aggregates + audit trail (from `service_events` / `api_request_batch`).

### Error handling: commits must not skip failures

If an insert fails (schema mismatch, non-null constraint violation, etc.), committing Kafka offsets past that message can permanently drop data.

The shared Kafka consumer behavior lives in `pkg/kafka/consumer.go`.

## 5) Storage: ClickHouse (Periscope schema)

ClickHouse is the platform’s time-series/event store for analytics, optimized for:

- high write rate
- time-range queries
- rollups via materialized views

### Materialized views (rollups)

Rollups exist so that “billing-critical” queries do not need to scan raw `viewer_connection_events` at read time:

- `viewer_hours_hourly` (hourly aggregation)
- `tenant_viewer_daily` (daily tenant rollup)
- `viewer_geo_hourly` (geo rollup)

Realtime-like derived data:

- `viewer_sessions_current` merges connect + disconnect into a single session record for current viewer calculations.

Operational rollups:

- `client_qoe_5m`, `stream_health_5m`, and other \*\_5m MVs for fast dashboard queries.

## 6) Query: Periscope Query (gRPC API)

Periscope Query is the read API that sits in front of ClickHouse. It provides:

- stream analytics (health, viewers, geo breakdowns)
- platform overview
- connection events (for UX analysis and/or support tooling)
- rollup access (5-minute aggregates, daily tiers, etc.)

Most list endpoints use cursor-based (keyset) pagination for time-series tables.

### Frequently used endpoints (and backing tables)

These live in `api_analytics_query/internal/grpc/server.go`:

- `GetStreamStatus` / `GetStreamsStatus`: reads `stream_state_current` for near-realtime stream state + quality fields.
- `GetStreamAnalyticsSummary`: MV-backed range aggregates (e.g., `stream_viewer_5m`, `stream_analytics_daily`, `stream_health_5m`, `client_qoe_5m`, `quality_tier_daily`).
- `GetStreamHealthMetrics`: reads `stream_health_samples` (detailed QoE samples) with cursor pagination.
- `GetConnectionEvents`: reads `viewer_connection_events` (includes raw `connection_addr`; redact at API boundary).
- `GetPlatformOverview`: uses `stream_state_current` (snapshot), `client_qoe_5m` (peak bandwidth), and `tenant_viewer_daily` (historical rollups).

## 7) API Exposure: Bridge (GraphQL)

Bridge exposes Periscope Query (and other services) via GraphQL:

- GraphQL schema: `pkg/graphql/schema.graphql`
- gqlgen mapping: `api_gateway/gqlgen.yml`
- Resolver implementations: `api_gateway/graph/schema.resolvers.go`

### Live + historical shape

- Historical analytics read from ClickHouse (Periscope Query).
- Live analytics events stream via Signalman subscriptions.
- GraphQL presents a **single canonical shape** (edges/nodes + subscriptions) so UIs can merge live + historical consistently.

### Demo mode data

Bridge can serve demo/mock analytics data. When adding new analytics fields, also update:

- `api_gateway/internal/demo/generators.go`

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

Billing’s canonical source is viewer disconnect session data:

```
USER_END / viewer disconnect
  → ClickHouse viewer_connection_events (bytes_transferred, session_duration)
  → hourly/daily rollups (MVs)
  → Periscope Query billing summarizer
  → Kafka billing.usage_reports
  → Purser (Postgres usage_records)
```

Notes:

- Use `docs/standards/metrics.md` for unit conversions (`_bps`, `_gb`, `_bytes`, etc.).
- “Peak bandwidth” must be derived from a rate table/rollup (e.g., `client_qoe_5m.avg_bw_out`), not cumulative byte counters.

## Known Gotchas (From the End-to-End Audit)

- **Tenant isolation**: All writes/reads must include `tenant_id`; avoid “zero UUID” sink behavior for missing tenants.
- **ClickHouse placeholders**: Use `?` placeholders consistently; `$1/$2/...` style is driver-incompatible in this codebase.
- **Session vs envelope event types**: Kafka uses `viewer_connect`/`viewer_disconnect`, but `viewer_connection_events.event_type` stores `connect`/`disconnect`.
- **Clip stages**: ClickHouse stores lowercase stages (`done`, `failed`, …); don’t query for enum strings like `STAGE_DONE`.
- **Materialized view semantics**: If an MV is meant to merge rows (connect+disconnect), the storage engine and ORDER BY must match the merge strategy.
- **Privacy**: Storing raw client IPs for internal analysis is acceptable, but API layers must redact them before exposing to tenants/users.

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

Billing rollups populated:

```sql
SELECT day, sum(viewer_hours), sum(egress_gb) FROM periscope.tenant_viewer_daily GROUP BY day ORDER BY day DESC LIMIT 14;
SELECT hour, sum(viewer_count), sum(egress_gb) FROM periscope.viewer_geo_hourly GROUP BY hour ORDER BY hour DESC LIMIT 48;
```

### B) Kafka health

If billing or analytics data appears to “freeze”, check:

- consumer group lag for the relevant group (Periscope Ingest, Purser)
- service logs around handler errors (schema mismatches often show here first)

## Extending Analytics (New Field / New Event)

Use this checklist when adding new analytics fields end-to-end:

1. Update protobuf contract (`pkg/proto/ipc.proto` or `pkg/proto/periscope.proto`)
2. Run `make proto`
3. Emit/populate the new field in the producing service (Helmsman/Foghorn/etc.)
4. Update ClickHouse schema (`pkg/database/sql/clickhouse/periscope.sql`) if needed
5. Update Periscope Ingest handlers to insert the new field
6. Update Periscope Query to select + return the new field
7. Update GraphQL schema (`pkg/graphql/schema.graphql`) if the field is exposed to UI
8. Run `make graphql` (server codegen) and implement resolver stubs
9. Update demo generators if applicable (`api_gateway/internal/demo/generators.go`)
10. Update frontend queries and run codegen in `website_application`
