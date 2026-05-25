# Metrics Semantic Contract

This document defines the authoritative units and semantics for all metrics in the FrameWorks platform.

## Naming Conventions

| Suffix            | Meaning                                         | Example                              |
| ----------------- | ----------------------------------------------- | ------------------------------------ |
| `_bytes`          | Cumulative byte count                           | `uploaded_bytes`, `downloaded_bytes` |
| `_bps`            | Bits per second (rate)                          | `bandwidthInBps`, `bandwidthOutBps`  |
| `_bytes_per_sec`  | Bytes per second (rate)                         | `up_speed`, `down_speed`             |
| `_gb`             | **GiB** (bytes / 1024³)                         | `egress_gb`, `display_storage_gb`    |
| `_gb_seconds`     | GiB-seconds for time-weighted storage meters    | `storage_gb_seconds_cold`            |
| `_mbps`           | **Mibps** (bps / 1024²) for billing rate fields | `peak_bandwidth_mbps`                |
| `_ms`             | Milliseconds                                    | `stream_buffer_ms`, `latency_ms`     |
| `_pct` or `_rate` | Ratio 0.0-1.0                                   | `packet_loss_rate`, `buffer_health`  |

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
| `buffer_size`                              | ms          | Gauge | Overall buffer in ms (`StreamBufferTrigger.stream_buffer_ms`)                |
| `buffer_health`                            | 0.0-1.0     | Gauge | `buffer_size / max_keepaway_ms` (clamped to 1.0)                             |
| `buffer_state`                             | enum string | Gauge | Buffer state (`FULL`, `EMPTY`, `DRY`, `RECOVER`, …)                          |
| `quality_tier`                             | string      | Gauge | Rich tier label (e.g. `"1080p60 H264 @ 6Mbps"`)                              |
| `has_issues`                               | boolean     | Gauge | Issue flag (Mist + Helmsman derived)                                         |
| `issues_description`                       | string      | Gauge | Human-readable issue summary                                                 |
| `track_count`                              | count       | Gauge | Track count                                                                  |
| `track_metadata`                           | JSON        | Gauge | Serialized typed tracks (includes per-track jitter/buffer/bitrate_bps, etc.) |

**Where packet loss + jitter live**

- Packet loss rate is derived from client QoE rollups (`client_qoe_5m.pkt_loss_rate`).
- Jitter is stored in `stream_health_samples.frame_jitter_ms` (from `StreamBufferTrigger.stream_jitter_ms`) with 5m rollups in `stream_health_5m.avg_frame_jitter_ms` and `stream_health_5m.max_frame_jitter_ms`. Per-track jitter is also available inside `track_metadata`.

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
- **Cumulative fields** (`_bytes`, `_total`): Display as totals, never with `/s`

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
