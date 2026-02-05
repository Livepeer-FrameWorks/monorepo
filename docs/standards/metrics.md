# Metrics Semantic Contract

This document defines the authoritative units and semantics for all metrics in the FrameWorks platform.

## Naming Conventions

| Suffix            | Meaning                                         | Example                              |
| ----------------- | ----------------------------------------------- | ------------------------------------ |
| `_bytes`          | Cumulative byte count                           | `uploaded_bytes`, `downloaded_bytes` |
| `_bps`            | Bits per second (rate)                          | `bandwidthInBps`, `bandwidthOutBps`  |
| `_bytes_per_sec`  | Bytes per second (rate)                         | `up_speed`, `down_speed`             |
| `_gb`             | **GiB** (bytes / 1024³) for billing rollups     | `egress_gb`, `average_storage_gb`    |
| `_mbps`           | **Mibps** (bps / 1024²) for billing rate fields | `peak_bandwidth_mbps`                |
| `_ms`             | Milliseconds                                    | `stream_buffer_ms`, `latency_ms`     |
| `_pct` or `_rate` | Ratio 0.0-1.0                                   | `packet_loss_rate`, `buffer_health`  |

## Data Categories

### 1. Node Metrics (Infrastructure Health)

**Source:** MistServer `/koekjes` JSON → Helmsman poller → ClickHouse `node_metrics_samples`

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

**Source:** MistServer `USER_END` trigger → `viewer_connection_events` → MVs

| Field               | Unit    | Type      | Description                                                       |
| ------------------- | ------- | --------- | ----------------------------------------------------------------- |
| `bytes_transferred` | bytes   | Counter   | Total bytes for session (`max(0, up_bytes) + max(0, down_bytes)`) |
| `session_duration`  | seconds | Counter   | Session duration                                                  |
| `egress_gb`         | GiB     | Aggregate | Sum of `bytes_transferred / (1024³)` (daily rollup)               |
| `viewer_hours`      | hours   | Aggregate | Sum of duration / 3600 (daily rollup)                             |

**Note:** Despite the name `egress_gb`, the current implementation derives it from `bytes_transferred` (up+down). Treat it as “bandwidth GiB” unless/until billing is changed to egress-only.

**Aggregation Pipeline:**

```
USER_END trigger (uploaded/downloaded bytes total)
  → viewer_connection_events.bytes_transferred (ClickHouse)
  → viewer_hours_hourly (MV aggregation)
  → tenant_viewer_daily (MV daily rollup)
  → billing.usage_reports (Kafka)
  → purser.usage_records (PostgreSQL)
```

### 3. Stream Health Metrics (QoE)

**Source:** MistServer `STREAM_BUFFER` trigger **and** Helmsman poller (stream lifecycle updates) → `stream_health_samples`

| Field (ClickHouse `stream_health_samples`) | Unit        | Type  | Description                                                                  |
| ------------------------------------------ | ----------- | ----- | ---------------------------------------------------------------------------- |
| `bitrate`                                  | kbps        | Gauge | Primary video bitrate (`StreamTrack.bitrate_kbps`)                           |
| `fps`                                      | frames/sec  | Gauge | Primary video FPS                                                            |
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

**Source:** `stream_state_current` (real-time snapshots) + rollups (`tenant_viewer_daily`, `client_qoe_5m`) via Periscope Query

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

- Use `USER_END` session data for accurate egress per stream
- Aggregate from `viewer_connection_events` → `stream_analytics_daily`

## MistServer Data Sources

### Node-level JSON (`/koekjes`)

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
| `egress_gb`        | GiB       | "1.2 GiB"        | Display as-is               |

### Rate vs Cumulative Display

- **Rate fields** (`_bps`, `_bytes_per_sec`): Display with `/s` suffix
- **Cumulative fields** (`_bytes`, `_total`): Display as totals, never with `/s`
