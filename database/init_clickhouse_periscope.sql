-- ============================================================================
-- PERISCOPE CLICKHOUSE SCHEMA
-- Time-series analytics for streams, viewers, nodes, and clips
-- ============================================================================

CREATE DATABASE IF NOT EXISTS frameworks;
USE frameworks;

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
    health_score Nullable(Float32),
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
    location Nullable(String),
    
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
    stream_name String,
    
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
    
    -- ===== NODE LOCATION =====
    node_latitude Float64,
    node_longitude Float64,
    node_name LowCardinality(String)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, stream_name)
TTL timestamp + INTERVAL 90 DAY;

-- Real-time viewer metrics (per-sample)
CREATE TABLE IF NOT EXISTS viewer_metrics (
    -- ===== TIME & TENANT PARTITIONING =====
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,
    stream_id UUID MATERIALIZED toUUIDOrZero(internal_name),
    
    -- ===== CORE METRICS =====
    viewer_count UInt32,
    connection_type LowCardinality(String),
    node_id LowCardinality(String),
    
    -- ===== GEOGRAPHIC DATA =====
    country_code FixedString(2),
    city LowCardinality(String),
    latitude Float64,
    longitude Float64,
    
    -- ===== TECHNICAL DATA =====
    connection_quality Float32,
    buffer_health Float32
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 90 DAY;

-- 5-minute viewer aggregates (rollup table)
CREATE TABLE IF NOT EXISTS viewer_metrics_5m (
    -- ===== ROLLUP INTERVAL & KEYS =====
    timestamp_5m DateTime,
    tenant_id UUID,
    internal_name String,
    node_id LowCardinality(String),

    -- ===== AGGREGATED METRICS =====
    peak_viewers UInt32,
    avg_viewers Float32,
    unique_countries UInt32,
    unique_cities UInt32,
    avg_connection_quality Float32,
    avg_buffer_health Float32
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp_5m), tenant_id)
ORDER BY (timestamp_5m, tenant_id, internal_name, node_id);

-- Aggregation MV → viewer_metrics_5m
CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_metrics_5m_mv TO viewer_metrics_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    internal_name,
    node_id,
    max(viewer_count) AS peak_viewers,
    avg(viewer_count) AS avg_viewers,
    uniq(country_code) AS unique_countries,
    uniq(city) AS unique_cities,
    avg(connection_quality) AS avg_connection_quality,
    avg(buffer_health) AS avg_buffer_health
FROM viewer_metrics
GROUP BY timestamp_5m, tenant_id, internal_name, node_id;

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
    memory_usage Float32,
    disk_usage Float32,
    ram_max UInt64,
    ram_current UInt64,
    
    -- ===== NETWORK METRICS =====
    bandwidth_in UInt64,
    bandwidth_out UInt64,
    up_speed UInt64,
    down_speed UInt64,
    connections_current UInt32,
    stream_count UInt32,
    
    -- ===== HEALTH METRICS =====
    health_score Float32,
    is_healthy UInt8,
    
    -- ===== GEOGRAPHIC DATA =====
    latitude Float64,
    longitude Float64,
    
    -- ===== ADDITIONAL METADATA =====
    tags Array(String),
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
    
    -- ===== ENCODING METRICS =====
    bitrate UInt32,
    fps Float32,
    gop_size UInt16,
    width UInt16,
    height UInt16,
    
    -- ===== BUFFER HEALTH =====
    buffer_size UInt32,
    buffer_used UInt32,
    buffer_health Float32,
    buffer_state LowCardinality(String),
    
    -- ===== NETWORK PERFORMANCE =====
    packets_sent UInt64,
    packets_lost UInt64,
    packets_retransmitted UInt64,
    
    -- ===== CODEC & PROFILE =====
    codec LowCardinality(String),
    profile LowCardinality(String),
    track_metadata JSON,
    
    -- ===== FRAME TIMING =====
    frame_jitter_ms Float32,
    keyframe_stability_ms Float32,
    frame_ms_max Float32,
    frame_ms_min Float32,
    frames_max UInt32,
    frames_min UInt32,
    keyframe_ms_max Float32,
    keyframe_ms_min Float32,
    
    -- ===== HEALTH STATUS =====
    issues_description String,
    has_issues UInt8,
    health_score Float32,
    track_count UInt16,
    
    -- ===== AUDIO METRICS =====
    audio_channels UInt8,
    audio_sample_rate UInt32,
    audio_codec LowCardinality(String),
    audio_bitrate UInt32
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
ORDER BY (timestamp_5m, tenant_id, internal_name, node_id);

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
    
    -- ===== PRIMARY VIDEO TRACK =====
    primary_width UInt16,
    primary_height UInt16,
    primary_fps Float32,
    primary_video_codec LowCardinality(String),
    primary_video_bitrate UInt32,
    quality_tier LowCardinality(String),
    
    -- ===== PRIMARY AUDIO TRACK =====
    primary_audio_channels UInt8,
    primary_audio_sample_rate UInt32,
    primary_audio_codec LowCardinality(String),
    primary_audio_bitrate UInt32
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 90 DAY;

-- Track change diffs per stream
CREATE TABLE IF NOT EXISTS track_change_events (
    -- ===== TIME & STREAM IDENTIFICATION =====
    timestamp DateTime,
    event_id UUID,
    tenant_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    
    -- ===== CHANGE METADATA =====
    change_type LowCardinality(String), -- add, remove, modify
    
    -- ===== TRACK COMPARISON =====
    previous_tracks String,             -- Previous track list JSON
    new_tracks String,                  -- New track list JSON
    
    -- ===== QUALITY CHANGES =====
    previous_quality_tier LowCardinality(String),
    new_quality_tier LowCardinality(String),
    previous_resolution String,
    new_resolution String,
    previous_codec LowCardinality(String),
    new_codec LowCardinality(String)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 90 DAY;

-- Hourly viewer aggregates for dashboard charts
CREATE TABLE IF NOT EXISTS stream_viewer_hourly (
    hour DateTime,
    tenant_id UUID,
    internal_name String,
    total_viewers AggregateFunction(sum, UInt32),
    peak_viewers AggregateFunction(max, UInt32),
    avg_viewers AggregateFunction(avg, UInt32)
) ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, internal_name);

-- Aggregation MV → stream_viewer_hourly
CREATE MATERIALIZED VIEW IF NOT EXISTS stream_viewer_hourly_mv TO stream_viewer_hourly AS
SELECT
    toStartOfHour(timestamp) as hour,
    tenant_id,
    internal_name,
    sumState(viewer_count) as total_viewers,
    maxState(viewer_count) as peak_viewers,
    avgState(viewer_count) as avg_viewers
FROM viewer_metrics
GROUP BY hour, tenant_id, internal_name;

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
ORDER BY (hour, tenant_id, internal_name);

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
ORDER BY (timestamp_5m, node_id);

-- Aggregation MV → node_performance_5m
CREATE MATERIALIZED VIEW IF NOT EXISTS node_performance_5m_mv TO node_performance_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) as timestamp_5m,
    node_id,
    avg(cpu_usage) as avg_cpu,
    max(cpu_usage) as max_cpu,
    avg(memory_usage) as avg_memory,
    max(memory_usage) as max_memory,
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
    total_bandwidth_in UInt64,
    total_bandwidth_out UInt64,
    avg_health_score Float32,
    was_healthy UInt8
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp_1h)
ORDER BY (timestamp_1h, node_id);

-- Aggregation MV → node_metrics_1h
CREATE MATERIALIZED VIEW IF NOT EXISTS node_metrics_1h_mv TO node_metrics_1h AS
SELECT
    toStartOfHour(timestamp) AS timestamp_1h,
    node_id,
    avg(cpu_usage) AS avg_cpu,
    max(cpu_usage) AS peak_cpu,
    avg(memory_usage) AS avg_memory,
    max(memory_usage) AS peak_memory,
    sum(bandwidth_in) AS total_bandwidth_in,
    sum(bandwidth_out) AS total_bandwidth_out,
    avg(health_score) AS avg_health_score,
    if(avg(is_healthy) >= 0.5, 1, 0) AS was_healthy
FROM node_metrics
GROUP BY timestamp_1h, node_id;

-- Daily viewer metrics per tenant
CREATE TABLE IF NOT EXISTS tenant_viewer_daily (
    day Date,
    tenant_id UUID,
    viewer_minutes UInt64,
    peak_concurrent_viewers UInt32,
    unique_streams UInt32,
    total_stream_hours Float32
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id);

-- Aggregation MV → tenant_viewer_daily
CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_viewer_daily_mv TO tenant_viewer_daily AS
SELECT
    toDate(timestamp) as day,
    tenant_id,
    sum(viewer_count * 5) as viewer_minutes,
    max(viewer_count) as peak_concurrent_viewers,
    uniq(internal_name) as unique_streams,
    count() * 5 / 60.0 as total_stream_hours
FROM viewer_metrics
GROUP BY day, tenant_id;

-- Daily connection/bandwidth metrics per tenant
CREATE TABLE IF NOT EXISTS tenant_connection_daily (
    day Date,
    tenant_id UUID,
    total_bytes UInt64,
    unique_sessions UInt32,
    total_connections UInt32
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id);

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

-- Hourly viewer geographics per tenant
CREATE TABLE IF NOT EXISTS viewer_geo_hourly (
    hour DateTime,
    tenant_id UUID,
    country_code FixedString(2),
    city LowCardinality(String),
    viewer_count UInt32,
    session_count UInt32,
    avg_latitude Float64,
    avg_longitude Float64
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, country_code, city);

-- Aggregation MV → viewer_geo_hourly
CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_geo_hourly_mv TO viewer_geo_hourly AS
SELECT
    toStartOfHour(timestamp) as hour,
    tenant_id,
    country_code,
    city,
    sum(viewer_count) as viewer_count,
    count() as session_count,
    avg(latitude) as avg_latitude,
    avg(longitude) as avg_longitude
FROM viewer_metrics
WHERE latitude != 0 AND longitude != 0
GROUP BY hour, tenant_id, country_code, city;

-- 5-minute stream health rollups
CREATE TABLE IF NOT EXISTS stream_health_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    avg_health_score Float32,
    max_jitter Float32,
    avg_keyframe_stability Float32,
    rebuffer_count UInt32,
    issue_count UInt32,
    sample_issues String,
    avg_bitrate Float32,
    avg_fps Float32,
    packet_loss_percentage Float32,
    buffer_dry_count UInt32,
    quality_tier LowCardinality(String)
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp_5m)
ORDER BY (timestamp_5m, tenant_id, internal_name, node_id);

-- Aggregation MV → stream_health_5m
CREATE MATERIALIZED VIEW IF NOT EXISTS stream_health_5m_mv TO stream_health_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    internal_name,
    node_id,
    avg(health_score) AS avg_health_score,
    max(frame_jitter_ms) AS max_jitter,
    avg(keyframe_stability_ms) AS avg_keyframe_stability,
    countIf(buffer_state = 'DRY') AS rebuffer_count,
    countIf(has_issues = 1) AS issue_count,
    any(issues_description) AS sample_issues,
    avg(bitrate) AS avg_bitrate,
    avg(fps) AS avg_fps,
    avg(if(packets_sent > 0, (packets_lost / packets_sent) * 100, 0)) AS packet_loss_percentage,
    countIf(buffer_state = 'DRY') AS buffer_dry_count,
    argMax(
        if(height >= 1080, '1080p+', if(height >= 720, '720p', if(height >= 480, '480p', 'SD'))), timestamp
    ) AS quality_tier
FROM stream_health_metrics
GROUP BY timestamp_5m, tenant_id, internal_name, node_id;

-- Quality changes per hour (resolution/codec)
CREATE TABLE IF NOT EXISTS quality_changes_1h (
    hour DateTime,
    tenant_id UUID,
    internal_name String,
    total_changes UInt32,
    resolution_changes UInt32,
    codec_changes UInt32,
    quality_tiers Array(String),
    latest_quality LowCardinality(String),
    latest_codec LowCardinality(String),
    latest_resolution String
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, internal_name);

-- Aggregation MV → quality_changes_1h
CREATE MATERIALIZED VIEW IF NOT EXISTS quality_changes_1h_mv TO quality_changes_1h AS
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    internal_name,
    count() AS total_changes,
    countIf(change_type = 'resolution_change') AS resolution_changes,
    countIf(change_type = 'codec_change') AS codec_changes,
    groupArray(new_quality_tier) AS quality_tiers,
    argMax(new_quality_tier, timestamp) AS latest_quality,
    argMax(new_codec, timestamp) AS latest_codec,
    argMax(new_resolution, timestamp) AS latest_resolution
FROM track_change_events
GROUP BY hour, tenant_id, internal_name;

-- Rebuffering events derived from health samples
CREATE TABLE IF NOT EXISTS rebuffering_events (
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    buffer_state LowCardinality(String),
    prev_state LowCardinality(String),
    rebuffer_start UInt8,
    rebuffer_end UInt8,
    health_score Float32
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name);

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
    if(buffer_state = 'RECOVER' AND prev_state = 'DRY', 1, 0) AS rebuffer_end,
    health_score
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
ORDER BY (day, tenant_id, internal_name);

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
    
    -- ===== CLIP DEFINITION =====
    title Nullable(String),
    format Nullable(String),            -- mp4, webm, etc.
    start_unix Nullable(Int64),         -- Unix timestamp
    stop_unix Nullable(Int64),          -- Unix timestamp
    start_ms Nullable(Int64),           -- Milliseconds offset
    stop_ms Nullable(Int64),            -- Milliseconds offset
    duration_sec Nullable(Int64),       -- Total duration
    
    -- ===== NODE ROUTING =====
    ingest_node_id Nullable(String),    -- Source node
    storage_node_id Nullable(String),   -- Destination node
    routing_distance_km Nullable(Float64), -- Geographic distance
    
    -- ===== PROCESSING STATUS =====
    percent Nullable(UInt32),           -- Completion percentage
    message Nullable(String),           -- Status/error message
    
    -- ===== OUTPUT DETAILS =====
    file_path Nullable(String),         -- Local file path
    s3_url Nullable(String),            -- S3 URL if uploaded
    size_bytes Nullable(UInt64)         -- Final file size
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name, request_id)
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
ORDER BY (hour, tenant_id, internal_name, stage);

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


