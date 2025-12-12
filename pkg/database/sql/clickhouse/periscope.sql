-- ============================================================================
-- PERISCOPE CLICKHOUSE SCHEMA
-- Time-series analytics for streams, viewers, nodes, and clips
-- ============================================================================

CREATE DATABASE IF NOT EXISTS periscope;
USE periscope;

-- ============================================================================
-- EVENT LOGS (high-cardinality, append-only)
-- Captures lifecycle and routing events emitted by the platform
-- ============================================================================

CREATE TABLE IF NOT EXISTS stream_events (
    -- ===== COMMON FIELDS (ALL EVENTS) =====
    event_id UUID,
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,
    stream_id UUID MATERIALIZED toUUIDOrZero(internal_name),
    node_id LowCardinality(String),
    event_type LowCardinality(String),
    status Nullable(String),
    
    -- ===== PUSH EVENTS (push-start, push-end) =====
    push_id Nullable(String),          -- PUSH_END only
    push_target Nullable(String),      -- Both push events
    target_uri_before Nullable(String), -- PUSH_END only
    target_uri_after Nullable(String),  -- PUSH_END only
    push_status Nullable(String),       -- PUSH_END only - JSON string
    log_messages Nullable(String),      -- PUSH_END only - JSON array
    
    -- ===== STREAM LIFECYCLE (stream-buffer, stream-end) =====
    buffer_state Nullable(String),      -- FULL, EMPTY, DRY, RECOVER
    has_issues Nullable(Boolean),
    issues_description Nullable(String),
    track_count Nullable(UInt16),
    quality_tier Nullable(String),
    primary_width Nullable(UInt16),
    primary_height Nullable(UInt16),
    primary_fps Nullable(Float32),
    
    -- Stream end metrics (STREAM_END)
    downloaded_bytes Nullable(UInt64),
    uploaded_bytes Nullable(UInt64),
    total_viewers Nullable(UInt32),
    total_inputs Nullable(UInt16),
    total_outputs Nullable(UInt16),
    viewer_seconds Nullable(UInt64),
    
    -- ===== STREAM INGEST EVENTS (stream-ingest) =====
    stream_key Nullable(String),
    user_id Nullable(String),
    hostname Nullable(String),
    push_url Nullable(String),
    protocol Nullable(String),          -- RTMP, SRT, WebRTC
    
    -- ===== RECORDING EVENTS (recording-end) =====
    file_size Nullable(UInt64),
    duration Nullable(UInt32),
    output_file Nullable(String),
    
    -- ===== BANDWIDTH THRESHOLD EVENTS =====
    current_bytes_per_sec Nullable(UInt64),
    threshold_exceeded Nullable(Boolean),
    threshold_value Nullable(UInt64),
    
    -- ===== GEOGRAPHIC DATA (various events) =====
    latitude Nullable(Float64),
    longitude Nullable(Float64),
    location Nullable(String),                    -- Node location name (push events)
    country_code Nullable(FixedString(2)),        -- ISO country code (viewer/publisher)
    city Nullable(String),                        -- City name (viewer/publisher)
    
    -- ===== FULL EVENT DATA =====
    event_data String                   -- Complete JSON for anything we missed
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, internal_name, timestamp)
TTL timestamp + INTERVAL 90 DAY;

-- Node selection and routing decisions
CREATE TABLE IF NOT EXISTS routing_events (
    -- ===== TIME & TENANT =====
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,               -- Stream UUID (normalized, no live+/vod+ prefix)

    -- ===== ROUTING DECISION =====
    selected_node LowCardinality(String),
    status LowCardinality(String),
    details String,
    score Int64,                        -- Load balancing score

    -- ===== CLIENT LOCATION =====
    client_ip String,
    client_country FixedString(2),
    client_latitude Float64,
    client_longitude Float64,
    client_bucket_h3 Nullable(UInt64),
    client_bucket_res Nullable(UInt8),

    -- ===== NODE LOCATION =====
    node_latitude Float64,
    node_longitude Float64,
    node_name LowCardinality(String),
    node_bucket_h3 Nullable(UInt64),
    node_bucket_res Nullable(UInt8),
    selected_node_id Nullable(String),
    routing_distance_km Nullable(Float64)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 90 DAY;

-- Viewer connection lifecycle events
CREATE TABLE IF NOT EXISTS connection_events (
    -- ===== EVENT IDENTIFICATION =====
    event_id UUID,
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,
    stream_id UUID MATERIALIZED toUUIDOrZero(internal_name),
    
    -- ===== CONNECTION DETAILS =====
    session_id String,
    connection_addr String,
    connector LowCardinality(String),   -- HLS, DASH, WebRTC, etc.
    node_id LowCardinality(String),
    request_url Nullable(String),       -- From USER_NEW webhook
    
    -- ===== GEOGRAPHIC DATA =====
    country_code FixedString(2),
    city LowCardinality(String),
    latitude Float64,
    longitude Float64,
    client_bucket_h3 Nullable(UInt64),
    client_bucket_res Nullable(UInt8),
    node_bucket_h3 Nullable(UInt64),
    node_bucket_res Nullable(UInt8),
    
    -- ===== SESSION METRICS =====
    event_type LowCardinality(String),  -- connect, disconnect, error
    session_duration UInt32,
    bytes_transferred UInt64
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 90 DAY;

-- Node health and performance samples
CREATE TABLE IF NOT EXISTS node_metrics (
    -- ===== TIME & NODE IDENTIFICATION =====
    timestamp DateTime,
    tenant_id UUID,
    node_id LowCardinality(String),
    
    -- ===== RESOURCE METRICS =====
    cpu_usage Float32,
    ram_max UInt64,
    ram_current UInt64,
    shm_total_bytes UInt64,
    shm_used_bytes UInt64,
    disk_total_bytes UInt64,
    disk_used_bytes UInt64,
    
    -- ===== NETWORK METRICS =====
    bandwidth_in UInt64,
    bandwidth_out UInt64,
    up_speed UInt64,
    down_speed UInt64,
    connections_current UInt32,
    stream_count UInt32,
    
    -- ===== HEALTH METRICS =====
    is_healthy UInt8,
    
    -- ===== GEOGRAPHIC DATA =====
    latitude Float64,
    longitude Float64,
    
    -- ===== ADDITIONAL METADATA =====
    metadata JSON
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, node_id)
TTL timestamp + INTERVAL 30 DAY;

-- Stream health metrics (encoder-level)
CREATE TABLE IF NOT EXISTS stream_health_metrics (
    -- ===== TIME & STREAM IDENTIFICATION =====
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,
    stream_id UUID MATERIALIZED toUUIDOrZero(internal_name),
    node_id LowCardinality(String),

    -- ===== ENCODING METRICS (Nullable - may not exist for audio-only streams) =====
    bitrate Nullable(UInt32),
    fps Nullable(Float32),
    gop_size Nullable(UInt16),
    width Nullable(UInt16),
    height Nullable(UInt16),

    -- ===== BUFFER HEALTH =====
    buffer_size Nullable(UInt32),
    buffer_health Nullable(Float32),
    buffer_state LowCardinality(String),

    -- ===== CODEC & QUALITY =====
    codec Nullable(String),
    quality_tier Nullable(String),      -- Rich quality string e.g. "1080p60 H264 @ 6Mbps"
    track_metadata JSON,

    -- ===== FRAME TIMING (Nullable - raw from MistServer track keys) =====
    frame_ms_max Nullable(Float32),
    frame_ms_min Nullable(Float32),
    frames_max Nullable(UInt32),
    frames_min Nullable(UInt32),
    keyframe_ms_max Nullable(Float32),
    keyframe_ms_min Nullable(Float32),

    -- ===== HEALTH STATUS =====
    issues_description Nullable(String),
    has_issues Nullable(UInt8),
    track_count Nullable(UInt16),

    -- ===== AUDIO METRICS (Nullable - may not exist for video-only streams) =====
    audio_channels Nullable(UInt8),
    audio_sample_rate Nullable(UInt32),
    audio_codec Nullable(String),
    audio_bitrate Nullable(UInt32)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 30 DAY;

-- Client-side QoE/transport metrics
CREATE TABLE IF NOT EXISTS client_metrics (
    -- ===== TIME & STREAM IDENTIFICATION =====
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,
    stream_id UUID MATERIALIZED toUUIDOrZero(internal_name),
    session_id String,
    node_id LowCardinality(String),
    
    -- ===== CONNECTION DETAILS =====
    protocol LowCardinality(String),    -- HLS, DASH, WebRTC
    host String,
    connection_time Float32,
    position Nullable(Float32),         -- Playback position
    
    -- ===== BANDWIDTH METRICS =====
    bandwidth_in UInt64,
    bandwidth_out UInt64,
    bytes_downloaded UInt64,
    bytes_uploaded UInt64,
    
    -- ===== NETWORK PERFORMANCE =====
    packets_sent UInt64,
    packets_lost UInt64,
    packets_retransmitted UInt64,
    connection_quality Nullable(Float32)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp)
TTL timestamp + INTERVAL 90 DAY;

-- Aggregated client metrics (5m)
CREATE TABLE IF NOT EXISTS client_metrics_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    active_sessions UInt32,
    avg_bw_in Float64,
    avg_bw_out Float64,
    avg_connection_time Float32,
    pkt_loss_rate Nullable(Float32),
    avg_connection_quality Nullable(Float32)
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp_5m)
ORDER BY (timestamp_5m, tenant_id, internal_name, node_id)
TTL timestamp_5m + INTERVAL 180 DAY;

-- Aggregation MV → client_metrics_5m
CREATE MATERIALIZED VIEW IF NOT EXISTS client_metrics_5m_mv TO client_metrics_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    internal_name,
    node_id,
    count(DISTINCT session_id) as active_sessions,
    avg(bandwidth_in) AS avg_bw_in,
    avg(bandwidth_out) AS avg_bw_out,
    avg(connection_time) AS avg_connection_time,
    if(sum(packets_sent) > 0, sum(packets_lost) / sum(packets_sent), NULL) AS pkt_loss_rate,
    avg(connection_quality) AS avg_connection_quality
FROM client_metrics
GROUP BY timestamp_5m, tenant_id, internal_name, node_id;

-- Track inventory snapshots per stream
CREATE TABLE IF NOT EXISTS track_list_events (
    -- ===== TIME & STREAM IDENTIFICATION =====
    timestamp DateTime,
    event_id UUID,
    tenant_id UUID,
    internal_name String,
    node_id LowCardinality(String),

    -- ===== TRACK METADATA =====
    track_list String,                  -- Complete track list JSON
    track_count UInt16,
    video_track_count UInt16,
    audio_track_count UInt16,

    -- ===== PRIMARY VIDEO TRACK (Nullable - may not exist for audio-only streams) =====
    primary_width Nullable(UInt16),
    primary_height Nullable(UInt16),
    primary_fps Nullable(Float32),
    primary_video_codec Nullable(String),
    primary_video_bitrate Nullable(UInt32),
    quality_tier Nullable(String),

    -- ===== PRIMARY AUDIO TRACK (Nullable - may not exist for video-only streams) =====
    primary_audio_channels Nullable(UInt8),
    primary_audio_sample_rate Nullable(UInt32),
    primary_audio_codec Nullable(String),
    primary_audio_bitrate Nullable(UInt32)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 90 DAY;

-- Hourly connection aggregates for bandwidth/session charts
CREATE TABLE IF NOT EXISTS stream_connection_hourly (
    hour DateTime,
    tenant_id UUID,
    internal_name String,
    total_bytes AggregateFunction(sum, UInt64),
    unique_viewers AggregateFunction(uniq, String),
    total_sessions AggregateFunction(count, UInt8)
) ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, internal_name)
TTL hour + INTERVAL 365 DAY;

-- Aggregation MV → stream_connection_hourly
CREATE MATERIALIZED VIEW IF NOT EXISTS stream_connection_hourly_mv TO stream_connection_hourly AS
SELECT
    toStartOfHour(timestamp) as hour,
    tenant_id,
    internal_name,
    sumState(bytes_transferred) as total_bytes,
    uniqState(session_id) as unique_viewers,
    countState() as total_sessions
FROM connection_events
GROUP BY hour, tenant_id, internal_name;

-- 5-minute node performance rollups
CREATE TABLE IF NOT EXISTS node_performance_5m (
    timestamp_5m DateTime,
    node_id LowCardinality(String),
    avg_cpu Float32,
    max_cpu Float32,
    avg_memory Float32,
    max_memory Float32,
    total_bandwidth UInt64,
    avg_streams Float32,
    max_streams UInt32
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp_5m)
ORDER BY (timestamp_5m, node_id)
TTL timestamp_5m + INTERVAL 180 DAY;

-- Aggregation MV → node_performance_5m
CREATE MATERIALIZED VIEW IF NOT EXISTS node_performance_5m_mv TO node_performance_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) as timestamp_5m,
    node_id,
    avg(cpu_usage) as avg_cpu,
    max(cpu_usage) as max_cpu,
    avg(if(ram_max > 0, ram_current / ram_max * 100, 0)) as avg_memory,
    max(if(ram_max > 0, ram_current / ram_max * 100, 0)) as max_memory,
    sum(bandwidth_in + bandwidth_out) as total_bandwidth,
    avg(stream_count) as avg_streams,
    max(stream_count) as max_streams
FROM node_metrics
GROUP BY timestamp_5m, node_id;

-- 1-hour node performance rollups
CREATE TABLE IF NOT EXISTS node_metrics_1h (
    timestamp_1h DateTime,
    node_id LowCardinality(String),
    avg_cpu Float32,
    peak_cpu Float32,
    avg_memory Float32,
    peak_memory Float32,
    avg_disk Float32,
    peak_disk Float32,
    avg_shm Float32,
    peak_shm Float32,
    total_bandwidth_in UInt64,
    total_bandwidth_out UInt64,
    was_healthy UInt8
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp_1h)
ORDER BY (timestamp_1h, node_id)
TTL timestamp_1h + INTERVAL 365 DAY;

-- Aggregation MV → node_metrics_1h
-- Note: bandwidth_in/out are cumulative counters, so we compute delta (max - min) per hour
CREATE MATERIALIZED VIEW IF NOT EXISTS node_metrics_1h_mv TO node_metrics_1h AS
SELECT
    toStartOfHour(timestamp) AS timestamp_1h,
    node_id,
    avg(cpu_usage) AS avg_cpu,
    max(cpu_usage) AS peak_cpu,
    avg(if(ram_max > 0, ram_current / ram_max * 100, 0)) AS avg_memory,
    max(if(ram_max > 0, ram_current / ram_max * 100, 0)) AS peak_memory,
    avg(if(disk_total_bytes > 0, disk_used_bytes / disk_total_bytes * 100, 0)) AS avg_disk,
    max(if(disk_total_bytes > 0, disk_used_bytes / disk_total_bytes * 100, 0)) AS peak_disk,
    avg(if(shm_total_bytes > 0, shm_used_bytes / shm_total_bytes * 100, 0)) AS avg_shm,
    max(if(shm_total_bytes > 0, shm_used_bytes / shm_total_bytes * 100, 0)) AS peak_shm,
    max(bandwidth_in) - min(bandwidth_in) AS total_bandwidth_in,
    max(bandwidth_out) - min(bandwidth_out) AS total_bandwidth_out,
    if(avg(is_healthy) >= 0.5, 1, 0) AS was_healthy
FROM node_metrics
GROUP BY timestamp_1h, node_id;

-- Daily connection/bandwidth metrics per tenant
CREATE TABLE IF NOT EXISTS tenant_connection_daily (
    day Date,
    tenant_id UUID,
    total_bytes UInt64,
    unique_sessions UInt32,
    total_connections UInt32
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id)
TTL day + INTERVAL 730 DAY;

-- Aggregation MV → tenant_connection_daily
CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_connection_daily_mv TO tenant_connection_daily AS
SELECT
    toDate(timestamp) as day,
    tenant_id,
    sum(bytes_transferred) as total_bytes,
    uniq(session_id) as unique_sessions,
    count() as total_connections
FROM connection_events
GROUP BY day, tenant_id;

-- 5-minute stream health rollups
CREATE TABLE IF NOT EXISTS stream_health_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    rebuffer_count UInt32,
    issue_count UInt32,
    sample_issues Nullable(String),
    avg_bitrate Float32,
    avg_fps Float32,
    buffer_dry_count UInt32,
    quality_tier LowCardinality(String)
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp_5m)
ORDER BY (timestamp_5m, tenant_id, internal_name, node_id)
TTL timestamp_5m + INTERVAL 180 DAY;

-- Aggregation MV → stream_health_5m
CREATE MATERIALIZED VIEW IF NOT EXISTS stream_health_5m_mv TO stream_health_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    internal_name,
    node_id,
    countIf(buffer_state = 'DRY') AS rebuffer_count,
    countIf(has_issues = 1) AS issue_count,
    any(issues_description) AS sample_issues,
    avg(bitrate) AS avg_bitrate,
    avg(fps) AS avg_fps,
    countIf(buffer_state = 'DRY') AS buffer_dry_count,
    argMax(
        if(height >= 1080, '1080p+', if(height >= 720, '720p', if(height >= 480, '480p', 'SD'))), timestamp
    ) AS quality_tier
FROM stream_health_metrics
GROUP BY timestamp_5m, tenant_id, internal_name, node_id;

-- Rebuffering events derived from health samples
CREATE TABLE IF NOT EXISTS rebuffering_events (
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    buffer_state LowCardinality(String),
    prev_state LowCardinality(String),
    rebuffer_start UInt8,
    rebuffer_end UInt8
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 90 DAY;

-- Derivation MV → rebuffering_events
CREATE MATERIALIZED VIEW IF NOT EXISTS rebuffering_events_mv TO rebuffering_events AS
SELECT
    timestamp,
    tenant_id,
    internal_name,
    node_id,
    buffer_state,
    lagInFrame(buffer_state) OVER (PARTITION BY tenant_id, internal_name ORDER BY timestamp) AS prev_state,
    if(buffer_state = 'DRY' AND prev_state IN ('FULL', 'RECOVER'), 1, 0) AS rebuffer_start,
    if(buffer_state = 'RECOVER' AND prev_state = 'DRY', 1, 0) AS rebuffer_end
FROM stream_health_metrics
WHERE buffer_state IN ('FULL', 'DRY', 'RECOVER');

-- Daily quality tier distribution
CREATE TABLE IF NOT EXISTS quality_tier_daily (
    day Date,
    tenant_id UUID,
    internal_name String,
    tier_1080p_minutes UInt32,
    tier_720p_minutes UInt32,
    tier_480p_minutes UInt32,
    tier_sd_minutes UInt32,
    primary_tier LowCardinality(String),
    codec_h264_minutes UInt32,
    codec_h265_minutes UInt32,
    avg_bitrate UInt32,
    avg_fps Float32
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id, internal_name)
TTL day + INTERVAL 730 DAY;

-- Aggregation MV → quality_tier_daily
CREATE MATERIALIZED VIEW IF NOT EXISTS quality_tier_daily_mv TO quality_tier_daily AS
SELECT
    toDate(timestamp) as day,
    tenant_id,
    internal_name,
    countIf(primary_height >= 1080) * 5 AS tier_1080p_minutes,
    countIf(primary_height >= 720 AND primary_height < 1080) * 5 AS tier_720p_minutes,
    countIf(primary_height >= 480 AND primary_height < 720) * 5 AS tier_480p_minutes,
    countIf(primary_height < 480) * 5 AS tier_sd_minutes,
    argMax(quality_tier, timestamp) AS primary_tier,
    countIf(primary_video_codec LIKE '%264%') * 5 AS codec_h264_minutes,
    countIf(primary_video_codec LIKE '%265%' OR primary_video_codec LIKE '%HEVC%') * 5 AS codec_h265_minutes,
    avg(primary_video_bitrate) AS avg_bitrate,
    avg(primary_fps) AS avg_fps
FROM track_list_events
WHERE track_count > 0
GROUP BY day, tenant_id, internal_name;

-- Clip orchestration events for visibility
CREATE TABLE IF NOT EXISTS clip_events (
    -- ===== TIME & STREAM IDENTIFICATION =====
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,
    request_id String,
    
    -- ===== CLIP PROCESSING STAGE =====
    stage LowCardinality(String),       -- requested, processing, completed, failed
    content_type LowCardinality(String), -- 'clip' or 'dvr'
    
    -- ===== TIME RANGE =====
    start_unix Nullable(Int64),         -- StartedAt from protobuf
    stop_unix Nullable(Int64),          -- CompletedAt from protobuf
    
    -- ===== NODE ROUTING =====
    ingest_node_id Nullable(String),    -- NodeId from protobuf
    
    -- ===== PROCESSING STATUS =====
    percent Nullable(UInt32),           -- ProgressPercent from protobuf
    message Nullable(String),           -- Error from protobuf
    
    -- ===== OUTPUT DETAILS =====
    file_path Nullable(String),         -- FilePath from protobuf
    s3_url Nullable(String),            -- S3Url from protobuf
    size_bytes Nullable(UInt64)         -- SizeBytes from protobuf
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name, request_id)
TTL timestamp + INTERVAL 90 DAY;

-- Storage usage snapshots (for accurate billing)
CREATE TABLE IF NOT EXISTS storage_snapshots (
    timestamp DateTime,
    tenant_id UUID,
    node_id LowCardinality(String),
    
    -- ===== USAGE METRICS =====
    total_bytes UInt64,
    file_count UInt32,
    
    -- ===== BREAKDOWN =====
    dvr_bytes UInt64,
    clip_bytes UInt64,
    recording_bytes UInt64
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, tenant_id, node_id)
TTL timestamp + INTERVAL 90 DAY;

-- Hourly clip pipeline counts
CREATE TABLE IF NOT EXISTS clip_events_1h (
    hour DateTime,
    tenant_id UUID,
    internal_name String,
    stage LowCardinality(String),
    count UInt64
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, internal_name, stage)
TTL hour + INTERVAL 365 DAY;

-- Aggregation MV → clip_events_1h
CREATE MATERIALIZED VIEW IF NOT EXISTS clip_events_1h_mv TO clip_events_1h AS
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    internal_name,
    stage,
    count() AS count
FROM clip_events
GROUP BY hour, tenant_id, internal_name, stage;

-- ============================================================================
-- LIVE STATE TABLES (Real-time Current State)
-- Populated directly from Foghorn's state snapshot events
-- Uses ReplacingMergeTree - latest row per key wins
-- ============================================================================

-- Current stream state from StreamLifecycleUpdate events
-- This is THE source of truth for stream status - simple SELECT, no aggregation
CREATE TABLE IF NOT EXISTS live_streams (
    tenant_id UUID,
    internal_name String,
    node_id LowCardinality(String),

    -- ===== STATUS (from StreamLifecycleUpdate) =====
    status LowCardinality(String),           -- 'live', 'offline'
    buffer_state LowCardinality(String),     -- FULL, EMPTY, DRY, RECOVER

    -- ===== VIEWERS (FROM FOGHORN - already aggregated!) =====
    current_viewers UInt32,
    total_inputs UInt16,

    -- ===== BANDWIDTH =====
    uploaded_bytes UInt64,
    downloaded_bytes UInt64,
    viewer_seconds UInt64,

    -- ===== HEALTH & QUALITY =====
    has_issues Nullable(UInt8),
    issues_description Nullable(String),
    track_count Nullable(UInt16),
    quality_tier Nullable(String),
    primary_width Nullable(UInt16),
    primary_height Nullable(UInt16),
    primary_fps Nullable(Float32),
    primary_codec Nullable(String),
    primary_bitrate Nullable(UInt32),

    -- ===== PACKET STATISTICS (from MistServer, per-stream totals) =====
    packets_sent Nullable(UInt64),
    packets_lost Nullable(UInt64),

    -- ===== TIMING =====
    started_at Nullable(DateTime),
    updated_at DateTime

) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, internal_name);

-- Current node state from NodeLifecycleUpdate events
-- Simple SELECT for node health/resources
CREATE TABLE IF NOT EXISTS live_nodes (
    tenant_id UUID,
    node_id String,

    -- ===== RESOURCES =====
    cpu_percent Float32,
    ram_used_bytes UInt64,
    ram_total_bytes UInt64,
    disk_used_bytes UInt64,
    disk_total_bytes UInt64,

    -- ===== NETWORK =====
    up_speed UInt64,
    down_speed UInt64,

    -- ===== STREAMS =====
    active_streams UInt32,

    -- ===== HEALTH =====
    is_healthy UInt8,

    -- ===== GEO =====
    latitude Float64,
    longitude Float64,
    location String,

    -- ===== OPERATIONAL METADATA =====
    metadata JSON,

    updated_at DateTime


) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, node_id);

-- Current artifact state (clips/DVR in progress)
-- Enables "what's the status of clip X?" queries without scanning clip_events
CREATE TABLE IF NOT EXISTS live_artifacts (
    tenant_id UUID,
    request_id String,                       -- clip_hash or dvr_hash
    internal_name String,                    -- source stream

    -- ===== ARTIFACT TYPE =====
    content_type LowCardinality(String),     -- 'clip' or 'dvr'

    -- ===== PROCESSING STATE =====
    stage LowCardinality(String),            -- requested, queued, processing, completed, failed, deleted
    progress_percent UInt8,
    error_message Nullable(String),

    -- ===== TIMING =====
    requested_at DateTime,
    started_at Nullable(DateTime),
    completed_at Nullable(DateTime),

    -- ===== TIME RANGE (clip boundaries) =====
    clip_start_unix Nullable(Int64),
    clip_stop_unix Nullable(Int64),

    -- ===== DVR SPECIFIC =====
    segment_count Nullable(UInt32),
    manifest_path Nullable(String),

    -- ===== OUTPUT =====
    file_path Nullable(String),
    s3_url Nullable(String),
    size_bytes Nullable(UInt64),

    -- ===== NODE =====
    processing_node_id Nullable(String),

    updated_at DateTime
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, request_id);

-- ============================================================================
-- VIEWER SESSION TRACKING (for real-time current_viewers)
-- Tracks active viewer sessions for accurate real-time counts
-- ============================================================================

-- Active viewer sessions (connect without disconnect)
-- Used to calculate current_viewers at query time
-- Uses AggregatingMergeTree with SimpleAggregateFunction to properly merge connect/disconnect:
-- - connected_at uses max() so real timestamp (from connect) wins over epoch (from disconnect)
-- - disconnected_at uses max() so actual disconnect time wins over NULL (from connect)
-- - bytes/duration use max() to get final values from disconnect event
-- NOTE: Migration from old schema requires DROP + CREATE (schema change)
CREATE TABLE IF NOT EXISTS viewer_sessions (
    tenant_id UUID,
    internal_name LowCardinality(String),
    session_id String,

    -- ===== SESSION STATE =====
    -- max() ensures: connect's real timestamp > disconnect's epoch(0)
    connected_at SimpleAggregateFunction(max, DateTime),
    -- max() ensures: disconnect's timestamp > connect's NULL (NULL < any value)
    disconnected_at SimpleAggregateFunction(max, Nullable(DateTime)),

    -- ===== CONNECTION DETAILS (use any - same for connect/disconnect) =====
    node_id SimpleAggregateFunction(any, LowCardinality(String)),
    connector SimpleAggregateFunction(any, LowCardinality(String)),

    -- ===== GEOGRAPHIC DATA (use any - same for connect/disconnect) =====
    country_code SimpleAggregateFunction(any, FixedString(2)),
    city SimpleAggregateFunction(any, LowCardinality(String)),
    latitude SimpleAggregateFunction(any, Float64),
    longitude SimpleAggregateFunction(any, Float64),

    -- ===== METRICS (use max - disconnect has final values) =====
    bytes_transferred SimpleAggregateFunction(max, UInt64),
    session_duration SimpleAggregateFunction(max, UInt32),

    last_updated SimpleAggregateFunction(max, DateTime)
) ENGINE = AggregatingMergeTree()
ORDER BY (tenant_id, session_id);

-- Materialized view to track viewer connects
CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_sessions_connect_mv TO viewer_sessions AS
SELECT
    tenant_id,
    internal_name,
    session_id,
    timestamp AS connected_at,
    CAST(NULL AS Nullable(DateTime)) AS disconnected_at,
    node_id,
    connector,
    country_code,
    city,
    latitude,
    longitude,
    bytes_transferred,
    session_duration,
    timestamp AS last_updated
FROM connection_events
WHERE event_type = 'connect' AND session_id != '';

-- Materialized view to track viewer disconnects
-- Sets connected_at to epoch(0) which will be overridden by connect's real value via max()
CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_sessions_disconnect_mv TO viewer_sessions AS
SELECT
    tenant_id,
    internal_name,
    session_id,
    toDateTime(0) AS connected_at,           -- max() ensures connect's real value wins
    timestamp AS disconnected_at,
    node_id,
    connector,
    country_code,
    city,
    latitude,
    longitude,
    bytes_transferred,
    session_duration,
    timestamp AS last_updated
FROM connection_events
WHERE event_type = 'disconnect' AND session_id != '';

-- ============================================================================
-- BILLING AGGREGATION TABLES (For accurate viewer_hours and geo breakdown)
-- These replace the previously non-existent tenant_viewer_daily and viewer_geo_hourly
-- ============================================================================

-- Hourly viewer aggregates by country (foundation for billing + geo)
-- Uses AggregatingMergeTree with State functions for efficient multi-level rollups
CREATE TABLE IF NOT EXISTS viewer_hours_hourly (
    hour DateTime,
    tenant_id UUID,
    internal_name String,
    country_code FixedString(2),
    unique_viewers AggregateFunction(uniq, String),
    total_session_seconds AggregateFunction(sum, UInt64),
    total_bytes AggregateFunction(sum, UInt64)
) ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, internal_name, country_code)
TTL hour + INTERVAL 365 DAY;

-- Aggregation MV → viewer_hours_hourly
-- Only counts complete sessions (disconnect events have accurate session_duration)
CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_hours_hourly_mv TO viewer_hours_hourly AS
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    internal_name,
    country_code,
    uniqState(session_id) AS unique_viewers,
    sumState(toUInt64(session_duration)) AS total_session_seconds,
    sumState(bytes_transferred) AS total_bytes
FROM connection_events
WHERE event_type = 'disconnect'
GROUP BY hour, tenant_id, internal_name, country_code;

-- Daily tenant rollup for billing (viewer_hours per tenant per day)
-- Used by billing.go for viewer_hours calculation
CREATE TABLE IF NOT EXISTS tenant_viewer_daily (
    day Date,
    tenant_id UUID,
    viewer_hours Float64,
    unique_viewers UInt32,
    total_sessions UInt32,
    egress_gb Float64
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id)
TTL day + INTERVAL 730 DAY;

-- Aggregation MV → tenant_viewer_daily (rolls up from viewer_hours_hourly)
CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_viewer_daily_mv TO tenant_viewer_daily AS
SELECT
    toDate(hour) AS day,
    tenant_id,
    sumMerge(total_session_seconds) / 3600.0 AS viewer_hours,
    toUInt32(uniqMerge(unique_viewers)) AS unique_viewers,
    toUInt32(count()) AS total_sessions,
    sumMerge(total_bytes) / (1024*1024*1024) AS egress_gb
FROM viewer_hours_hourly
GROUP BY day, tenant_id;

-- Hourly geographic breakdown (viewer count + hours + egress per country)
-- Used by billing.go for geo_breakdown in email summaries
CREATE TABLE IF NOT EXISTS viewer_geo_hourly (
    hour DateTime,
    tenant_id UUID,
    country_code FixedString(2),
    viewer_count UInt32,
    viewer_hours Float64,
    egress_gb Float64
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, country_code)
TTL hour + INTERVAL 365 DAY;

-- Aggregation MV → viewer_geo_hourly (rolls up from viewer_hours_hourly)
CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_geo_hourly_mv TO viewer_geo_hourly AS
SELECT
    hour,
    tenant_id,
    country_code,
    toUInt32(uniqMerge(unique_viewers)) AS viewer_count,
    sumMerge(total_session_seconds) / 3600.0 AS viewer_hours,
    sumMerge(total_bytes) / (1024*1024*1024) AS egress_gb
FROM viewer_hours_hourly
GROUP BY hour, tenant_id, country_code;

-- ============================================================================
-- ANALYTICS ROLLUP TABLES (For Dashboard Overview)
-- Pre-computed daily aggregates for efficient dashboard queries
-- ============================================================================

-- Daily tenant-level analytics rollup
-- Used by GetPlatformOverview for tenant dashboard cards
CREATE TABLE IF NOT EXISTS tenant_analytics_daily (
    day Date,
    tenant_id UUID,
    -- Stream counts (from connection_events - streams with viewer activity)
    total_streams UInt32,
    -- Viewer metrics
    total_views UInt64,           -- COUNT of connect events
    unique_viewers UInt32,        -- DISTINCT session_ids
    -- Bandwidth
    egress_bytes UInt64
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id)
TTL day + INTERVAL 730 DAY;

-- Aggregation MV → tenant_analytics_daily (from connection_events)
CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_analytics_daily_mv TO tenant_analytics_daily AS
SELECT
    toDate(timestamp) AS day,
    tenant_id,
    uniq(internal_name) AS total_streams,
    countIf(event_type = 'connect') AS total_views,
    uniq(session_id) AS unique_viewers,
    sum(bytes_transferred) AS egress_bytes
FROM connection_events
GROUP BY day, tenant_id;

-- Daily stream-level analytics rollup
-- Used by GetStreamAnalytics for per-stream metrics (batch query)
CREATE TABLE IF NOT EXISTS stream_analytics_daily (
    day Date,
    tenant_id UUID,
    internal_name String,
    -- Viewer metrics
    total_views UInt64,
    unique_viewers UInt32,
    -- Geographic diversity
    unique_countries UInt16,
    unique_cities UInt16,
    -- Bandwidth
    egress_bytes UInt64
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id, internal_name)
TTL day + INTERVAL 730 DAY;

-- Aggregation MV → stream_analytics_daily (from connection_events)
CREATE MATERIALIZED VIEW IF NOT EXISTS stream_analytics_daily_mv TO stream_analytics_daily AS
SELECT
    toDate(timestamp) AS day,
    tenant_id,
    internal_name,
    countIf(event_type = 'connect') AS total_views,
    uniq(session_id) AS unique_viewers,
    uniq(country_code) AS unique_countries,
    uniq(city) AS unique_cities,
    sum(bytes_transferred) AS egress_bytes
FROM connection_events
GROUP BY day, tenant_id, internal_name;
