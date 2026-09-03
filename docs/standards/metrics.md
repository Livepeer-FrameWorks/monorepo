# Metrics Semantic Contract

This document defines the authoritative units and semantics for all metrics in the FrameWorks platform.

## Naming Conventions

| Suffix            | Meaning                                                                   | Example                                  |
| ----------------- | ------------------------------------------------------------------------- | ---------------------------------------- |
| `_bytes`          | Byte quantity; metric type determines cumulative counter vs current gauge | `uploaded_bytes`, `control_outbox_bytes` |
| `_bps`            | Bits per second (rate)                                                    | `bandwidthInBps`, `bandwidthOutBps`      |
| `_bytes_per_sec`  | Bytes per second (rate)                                                   | `up_speed`, `down_speed`                 |
| `_gb`             | **GiB** (bytes / 1024³)                                                   | `egress_gb`, `display_storage_gb`        |
| `_gb_seconds`     | GiB-seconds for time-weighted storage meters                              | `storage_gb_seconds_cold`                |
| `_mbps`           | **Mibps** (bps / 1024²) for billing rate fields                           | `peak_bandwidth_mbps`                    |
| `_ms`             | Milliseconds                                                              | `stream_buffer_ms`, `latency_ms`         |
| `_pct` or `_rate` | Ratio 0.0-1.0                                                             | `packet_loss_rate`, `buffer_health`      |

## Data Categories

### 1. Node Metrics (Infrastructure Health)

**Source:** MistServer `/metrics.json` → Helmsman poller → ClickHouse `node_metrics_samples`

**Important:** In ClickHouse, `node_metrics_samples.bandwidth_in` / `bandwidth_out` are **cumulative bytes** since node start (not a rate). Rates are stored separately as `up_speed` / `down_speed` (bytes/sec).

| Field (ClickHouse `node_metrics_samples`) | Unit      | Type    | Description                                          |
| ----------------------------------------- | --------- | ------- | ---------------------------------------------------- |
| `bandwidth_in`                            | bytes     | Counter | Cumulative bytes received (resets on server restart) |
| `bandwidth_out`                           | bytes     | Counter | Cumulative bytes sent (resets on server restart)     |
| `up_speed`                                | bytes/sec | Gauge   | Instantaneous upload rate (computed from delta)      |
| `down_speed`                              | bytes/sec | Gauge   | Instantaneous download rate (computed from delta)    |
| `connections_current`                     | count     | Gauge   | Current active viewer connections                    |
| `stream_count`                            | count     | Gauge   | Current active stream count on the node              |
| `cpu_usage`                               | 0-100     | Gauge   | CPU utilization percentage                           |
| `ram_current`                             | bytes     | Gauge   | Current RAM usage                                    |
| `ram_max`                                 | bytes     | Gauge   | Total RAM available                                  |
| `disk_used_bytes`                         | bytes     | Gauge   | Current disk usage                                   |
| `disk_total_bytes`                        | bytes     | Gauge   | Total disk capacity                                  |
| `shm_used_bytes`                          | bytes     | Gauge   | Shared memory usage                                  |
| `shm_total_bytes`                         | bytes     | Gauge   | Total shared memory                                  |
| `operational_mode`                        | enum      | Gauge   | Node mode: `normal`, `draining`, or `maintenance`    |

**Note:** RAM fields are currently captured as **bytes** (from MistServer `mem_total`/`mem_used`) and stored as bytes. The proto comments still mention MiB, but the actual data path is bytes.

### 2. Viewer Session Metrics (Billing Source)

**Source:** MistServer `USER_END` trigger → `raw_mist_triggers` → `viewer_sessions_final` (append-only projection) → `viewer_usage_5m` ledger → billing cursor + dashboard rollups. See [meter-contracts.md](../architecture/meter-contracts.md).

| Field               | Unit    | Type      | Description                                                       |
| ------------------- | ------- | --------- | ----------------------------------------------------------------- |
| `bytes_transferred` | bytes   | Counter   | Total bytes for session (`max(0, up_bytes) + max(0, down_bytes)`) |
| `session_duration`  | seconds | Counter   | Session duration                                                  |
| `ingress_gb`        | GiB     | Aggregate | Sum of uploaded bytes / 1024³ from finalized session facts        |
| `egress_gb`         | GiB     | Aggregate | Sum of downloaded bytes / 1024³ from finalized session facts      |
| `viewer_hours`      | hours   | Aggregate | Dashboard display value: sum of duration / 3600                   |

**Aggregation Pipeline:**

```
USER_END trigger (uploaded/downloaded bytes total)
  → raw_mist_triggers
  → viewer_sessions_final
  → viewer_usage_5m canonical ledger
  → billing.usage_reports (Kafka, rated path)
  → dashboard rollups (analytics path)
```

### 3. Stream Health Metrics (QoE)

**Source:** MistServer `STREAM_BUFFER` trigger **and** Helmsman poller (stream lifecycle updates) → `stream_health_samples`

| Field (ClickHouse `stream_health_samples`) | Unit        | Type  | Description                                                                  |
| ------------------------------------------ | ----------- | ----- | ---------------------------------------------------------------------------- |
| `bitrate`                                  | kbps        | Gauge | Primary video bitrate (`StreamTrack.bitrate_kbps`)                           |
| `fps`                                      | frames/sec  | Gauge | Primary video FPS; Mist `0` means unknown/dynamic and is treated as absent   |
| `width` / `height`                         | pixels      | Gauge | Primary video dimensions                                                     |
| `codec`                                    | string      | Gauge | Primary video codec                                                          |
| `buffer_size`                              | ms          | Gauge | Overall buffer in ms (`stream_buffer_ms` / lifecycle `buffer_ms`)            |
| `max_keepaway_ms`                          | ms          | Gauge | Mist's maximum viewer distance from live; retained as the health denominator |
| `buffer_health`                            | 0.0-1.0     | Gauge | `buffer_size / max_keepaway_ms` (clamped to 1.0)                             |
| `buffer_state`                             | enum string | Gauge | Buffer state (`FULL`, `EMPTY`, `DRY`, `RECOVER`, …)                          |
| `quality_tier`                             | string      | Gauge | Rich tier label (e.g. `"1080p60 H264 @ 6Mbps"`)                              |
| `has_issues`                               | boolean     | Gauge | Issue flag (Mist + Helmsman derived)                                         |
| `issues_description`                       | string      | Gauge | Human-readable issue summary                                                 |
| `track_count`                              | count       | Gauge | Track count                                                                  |
| `track_metadata`                           | JSON        | Gauge | Serialized typed tracks (includes per-track jitter/buffer/bitrate_bps, etc.) |

**Where packet loss + jitter live**

- Packet loss rate is derived from client QoE rollups (`client_qoe_5m.pkt_loss_rate`).
- Stream-wide jitter is stored in `stream_health_samples.frame_jitter_ms` from both `StreamBufferTrigger.stream_jitter_ms` and the primary 10-second `StreamLifecycleUpdate.jitter_ms` producer. It has 5m rollups in `stream_health_5m.avg_frame_jitter_ms` and `stream_health_5m.max_frame_jitter_ms`. Per-track jitter is also available inside `track_metadata`.
- Presence and value are independent for `buffer_size`, `buffer_health`,
  `max_keepaway_ms`, and `frame_jitter_ms`: a
  producer-supplied zero is retained as zero, while an absent field remains `NULL`.
  This distinction is required for correct source fidelity and must not be implemented
  with a `value > 0` presence check.
- Historical `stream_health_samples` written before the v0.3.0 fidelity change may
  represent a supplied zero as `NULL`. Because ClickHouse averages ignore `NULL`,
  aggregates spanning that deployment boundary are not strictly comparable to
  post-change aggregates that correctly include zero-valued samples.

### 4. Real-time Viewer Metrics (Live Dashboard)

**Source:** Helmsman client poller → `ClientLifecycleUpdate` → Signalman subscription

| Field             | Unit     | Type  | Description                   |
| ----------------- | -------- | ----- | ----------------------------- |
| `bandwidthInBps`  | bits/sec | Gauge | Viewer upload rate            |
| `bandwidthOutBps` | bits/sec | Gauge | Viewer download rate (egress) |

### 5. Platform Overview Metrics

**Source:** `stream_state_current` (real-time snapshots) + finalized viewer usage ledgers (`viewer_usage_5m_v`) + canonical dashboard rollups (`tenant_usage_hourly/daily`, `client_qoe_5m`) via Periscope Query

| Field                | Unit  | Type  | Description                            |
| -------------------- | ----- | ----- | -------------------------------------- |
| `totalUploadBytes`   | bytes | Gauge | Sum of ingest bytes across all streams |
| `totalDownloadBytes` | bytes | Gauge | Sum of egress bytes across all streams |

## Future: QoE Metrics

Reserved fields for quality-of-experience tracking:

| Field                    | Unit  | Type    | Description                           |
| ------------------------ | ----- | ------- | ------------------------------------- |
| `time_to_first_frame_ms` | ms    | Gauge   | Stream startup latency                |
| `glass_latency_ms`       | ms    | Gauge   | End-to-end latency (camera to screen) |
| `rebuffer_count`         | count | Counter | Number of rebuffering events          |
| `rebuffer_duration_ms`   | ms    | Counter | Total time spent rebuffering          |
| `video_startup_time_ms`  | ms    | Gauge   | Time from play request to first frame |
| `seek_latency_ms`        | ms    | Gauge   | Time to complete seek operation       |

### Player Boot Telemetry (landed)

Time-to-first-frame is captured by the browser player as a one-shot boot waterfall
(`player_boot_samples`; see analytics-pipeline.md → "Player boot telemetry"). All
span fields are **ms**; `total_ttf_ms` is the headline TTFF. Percentiles are computed
at read time (`quantileIf` over boots that reached first frame), not pre-rolled.

| Field                | Unit | Description                             |
| -------------------- | ---- | --------------------------------------- |
| `total_ttf_ms`       | ms   | boot_start → first painted frame (rVFC) |
| `gateway_resolve_ms` | ms   | GraphQL `resolveViewerEndpoint` only    |
| `mist_hydrate_ms`    | ms   | MistServer `json_*.js` hydration        |
| `player_select_ms`   | ms   | Player/source selection                 |
| `connect_ms`         | ms   | Player init / network connect           |
| `prebuffer_ms`       | ms   | Initial buffering before first frame    |

Ingest exposes `periscope_player_boot_events_total{status}` (Counter; `processed`/`error`).

### Client Session Correlation (landed)

`client_session_id` is an opaque, attach-scoped diagnostic identifier. The browser
generates it only when `telemetry.session` is enabled and sends it as `fwsid` on Mist
playback and stream-info URLs. It has no unit, must match `[A-Za-z0-9._-]{1,64}`, and
must never be interpreted as a viewer, account, billing, or globally unique identity.
It may be absent or ambiguous when Mist reuses a token-backed session, so all joins are
best-effort and bounded by tenant, content, and time.

Stored request URLs are diagnostic dimensions, not authentication records. Ingest rebuilds
the retained query from an explicit allowlist containing only a validated `fwsid`; every
other parameter, URL userinfo, and the fragment are discarded. If only the query is malformed,
ingest retains a safely reparsed scheme/host/path prefix; an unparseable path stores an empty URL.
Session-detail reads must collapse both source tables before joining;
joining raw browser beacons to raw Mist connection events multiplies rows and corrupts
additive QoE counters. The collapsed browser-session surface is keyset-paginated before
connection lookup; only IDs on the selected page participate in that lookup. Unmatched
connection dimensions use semantic empty values, not storage-type defaults such as
NUL-padded `FixedString` bytes.

## Counter vs Gauge Semantics

### Counter

Cumulative value that only increases (may reset on restart).

**Aggregation rules:**

- Time window totals: Use `max - min` in the window
- Rolling rates: Compute delta between samples and divide by elapsed time
- Cross-window sums: Sum of per-window totals

**Examples:** `bandwidth_out`, `bytes_transferred`, `rebuffer_count`

### Gauge

Point-in-time measurement that can go up or down.

**Aggregation rules:**

- Average over time: Use `avg()`
- Peak value: Use `max()`
- Current state: Use latest value

**Examples:** `cpu_usage`, `connections_current`, `buffer_health`

## Reset Handling

| Scope               | Resets On      | Safe for Cumulative Storage? | Mitigation                        |
| ------------------- | -------------- | ---------------------------- | --------------------------------- |
| Node-level counters | Server restart | Yes                          | Detect restart via timestamp gaps |
| Per-stream counters | Stream end     | **NO**                       | Derive from session aggregates    |
| Session counters    | Never          | Yes                          | Each session is independent       |

### Per-Stream Counter Warning

MistServer's per-stream counters (`streams[x].bw`, `streams[x].tot`) reset when the stream goes offline. **Do not store these as cumulative values.** Instead:

- Use `USER_END` session data projected into `viewer_sessions_final` / `viewer_usage_5m` for accurate bandwidth per stream
- Aggregate from canonical ledgers into dashboard rollups such as `stream_analytics_daily`

## MistServer Data Sources

### Node-level JSON (`/metrics.json`)

| Field  | Type    | Indices                                              | Description                                                                                |
| ------ | ------- | ---------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| `bw`   | Counter | `[out, in]`                                          | Cumulative bytes transferred (server-wide); index 0 = bytes sent, index 1 = bytes received |
| `curr` | Gauge   | `[viewers, incoming, outgoing, unspecified, cached]` | Current active connections                                                                 |
| `tot`  | Counter | `[viewers, incoming, outgoing, unspecified]`         | Cumulative session counts                                                                  |
| `pkts` | Counter | `[sent, lost, retrans]`                              | Packet counters                                                                            |

### Triggers

**USER_NEW** - Viewer connects

- `stream name`, `host` (client IP/host), `connector`, `session_id`, `request_url`

**USER_END** - Viewer disconnects (authoritative for billing)

- `session identifier`, `stream name`, `connector`
- `duration in seconds` (session length)
- `uploaded bytes total` (egress - bytes sent TO viewer)
- `downloaded bytes total` (ingress - bytes received FROM viewer)

**STREAM_BUFFER** - Stream health changes

- `stream_name`, `buffer_state` (EMPTY, FULL, DRY, RECOVER)
- `health` (JSON parsed into typed fields): stream buffer/jitter, max keepaway, issues, and typed tracks (codec/fps/bitrate_kbps/bitrate_bps/jitter/buffer + frame timing)

## Frontend Display Guidelines

### Unit Conversions

| Backend Field      | Unit      | Frontend Display | Conversion                  |
| ------------------ | --------- | ---------------- | --------------------------- |
| `totalUploadBytes` | bytes     | "1.2 GB"         | `formatBytes(value)`        |
| `bandwidthOutBps`  | bits/sec  | "1.2 Mbps"       | `value / 1_000_000`         |
| `up_speed`         | bytes/sec | "1.2 MB/s"       | `formatBytes(value) + '/s'` |
| `primary_bitrate`  | kbps      | "6.0 Mbps"       | `value / 1000`              |
| `buffer_health`    | 0.0-1.0   | "85%"            | `value * 100`               |
| `packet_loss_rate` | 0.0-1.0   | "0.5%"           | `value * 100`               |
| `ingress_gb`       | GiB       | "1.2 GiB"        | Display as-is               |
| `egress_gb`        | GiB       | "1.2 GiB"        | Display as-is               |

### Rate vs Cumulative Display

- **Rate fields** (`_bps`, `_bytes_per_sec`): Display with `/s` suffix
- **Counters** (normally `_total`, including byte-total counters): Display as totals, never with `/s`
- **Gauges** (including current-size metrics such as `control_outbox_bytes`): Display the current value

### Helmsman durability metrics

| Metric                                      | Type    | Meaning                                                                    |
| ------------------------------------------- | ------- | -------------------------------------------------------------------------- |
| `helmsman_control_outbox_pending`           | Gauge   | Durable media-control rows pending or awaiting same-epoch confirmation     |
| `helmsman_control_outbox_bytes`             | Gauge   | Current bytes occupied by pending and unconfirmed control rows             |
| `helmsman_control_outbox_quarantined`       | Gauge   | Rows quarantined after decode or repeated read failure                     |
| `helmsman_control_outbox_quarantined_bytes` | Gauge   | Current bytes occupied by quarantined rows                                 |
| `helmsman_control_delivery_outcomes_total`  | Counter | Delivery outcomes by durability class, including persisted/sent/confirmed  |
| `helmsman_control_response_drops_total`     | Counter | Late or duplicate inbound replies discarded without blocking receive       |
| `helmsman_control_outbox_scan_errors_total` | Counter | Read, decode, metric, and quarantine errors by phase                       |
| `helmsman_trigger_wal_pending`              | Gauge   | Final/accounting Mist triggers awaiting a positive Foghorn acknowledgement |
| `helmsman_trigger_wal_appends_total`        | Counter | Trigger-WAL append, duplicate, and error outcomes                          |
| `helmsman_trigger_ack_outcomes_total`       | Counter | Foghorn trigger acknowledgement outcomes                                   |

### Helmsman stream WebSocket metrics

| Metric                                | Type    | Meaning                                                                |
| ------------------------------------- | ------- | ---------------------------------------------------------------------- |
| `helmsman_stream_ws_connections`      | Gauge   | `1` while the MistController change-detector socket is open            |
| `helmsman_stream_ws_reconnects_total` | Counter | Reconnect attempts after the initial WebSocket dial                    |
| `helmsman_stream_ws_nudges_total`     | Counter | Relevant status/input/output changes queued for targeted refresh       |
| `helmsman_stream_ws_refreshes_total`  | Counter | Batched filtered `active_streams` API requests made by the accelerator |

These metrics describe latency acceleration, not source-of-truth health. Alerting
must retain the normal poll/control-trigger signals. A sustained zero connection
gauge plus increasing reconnects indicates that Helmsman is operating on its
unchanged polling backstop. Helmsman sends periodic WebSocket pings and requires
pongs within a bounded read window, so a half-open or wedged controller connection
is closed, reflected in the gauge, and reconnected instead of remaining indefinitely
reported as open.

Each configured Mist node is owned by one immutable Helmsman runtime generation: node
identity, authoritative and accelerator API-client lanes, cancellation, serialized
requests, WebSocket queue, edge-API request lifetime, and spawned poll work change
together. Replacement cancels the old generation, publishes the new one without waiting
on an active transport write, and joins retired work asynchronously; shutdown joins all
current and retired work before returning. A different node clears
vanish/admission/observation maps; re-registering the same node preserves pending offline
and reconciliation evidence. Observation IDs remain monotonic across both paths.
Node-metric forwarding
runs in a joined worker with a bounded deadline and one-slot wake coalescing, so a slow
control send cannot block the monitor update consumer. Because gRPC streaming sends have no
per-message cancellation, every send—including context-free heartbeat, relay, and outbox
writes—gets an independent 15-second transport watchdog that begins only after it owns the
send lane. The ordinary non-blocking send budget remains five seconds, so transient
flow-control backpressure can outlive a caller without immediately recycling the shared
stream. A caller context governs lane acquisition. After ownership, explicit cancellation is
not transport-failure evidence; a caller deadline may stop waiting while the serialized
write retains the lane until it completes or reaches the transport watchdog. Non-blocking
Mist webhooks do not inherit browser/request disconnects. If an active write
exceeds its transport deadline, Helmsman cancels its owning control stream; the serialized
send lane is released and the normal reconnect loop creates a fresh stream. Node-runtime
cancellation is propagated through lifecycle sends, so replacement and shutdown cannot wait
forever on an unread control connection.
Once shutdown marks the monitor stopped under its node-lifecycle lock, later desired-state
add/remove callbacks are ignored and cannot register new retirement work while shutdown waits.

The controller's initial stream dump has a fixed connect-relative warm-up deadline, so
continuous traffic cannot extend bootstrap indefinitely. Deadline expiry requests one
authoritative full sweep. Until that succeeds, first-seen tail frames remain bootstrap
snapshot data while changes to already observed streams stay actionable; this recovers
warm-up transitions without turning a slow dump tail into targeted-refresh bursts. Full polls and targeted
refreshes carry monotonic observation IDs; the per-stream claim is held through trigger
send so rows cannot publish out of order. A targeted read newer than an in-flight poll is
merged into one stable presence snapshot used by source leases, vanish detection, admitted
runtime reconciliation, and freshness pruning. It suppresses absence only for that stream;
the next authoritative poll confirms or removes it, and boot reconciliation still completes.
Failed targeted refreshes request a full sweep without reserving freshness
or stamping the refresh throttle. Failed full sweeps retry with bounded jittered exponential
backoff; CAS contention waits briefly for the already-running poll instead of being classified
as a Mist failure, and that poll's success completes bootstrap. Stream-end frames rely on the
normal vanish diff rather than requesting an all-stream replay. Successful fallback sweeps are debounced
to one per two seconds, and stream-frame dedup survives socket reconnects so a repeated
initial dump is not emitted as a fresh change set. Socket dedup, refresh-throttle,
pending-name, and observation maps are bounded; authoritative inventories prune reconnect
dedup entries for streams no longer present, but only when those entries are not newer
than the inventory snapshot. Targeted response ordering is assigned at request start and
registered only after a successful response, preventing a slow pre-poll request from
reviving state removed by the newer poll. Observation-cap overflow switches the
accelerator to poll-only mode until a bounded authoritative inventory clears it.

Source leases use a 10-second continuous-missing dwell and admitted runtime streams use
a 20-second continuous-missing dwell after their 30-second minimum age. These are time
windows rather than poll counts, so accelerator sweeps cannot shorten reconciliation.

`helmsman_control_delivery_outcomes_total` uses these bounded pairs:
`durable/{persisted,sent,confirmed,persist_failed}`, `bounded/evicted`, and
`ephemeral/dropped`. Alert on `persist_failed`, `evicted`, and `dropped`.
`helmsman_control_outbox_scan_errors_total.phase` is one of `drain_read`,
`drain_decode`, `quarantine_rename`, `metrics_readdir`, `metrics_stat`,
`quarantine_stat`, or `quarantine_remove`.

Foghorn exposes `foghorn_artifact_deletion_outcomes_total{outcome}` for point
deletion decisions (`applied`, `fenced`, `absent`, `parent_missing`, or
`error`). `fenced` means a newer node-clock placement defeated the deletion;
`absent` is an idempotent replay, while `parent_missing` identifies a signal
for an artifact no longer in the local catalog. Navigator exposes
`navigator_config_seed_apply_ack_outcomes_total{state,outcome}`; `state` is
`applied` or `pending_apply`, and `outcome` is `accepted`, `stale`, `revoked`,
`missing_parent`, `filtered`, or `error`. `stale` means the seed/delivery fence
did not advance; `revoked` means the tenant/cluster authority carries a
revocation tombstone, so a revocation racing an ACK is distinguishable from an
ordinary replay; `missing_parent` means alias authority disappeared before the
ACK acquired its parent lock. Navigator's DNS worker also exposes
`navigator_dns_write_back_cas_misses_total` (in_dns write-backs skipped because
an ACK advanced the row mid-publish — a sustained rate means something keeps
moving rows under the publisher),
`navigator_dns_legacy_continuity_rides_total` (publish-pass occurrences of
versionless pre-upgrade rows retained as continuity members — sustained
non-zero after a rollout means edges never re-ACKed a revision), and
`navigator_dns_legacy_continuity_expired_total` (continuity members demoted by
the 30-day age bound). `filtered` means local or connected authority
excluded a tenant bundle; `error` counts only unresolved tenant outcomes
deferred by an authority failure before the database write. A mixed batch can
increment both without double-counting an outcome. Foghorn exposes
`foghorn_config_seed_apply_ack_outbox_pending`,
`foghorn_config_seed_apply_ack_outbox_oldest_pending_seconds`, and
`foghorn_config_seed_apply_ack_outbox_quarantined`, and
`foghorn_config_seed_apply_ack_outbox_outcomes_total{outcome}`. The bounded
outcomes are `enqueued`, `deduplicated`, `stale`, `enqueue_error`, `delivered`,
`retry`, `retry_error`, `settle_error`, `superseded`, `quarantined`,
`quarantine_error`, and `scan_error`. `stale` is an older seed rejected by the
per-node durable row; `deduplicated` is an equivalent current-seed projection.
Quarantined
rows are retained but are not pending; a future same- or newer-seed result can
repair them. Alert on sustained pending age and on `enqueue_error`,
`retry_error`, `settle_error`, `quarantine_error`, or `scan_error` increases;
the quarantine gauge and `quarantined` outcome require inspection of retained rows.

## Prometheus Metric Wiring Policy

These rules govern how service-side Prometheus metrics are declared, updated, and cleaned up. They apply to every `*_total` counter, `*_seconds` histogram, and gauge exposed on a service's `/metrics` endpoint.

### Pre-initialization of bounded labelsets

For metrics whose label cardinality is bounded and known ahead of time (e.g. `status="ok"|"error"`, fixed operation enums), pre-initialize each expected labelset to zero at service startup so the series appears immediately in `/metrics`:

```go
counter.WithLabelValues("create", "ok").Add(0)
counter.WithLabelValues("create", "error").Add(0)
```

This must ship in the same change that wires the actual increment site — never earlier. Pre-initializing a metric that has no real updater paints a permanent zero series and hides the missing wiring. A declared-but-never-updated metric showing as null in Grafana is a feature: it surfaces unwired code paths.

### Dynamic labelsets

For labels with unbounded or runtime-dependent values (`tenant_id`, `stream_id`, `node_id`, `partition`), do not pre-initialize. Dashboard queries must tolerate cold series with `… or vector(0)` or similar.

### Stale labelset cleanup for gauges

A `GaugeVec` keyed by a dynamic label accumulates dead labelsets forever unless the application explicitly deletes them. Two patterns:

- **Per-cycle truth (poller/reconciler):** Track the set of label values observed in the current cycle. Diff against the previous cycle and call `vec.DeleteLabelValues(...)` for each labelset that disappeared. Helmsman's `emitClientLifecycle` is the reference implementation (`api_sidecar/internal/handlers/poller.go`).
- **Lifecycle-driven:** Pair every `Inc()` with a matching `Dec()` on the corresponding teardown path. The Dec must run on the real lifecycle event (connection close, client retire), not in a resolver `defer` that fires when the resolver returns.

Use the **app-declared labels** of the GaugeVec — never scrape-target labels added by vmagent (`instance`, `node_id`-as-target-label, etc.) which do not exist in the vec.

### When to remove a metric

Remove only when the architecture cannot produce the event. Examples:

- A `kafka_consumer_lag` gauge in a service that has no Kafka consumer.
- A `dns_queries_total` counter in a service that does not run a DNS server.

Do not remove a metric simply because production has not exercised the code path yet.

### Counter vs gauge for connection-pool / system stats

For Go `database/sql` connection-pool stats, register `prometheus.NewGaugeFunc` / `NewCounterFunc` that read `db.Stats()` at scrape time. Do not start a background goroutine + ticker — that introduces sampling drift, a context lifecycle, and a goroutine to leak.

Standard names:

| Metric                                     | Type        | Source                              |
| ------------------------------------------ | ----------- | ----------------------------------- |
| `<service>_db_open_connections`            | GaugeFunc   | `db.Stats().OpenConnections`        |
| `<service>_db_in_use_connections`          | GaugeFunc   | `db.Stats().InUse`                  |
| `<service>_db_idle_connections`            | GaugeFunc   | `db.Stats().Idle`                   |
| `<service>_db_wait_count_total`            | CounterFunc | `db.Stats().WaitCount`              |
| `<service>_db_wait_duration_seconds_total` | CounterFunc | `db.Stats().WaitDuration.Seconds()` |

### gRPC metrics interceptor placement

When a service exposes `<svc>_grpc_requests_total{method,status}` + `<svc>_grpc_request_duration_seconds`, wire it as a `grpc.UnaryServerInterceptor` placed **outermost** in the interceptor chain. Sitting after the auth interceptor hides every `Unauthenticated` / `PermissionDenied` rejection, which is precisely the failure-rate signal we want to see.

Chain order: `GRPCMetricsInterceptor → GRPCLoggingInterceptor → GRPCAuthInterceptor → handler`. The status label is `"ok"` on success, otherwise `status.Code(err).String()` (e.g. `"Unauthenticated"`).

### Naming and double-prefix trap

`pkg/monitoring/metrics.go` `NewCounter` / `NewGauge` / `NewHistogram` already prepend `serviceName + "_"`. Never pass a name that itself begins with the service name — the result is a double-prefixed metric (`<svc>_<svc>_*`).
