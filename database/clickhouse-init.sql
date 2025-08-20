-- ============================================================================
-- CLICKHOUSE SCHEMA FOR FRAMEWORKS TIME-SERIES ANALYTICS
-- ============================================================================

-- Create database if it doesn't exist
CREATE DATABASE IF NOT EXISTS frameworks;

-- Switch to using the frameworks database
USE frameworks;

-- ============================================================================
-- STREAM EVENTS
-- ============================================================================

-- Stream lifecycle and operational events
CREATE TABLE IF NOT EXISTS stream_events (
    -- Event identification
    event_id UUID,
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,
    
    -- Event details
    event_type LowCardinality(String),
    status LowCardinality(String),
    node_id LowCardinality(String),
    
    -- Optional event-specific data
    ingest_type Nullable(String),
    protocol Nullable(String),
    target Nullable(String),
    file_size Nullable(UInt64),
    duration Nullable(UInt32),
    
    -- Raw event data
    event_data String -- JSON string
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 90 DAY;

-- ============================================================================
-- ROUTING EVENTS
-- ============================================================================

-- Load balancing and routing events
CREATE TABLE IF NOT EXISTS routing_events (
    timestamp DateTime,
    tenant_id UUID,
    stream_name String,
    selected_node LowCardinality(String),
    status LowCardinality(String),
    details String,
    score Int64,
    client_ip String,
    client_country FixedString(2),
    client_region LowCardinality(String),
    client_city LowCardinality(String),
    client_latitude Float64,
    client_longitude Float64,
    node_scores String,
    routing_metadata String
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, stream_name)
TTL timestamp + INTERVAL 90 DAY;

-- ============================================================================
-- VIEWER METRICS
-- ============================================================================

-- Viewer metrics with efficient time-series storage
CREATE TABLE IF NOT EXISTS viewer_metrics (
    -- Time and tenant partitioning
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,
    
    -- Core metrics
    viewer_count UInt32,
    connection_type LowCardinality(String),
    node_id LowCardinality(String),
    
    -- Geographic data
    country_code FixedString(2),
    city LowCardinality(String),
    latitude Float64,
    longitude Float64,
    
    -- Technical data
    connection_quality Float32,
    buffer_health Float32
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 90 DAY;

-- Connection events for session analysis
CREATE TABLE IF NOT EXISTS connection_events (
    -- Event identification
    event_id UUID,
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,
    
    -- Connection details
    user_id String,
    session_id String,
    connection_addr String,
    user_agent String,
    connector LowCardinality(String),
    node_id LowCardinality(String),
    
    -- Geographic data
    country_code FixedString(2),
    city LowCardinality(String),
    latitude Float64,
    longitude Float64,
    
    -- Session metrics
    event_type LowCardinality(String),
    session_duration UInt32,
    bytes_transferred UInt64
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 90 DAY;

-- ============================================================================
-- NETWORK METRICS
-- ============================================================================

-- Node performance metrics
CREATE TABLE IF NOT EXISTS node_metrics (
    -- Time and node identification
    timestamp DateTime,
    tenant_id UUID,
    node_id LowCardinality(String),
    
    -- Resource metrics
    cpu_usage Float32,
    memory_usage Float32,
    disk_usage Float32,
    ram_max UInt64,
    ram_current UInt64,
    
    -- Network metrics
    bandwidth_in UInt64,
    bandwidth_out UInt64,
    up_speed UInt64,
    down_speed UInt64,
    connections_current UInt32,
    stream_count UInt32,
    
    -- Health metrics
    health_score Float32,
    is_healthy UInt8,
    
    -- Geographic data
    latitude Float64,
    longitude Float64,
    
    -- Additional metadata
    tags Array(String),
    metadata JSON
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, node_id)
TTL timestamp + INTERVAL 30 DAY;

-- ============================================================================
-- STREAM HEALTH METRICS
-- ============================================================================

-- Stream health metrics for detailed monitoring
CREATE TABLE IF NOT EXISTS stream_health_metrics (
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    
    -- Video quality metrics
    bitrate UInt32,
    fps Float32,
    gop_size UInt16,
    width UInt16,
    height UInt16,
    
    -- Buffer and connection health
    buffer_size UInt32,
    buffer_used UInt32,
    buffer_health Float32,
    buffer_state LowCardinality(String), -- FULL, EMPTY, DRY, RECOVER
    
    -- Network performance
    packets_sent UInt64,
    packets_lost UInt64,
    packets_retransmitted UInt64,
    bandwidth_in UInt64,
    bandwidth_out UInt64,
    
    -- Codec information
    codec LowCardinality(String),
    profile LowCardinality(String),
    track_metadata JSON,
    
    -- Enhanced health metrics from STREAM_BUFFER parsing
    frame_jitter_ms Float32,
    keyframe_stability_ms Float32,
    issues_description String,
    has_issues UInt8,
    health_score Float32,
    track_count UInt16,
    
    -- Frame timing metrics
    frame_ms_max Float32,
    frame_ms_min Float32,
    frames_max UInt32,
    frames_min UInt32,
    keyframe_ms_max Float32,
    keyframe_ms_min Float32,
    
    -- Audio metrics
    audio_channels UInt8,
    audio_sample_rate UInt32,
    audio_codec LowCardinality(String),
    audio_bitrate UInt32
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 30 DAY;

-- ============================================================================
-- USAGE RECORDS
-- ============================================================================

-- Usage records for billing (time-series usage tracking)
CREATE TABLE IF NOT EXISTS usage_records (
    timestamp DateTime,
    tenant_id UUID,
    cluster_id String,
    usage_type LowCardinality(String),
    usage_value Float64,
    billing_month Date,
    usage_details String
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, tenant_id, cluster_id)
TTL timestamp + INTERVAL 365 DAY;

-- ============================================================================
-- MATERIALIZED VIEWS FOR REAL-TIME AGGREGATION
-- ============================================================================

-- 5-minute viewer aggregates table
CREATE TABLE IF NOT EXISTS viewer_metrics_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    peak_viewers UInt32,
    avg_viewers Float32,
    unique_countries UInt32,
    unique_cities UInt32,
    avg_connection_quality Float32,
    avg_buffer_health Float32
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp_5m), tenant_id)
ORDER BY (timestamp_5m, tenant_id, internal_name, node_id);

-- Materialized view for 5-minute aggregated viewer metrics
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

-- Track list events with enhanced quality metrics
CREATE TABLE IF NOT EXISTS track_list_events (
    timestamp DateTime,
    event_id UUID,
    tenant_id UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000001'),
    internal_name String,
    node_id LowCardinality(String),
    track_list String,
    track_count UInt16,
    
    -- Video track metrics
    video_track_count UInt16,
    audio_track_count UInt16,
    primary_width UInt16,
    primary_height UInt16,
    primary_fps Float32,
    primary_video_codec LowCardinality(String),
    primary_video_bitrate UInt32,
    quality_tier LowCardinality(String), -- 1080p+, 720p, 480p, SD
    
    -- Audio track metrics
    primary_audio_channels UInt8,
    primary_audio_sample_rate UInt32,
    primary_audio_codec LowCardinality(String),
    primary_audio_bitrate UInt32
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 90 DAY;

-- Track change events for quality monitoring
CREATE TABLE IF NOT EXISTS track_change_events (
    timestamp DateTime,
    event_id UUID,
    tenant_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    
    -- Change detection
    change_type LowCardinality(String), -- codec_change, resolution_change, bitrate_change, track_added, track_removed
    previous_tracks String, -- JSON
    new_tracks String, -- JSON
    
    -- Change details
    previous_quality_tier LowCardinality(String),
    new_quality_tier LowCardinality(String),
    previous_resolution String, -- e.g., "1920x1080"
    new_resolution String,
    previous_codec LowCardinality(String),
    new_codec LowCardinality(String)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 90 DAY;

-- ============================================================================
-- ADDITIONAL MATERIALIZED VIEWS FOR GRAFANA DASHBOARDS
-- ============================================================================

-- Hourly viewer summary from viewer_metrics
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

-- Hourly connection summary from connection_events  
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

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_connection_hourly_mv TO stream_connection_hourly AS
SELECT
    toStartOfHour(timestamp) as hour,
    tenant_id,
    internal_name,
    sumState(bytes_transferred) as total_bytes,
    uniqState(user_id) as unique_viewers,
    countState() as total_sessions
FROM connection_events
GROUP BY hour, tenant_id, internal_name;

-- Node performance 5-minute summary for infrastructure monitoring
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

-- Daily tenant viewer metrics from viewer_metrics table
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

CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_viewer_daily_mv TO tenant_viewer_daily AS
SELECT
    toDate(timestamp) as day,
    tenant_id,
    sum(viewer_count * 5) as viewer_minutes, -- 5-minute samples to viewer-minutes
    max(viewer_count) as peak_concurrent_viewers,
    uniq(internal_name) as unique_streams,
    count() * 5 / 60.0 as total_stream_hours -- Convert 5-minute samples to hours
FROM viewer_metrics
GROUP BY day, tenant_id;

-- Daily tenant connection metrics from connection_events table
CREATE TABLE IF NOT EXISTS tenant_connection_daily (
    day Date,
    tenant_id UUID,
    total_bytes UInt64,
    unique_sessions UInt32,
    total_connections UInt32
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id);

CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_connection_daily_mv TO tenant_connection_daily AS
SELECT
    toDate(timestamp) as day,
    tenant_id,
    sum(bytes_transferred) as total_bytes,
    uniq(session_id) as unique_sessions,
    count() as total_connections
FROM connection_events
GROUP BY day, tenant_id;

-- Geographic viewer distribution for maps
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

-- ============================================================================
-- STREAM HEALTH MATERIALIZED VIEWS
-- ============================================================================

-- Stream health summary (5-minute aggregates)
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
        if(height >= 1080, '1080p+',
           if(height >= 720, '720p',
              if(height >= 480, '480p', 'SD'))), 
        timestamp
    ) AS quality_tier
FROM stream_health_metrics
GROUP BY timestamp_5m, tenant_id, internal_name, node_id;

-- Quality changes aggregation (hourly)
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

-- Rebuffering events materialized view
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

-- Stream health alerts (issues detected)
CREATE TABLE IF NOT EXISTS stream_health_alerts (
    timestamp DateTime,
    tenant_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    alert_type LowCardinality(String), -- high_jitter, keyframe_instability, packet_loss, rebuffering
    severity LowCardinality(String),   -- low, medium, high, critical
    health_score Float32,
    frame_jitter_ms Float32,
    packet_loss_percentage Float32,
    issues_description String
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name, alert_type);

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_health_alerts_mv TO stream_health_alerts AS
SELECT
    timestamp,
    tenant_id,
    internal_name,
    node_id,
    multiIf(
        frame_jitter_ms > 50, 'high_jitter',
        keyframe_stability_ms > 100, 'keyframe_instability',
        (packets_lost / packets_sent) > 0.05, 'packet_loss',
        buffer_state = 'DRY', 'rebuffering',
        'unknown'
    ) AS alert_type,
    multiIf(
        frame_jitter_ms > 100 OR (packets_lost / packets_sent) > 0.1, 'critical',
        frame_jitter_ms > 75 OR (packets_lost / packets_sent) > 0.05, 'high',
        frame_jitter_ms > 30 OR keyframe_stability_ms > 50, 'medium',
        'low'
    ) AS severity,
    health_score,
    frame_jitter_ms,
    if(packets_sent > 0, (packets_lost / packets_sent) * 100, 0) AS packet_loss_percentage,
    issues_description
FROM stream_health_metrics
WHERE has_issues = 1 
   OR frame_jitter_ms > 30 
   OR keyframe_stability_ms > 50 
   OR (packets_sent > 0 AND (packets_lost / packets_sent) > 0.02)
   OR buffer_state = 'DRY';

-- Track quality tier trends (daily)
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


