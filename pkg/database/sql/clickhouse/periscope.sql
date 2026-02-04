-- ============================================================================
-- PERISCOPE V2 CLICKHOUSE SCHEMA (GraphQL-first)
-- Clean, normalized analytics schema designed to align with public GraphQL API.
-- ============================================================================

CREATE DATABASE IF NOT EXISTS periscope;
USE periscope;

-- ============================================================================
-- CORE STREAM EVENT LOG (normalized lifecycle + notable events)
-- ============================================================================

CREATE TABLE IF NOT EXISTS stream_event_log (
    event_id UUID,
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),

    event_type LowCardinality(String),      -- canonical event type (stream_lifecycle, stream_buffer, stream_end, etc.)
    status Nullable(String),                -- stream status at time of event

    -- Optional stream metrics captured at event time
    buffer_state Nullable(String),
    has_issues Nullable(UInt8),
    issues_description Nullable(String),
    track_count Nullable(UInt16),
    quality_tier Nullable(String),
    primary_width Nullable(UInt16),
    primary_height Nullable(UInt16),
    primary_fps Nullable(Float32),
    primary_codec Nullable(String),
    primary_bitrate Nullable(UInt32),

    downloaded_bytes Nullable(UInt64),
    uploaded_bytes Nullable(UInt64),
    total_viewers Nullable(UInt32),
    total_inputs Nullable(UInt16),
    total_outputs Nullable(UInt16),
    viewer_seconds Nullable(UInt64),

    -- Ingest / routing details (where present)
    stream_key Nullable(String),
    user_id Nullable(String),
    request_url Nullable(String),
    protocol Nullable(String),

    -- Geo metadata (when present)
    latitude Nullable(Float64),
    longitude Nullable(Float64),
    location Nullable(String),
    country_code Nullable(FixedString(2)),
    city Nullable(String),

    -- Full event payload (typed JSON)
    event_data String
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp, event_id)
TTL timestamp + INTERVAL 90 DAY;

-- ============================================================================
-- STREAM VIEWER ROLLUPS (from stream_event_log.total_viewers)
-- ============================================================================

CREATE TABLE IF NOT EXISTS stream_viewer_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    stream_id UUID,
    max_viewers UInt32,
    avg_viewers Float32
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp_5m)
ORDER BY (timestamp_5m, tenant_id, stream_id)
TTL timestamp_5m + INTERVAL 180 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_viewer_5m_mv TO stream_viewer_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    stream_id,
    max(total_viewers) AS max_viewers,
    avg(total_viewers) AS avg_viewers
FROM stream_event_log
WHERE total_viewers IS NOT NULL
GROUP BY timestamp_5m, tenant_id, stream_id;

-- ============================================================================
-- STREAM CURRENT STATE (live snapshot)
-- ============================================================================

CREATE TABLE IF NOT EXISTS stream_state_current (
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),

    status LowCardinality(String),
    buffer_state LowCardinality(String),

    current_viewers UInt32,
    total_inputs UInt16,

    uploaded_bytes UInt64,
    downloaded_bytes UInt64,
    viewer_seconds UInt64,

    has_issues Nullable(UInt8),
    issues_description Nullable(String),
    track_count Nullable(UInt16),
    quality_tier Nullable(String),
    primary_width Nullable(UInt16),
    primary_height Nullable(UInt16),
    primary_fps Nullable(Float32),
    primary_codec Nullable(String),
    primary_bitrate Nullable(UInt32),

    packets_sent Nullable(UInt64),
    packets_lost Nullable(UInt64),
    packets_retransmitted Nullable(UInt64),

    started_at Nullable(DateTime),
    updated_at DateTime
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, stream_id);

-- ============================================================================
-- STREAM HEALTH SAMPLES (detailed QoE)
-- ============================================================================

CREATE TABLE IF NOT EXISTS stream_health_samples (
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),

    bitrate Nullable(UInt32),
    fps Nullable(Float32),
    gop_size Nullable(UInt16),
    width Nullable(UInt16),
    height Nullable(UInt16),

    buffer_size Nullable(UInt32),
    buffer_health Nullable(Float32),
    buffer_state LowCardinality(String),

    codec Nullable(String),
    quality_tier Nullable(String),
    track_metadata JSON,

    frame_ms_max Nullable(Float32),
    frame_ms_min Nullable(Float32),
    frames_max Nullable(UInt32),
    frames_min Nullable(UInt32),
    keyframe_ms_max Nullable(Float32),
    keyframe_ms_min Nullable(Float32),
    frame_jitter_ms Nullable(Float32),

    issues_description Nullable(String),
    has_issues Nullable(UInt8),
    track_count Nullable(UInt16),

    audio_channels Nullable(UInt8),
    audio_sample_rate Nullable(UInt32),
    audio_codec Nullable(String),
    audio_bitrate Nullable(UInt32)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp)
TTL timestamp + INTERVAL 30 DAY;

-- 5m rollups for health
CREATE TABLE IF NOT EXISTS stream_health_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    rebuffer_count UInt32,
    issue_count UInt32,
    sample_issues Nullable(String),
    avg_bitrate Float32,
    avg_fps Float32,
    avg_buffer_health Float32,
    avg_frame_jitter_ms Nullable(Float32),
    max_frame_jitter_ms Nullable(Float32),
    buffer_dry_count UInt32,
    quality_tier LowCardinality(String)
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp_5m)
ORDER BY (timestamp_5m, tenant_id, stream_id, node_id)
TTL timestamp_5m + INTERVAL 180 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_health_5m_mv TO stream_health_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    stream_id,
    internal_name,
    node_id,
    countIf(buffer_state = 'DRY') AS rebuffer_count,
    countIf(has_issues = 1) AS issue_count,
    any(issues_description) AS sample_issues,
    ifNull(avg(bitrate), 0) AS avg_bitrate,
    ifNull(avg(fps), 0) AS avg_fps,
    ifNull(avg(buffer_health), 0) AS avg_buffer_health,
    avg(frame_jitter_ms) AS avg_frame_jitter_ms,
    max(frame_jitter_ms) AS max_frame_jitter_ms,
    countIf(buffer_state = 'DRY') AS buffer_dry_count,
    ifNull(argMax(
        if(height >= 2160, '2160p',
          if(height >= 1440, '1440p',
            if(height >= 1080, '1080p',
              if(height >= 720, '720p',
                if(height >= 480, '480p', 'SD'))))), timestamp
    ), 'Unknown') AS quality_tier
FROM stream_health_samples
GROUP BY timestamp_5m, tenant_id, stream_id, internal_name, node_id;

-- Rebuffering events derived from health samples
CREATE TABLE IF NOT EXISTS rebuffering_events (
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    buffer_state LowCardinality(String),
    prev_state LowCardinality(String),
    rebuffer_start UInt8,
    rebuffer_end UInt8
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp, event_id)
TTL timestamp + INTERVAL 90 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS rebuffering_events_mv TO rebuffering_events AS
SELECT
    timestamp,
    tenant_id,
    stream_id,
    internal_name,
    node_id,
    buffer_state,
    lagInFrame(buffer_state) OVER (PARTITION BY tenant_id, stream_id ORDER BY timestamp) AS prev_state,
    if(buffer_state = 'DRY' AND prev_state IN ('FULL', 'RECOVER'), 1, 0) AS rebuffer_start,
    if(buffer_state = 'RECOVER' AND prev_state = 'DRY', 1, 0) AS rebuffer_end
FROM stream_health_samples
WHERE buffer_state IN ('FULL', 'DRY', 'RECOVER');

-- ============================================================================
-- TRACK LIST EVENTS + QUALITY ROLLUPS
-- ============================================================================

CREATE TABLE IF NOT EXISTS track_list_events (
    timestamp DateTime,
    event_id UUID,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),

    track_list String,
    track_count UInt16,
    video_track_count UInt16,
    audio_track_count UInt16,

    primary_width Nullable(UInt16),
    primary_height Nullable(UInt16),
    primary_fps Nullable(Float32),
    primary_video_codec Nullable(String),
    primary_video_bitrate Nullable(UInt32),
    quality_tier Nullable(String),

    primary_audio_channels Nullable(UInt8),
    primary_audio_sample_rate Nullable(UInt32),
    primary_audio_codec Nullable(String),
    primary_audio_bitrate Nullable(UInt32)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp, event_id)
TTL timestamp + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS quality_tier_daily (
    day Date,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    tier_2160p_minutes UInt32,
    tier_1440p_minutes UInt32,
    tier_1080p_minutes UInt32,
    tier_720p_minutes UInt32,
    tier_480p_minutes UInt32,
    tier_sd_minutes UInt32,
    primary_tier LowCardinality(String),
    codec_h264_minutes UInt32,
    codec_h265_minutes UInt32,
    codec_vp9_minutes UInt32,
    codec_av1_minutes UInt32,
    avg_bitrate UInt32,
    avg_fps Float32
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id, stream_id)
TTL day + INTERVAL 730 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS quality_tier_daily_mv TO quality_tier_daily AS
SELECT
    toDate(timestamp) as day,
    tenant_id,
    stream_id,
    internal_name,
    countIf(primary_height >= 2160) * 5 AS tier_2160p_minutes,
    countIf(primary_height >= 1440 AND primary_height < 2160) * 5 AS tier_1440p_minutes,
    countIf(primary_height >= 1080 AND primary_height < 1440) * 5 AS tier_1080p_minutes,
    countIf(primary_height >= 720 AND primary_height < 1080) * 5 AS tier_720p_minutes,
    countIf(primary_height >= 480 AND primary_height < 720) * 5 AS tier_480p_minutes,
    countIf(primary_height < 480) * 5 AS tier_sd_minutes,
    ifNull(argMax(quality_tier, timestamp), 'Unknown') AS primary_tier,
    countIf(primary_video_codec LIKE '%264%') * 5 AS codec_h264_minutes,
    countIf(primary_video_codec LIKE '%265%' OR primary_video_codec LIKE '%HEVC%') * 5 AS codec_h265_minutes,
    countIf(lower(primary_video_codec) LIKE '%vp9%') * 5 AS codec_vp9_minutes,
    countIf(lower(primary_video_codec) LIKE '%av1%') * 5 AS codec_av1_minutes,
    ifNull(toUInt32(avg(primary_video_bitrate)), 0) AS avg_bitrate,
    ifNull(avg(primary_fps), 0) AS avg_fps
FROM track_list_events
WHERE track_count > 0
GROUP BY day, tenant_id, stream_id, internal_name;

-- ============================================================================
-- VIEWER CONNECTIONS + SESSIONS
-- ============================================================================

CREATE TABLE IF NOT EXISTS viewer_connection_events (
    event_id UUID,
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    session_id String,
    connection_addr String,
    connector LowCardinality(String),
    node_id LowCardinality(String),
    request_url Nullable(String),

    country_code FixedString(2),
    city LowCardinality(String),
    latitude Float64,
    longitude Float64,
    client_bucket_h3 Nullable(UInt64),
    client_bucket_res Nullable(UInt8),
    node_bucket_h3 Nullable(UInt64),
    node_bucket_res Nullable(UInt8),

    event_type LowCardinality(String),
    session_duration UInt32,
    bytes_transferred UInt64
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp, event_id)
TTL timestamp + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS viewer_sessions_current (
    tenant_id UUID,
    stream_id UUID,
    internal_name LowCardinality(String),
    session_id String,
    node_id LowCardinality(String),

    connected_at SimpleAggregateFunction(min, Nullable(DateTime)),
    disconnected_at SimpleAggregateFunction(max, Nullable(DateTime)),

    connector SimpleAggregateFunction(any, LowCardinality(String)),

    country_code SimpleAggregateFunction(any, FixedString(2)),
    city SimpleAggregateFunction(any, LowCardinality(String)),
    latitude SimpleAggregateFunction(any, Float64),
    longitude SimpleAggregateFunction(any, Float64),

    bytes_transferred SimpleAggregateFunction(max, UInt64),
    session_duration SimpleAggregateFunction(max, UInt32),

    last_updated SimpleAggregateFunction(max, DateTime)
) ENGINE = AggregatingMergeTree()
ORDER BY (tenant_id, stream_id, node_id, session_id);

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_sessions_connect_mv TO viewer_sessions_current AS
SELECT
    tenant_id,
    stream_id,
    internal_name,
    session_id,
    node_id,
    timestamp AS connected_at,
    CAST(NULL AS Nullable(DateTime)) AS disconnected_at,
    connector,
    country_code,
    city,
    latitude,
    longitude,
    bytes_transferred,
    session_duration,
    timestamp AS last_updated
FROM viewer_connection_events
WHERE event_type = 'connect' AND session_id != '';

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_sessions_disconnect_mv TO viewer_sessions_current AS
SELECT
    tenant_id,
    stream_id,
    internal_name,
    session_id,
    node_id,
    CAST(NULL AS Nullable(DateTime)) AS connected_at,
    timestamp AS disconnected_at,
    connector,
    country_code,
    city,
    latitude,
    longitude,
    bytes_transferred,
    session_duration,
    timestamp AS last_updated
FROM viewer_connection_events
WHERE event_type = 'disconnect' AND session_id != '';

-- Client QoE samples
CREATE TABLE IF NOT EXISTS client_qoe_samples (
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    session_id String,
    node_id LowCardinality(String),

    protocol LowCardinality(String),
    host String,
    connection_time Float32,
    position Nullable(Float32),

    bandwidth_in UInt64,
    bandwidth_out UInt64,
    bytes_downloaded UInt64,
    bytes_uploaded UInt64,

    packets_sent UInt64,
    packets_lost UInt64,
    packets_retransmitted UInt64,
    connection_quality Nullable(Float32)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp)
TTL timestamp + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS client_qoe_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    active_sessions UInt32,
    avg_bw_in Float64,
    avg_bw_out Float64,
    avg_connection_time Float32,
    pkt_loss_rate Nullable(Float32)
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp_5m)
ORDER BY (timestamp_5m, tenant_id, stream_id, node_id)
TTL timestamp_5m + INTERVAL 180 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS client_qoe_5m_mv TO client_qoe_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    stream_id,
    internal_name,
    node_id,
    count(DISTINCT session_id) as active_sessions,
    avg(bandwidth_in) AS avg_bw_in,
    avg(bandwidth_out) AS avg_bw_out,
    avg(connection_time) AS avg_connection_time,
    if(sum(packets_sent) > 0, sum(packets_lost) / sum(packets_sent), NULL) AS pkt_loss_rate
FROM client_qoe_samples
GROUP BY timestamp_5m, tenant_id, stream_id, internal_name, node_id;

-- ============================================================================
-- VIEWER USAGE ROLLUPS
-- ============================================================================

-- Tenant-level usage rollup (5-minute) for live billing dashboards
CREATE TABLE IF NOT EXISTS tenant_usage_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    unique_viewers AggregateFunction(uniq, String),
    total_session_seconds AggregateFunction(sum, UInt64),
    total_bytes AggregateFunction(sum, UInt64)
) ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(timestamp_5m)
ORDER BY (timestamp_5m, tenant_id)
TTL timestamp_5m + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_usage_5m_mv TO tenant_usage_5m AS
SELECT
    toStartOfFiveMinute(timestamp) AS timestamp_5m,
    tenant_id,
    uniqState(session_id) AS unique_viewers,
    sumState(toUInt64(session_duration)) AS total_session_seconds,
    sumState(bytes_transferred) AS total_bytes
FROM viewer_connection_events
WHERE event_type = 'disconnect'
GROUP BY timestamp_5m, tenant_id;

CREATE TABLE IF NOT EXISTS stream_connection_hourly (
    hour DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    total_bytes AggregateFunction(sum, UInt64),
    unique_viewers AggregateFunction(uniq, String),
    total_sessions AggregateFunction(count, UInt64)
) ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, stream_id)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_connection_hourly_mv TO stream_connection_hourly AS
SELECT
    toStartOfHour(timestamp) as hour,
    tenant_id,
    stream_id,
    internal_name,
    sumState(bytes_transferred) as total_bytes,
    uniqState(session_id) as unique_viewers,
    countState() as total_sessions
FROM viewer_connection_events
GROUP BY hour, tenant_id, stream_id, internal_name;

CREATE TABLE IF NOT EXISTS viewer_hours_hourly (
    hour DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    country_code FixedString(2),
    unique_viewers AggregateFunction(uniq, String),
    total_session_seconds AggregateFunction(sum, UInt64),
    total_bytes AggregateFunction(sum, UInt64)
) ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, stream_id, country_code)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_hours_hourly_mv TO viewer_hours_hourly AS
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    stream_id,
    internal_name,
    country_code,
    uniqState(session_id) AS unique_viewers,
    sumState(toUInt64(session_duration)) AS total_session_seconds,
    sumState(bytes_transferred) AS total_bytes
FROM viewer_connection_events
WHERE event_type = 'disconnect'
GROUP BY hour, tenant_id, stream_id, internal_name, country_code;

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

CREATE TABLE IF NOT EXISTS viewer_city_hourly (
    hour DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    country_code FixedString(2),
    city LowCardinality(String),
    latitude AggregateFunction(any, Float64),
    longitude AggregateFunction(any, Float64),
    unique_viewers AggregateFunction(uniq, String),
    total_session_seconds AggregateFunction(sum, UInt64),
    total_bytes AggregateFunction(sum, UInt64)
) ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, stream_id, country_code, city)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_city_hourly_mv TO viewer_city_hourly AS
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    stream_id,
    internal_name,
    country_code,
    city,
    anyState(latitude) AS latitude,
    anyState(longitude) AS longitude,
    uniqState(session_id) AS unique_viewers,
    sumState(toUInt64(session_duration)) AS total_session_seconds,
    sumState(bytes_transferred) AS total_bytes
FROM viewer_connection_events
WHERE event_type = 'disconnect' AND city != ''
GROUP BY hour, tenant_id, stream_id, internal_name, country_code, city;

-- Daily analytics rollups
CREATE TABLE IF NOT EXISTS tenant_analytics_daily (
    day Date,
    tenant_id UUID,
    total_streams UInt32,
    total_views UInt64,
    unique_viewers UInt32,
    egress_bytes UInt64
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id)
TTL day + INTERVAL 730 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_analytics_daily_mv TO tenant_analytics_daily AS
SELECT
    toDate(timestamp) AS day,
    tenant_id,
    uniq(stream_id) AS total_streams,
    countIf(event_type = 'connect') AS total_views,
    uniq(session_id) AS unique_viewers,
    sum(bytes_transferred) AS egress_bytes
FROM viewer_connection_events
GROUP BY day, tenant_id;

CREATE TABLE IF NOT EXISTS stream_analytics_daily (
    day Date,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    total_views UInt64,
    unique_viewers UInt32,
    unique_countries UInt16,
    unique_cities UInt16,
    egress_bytes UInt64
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id, stream_id)
TTL day + INTERVAL 730 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_analytics_daily_mv TO stream_analytics_daily AS
SELECT
    toDate(timestamp) AS day,
    tenant_id,
    stream_id,
    internal_name,
    countIf(event_type = 'connect') AS total_views,
    uniq(session_id) AS unique_viewers,
    uniq(country_code) AS unique_countries,
    uniq(city) AS unique_cities,
    sum(bytes_transferred) AS egress_bytes
FROM viewer_connection_events
GROUP BY day, tenant_id, stream_id, internal_name;

-- ============================================================================
-- ROUTING DECISIONS
-- ============================================================================

CREATE TABLE IF NOT EXISTS routing_decisions (
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,

    selected_node LowCardinality(String),
    status LowCardinality(String),
    details String,
    score Int64,

    client_ip String,
    client_country FixedString(2),
    client_latitude Float64,
    client_longitude Float64,
    client_bucket_h3 Nullable(UInt64),
    client_bucket_res Nullable(UInt8),

    node_latitude Float64,
    node_longitude Float64,
    node_name LowCardinality(String),
    node_bucket_h3 Nullable(UInt64),
    node_bucket_res Nullable(UInt8),
    selected_node_id Nullable(String),
    routing_distance_km Nullable(Float64),

    stream_tenant_id Nullable(UUID),
    cluster_id LowCardinality(String) DEFAULT '',

    latency_ms Nullable(Float32),
    candidates_count Nullable(Int32),
    event_type Nullable(String),
    source Nullable(String)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp)
TTL timestamp + INTERVAL 90 DAY;

-- ============================================================================
-- NODE STATE + METRICS
-- ============================================================================

CREATE TABLE IF NOT EXISTS node_state_current (
    tenant_id UUID,
    cluster_id LowCardinality(String),
    node_id String,

    cpu_percent Float32,
    ram_used_bytes UInt64,
    ram_total_bytes UInt64,
    disk_used_bytes UInt64,
    disk_total_bytes UInt64,

    up_speed UInt64,
    down_speed UInt64,

    active_streams UInt32,
    is_healthy UInt8,

    latitude Float64,
    longitude Float64,
    location String,

    metadata JSON,
    updated_at DateTime
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, cluster_id, node_id);

CREATE TABLE IF NOT EXISTS node_metrics_samples (
    timestamp DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String),
    node_id LowCardinality(String),

    cpu_usage Float32,
    ram_max UInt64,
    ram_current UInt64,
    shm_total_bytes UInt64,
    shm_used_bytes UInt64,
    disk_total_bytes UInt64,
    disk_used_bytes UInt64,

    bandwidth_in UInt64,
    bandwidth_out UInt64,
    up_speed UInt64,
    down_speed UInt64,
    connections_current UInt32,
    stream_count UInt32,

    is_healthy UInt8,
    latitude Float64,
    longitude Float64,

    metadata JSON
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, cluster_id, node_id, timestamp)
TTL timestamp + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS node_performance_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String),
    node_id LowCardinality(String),
    avg_cpu Float32,
    max_cpu Float32,
    avg_memory Float32,
    max_memory Float32,
    total_bandwidth UInt64,
    avg_streams Float32,
    max_streams UInt32
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp_5m), tenant_id)
ORDER BY (tenant_id, cluster_id, node_id, timestamp_5m)
TTL timestamp_5m + INTERVAL 180 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS node_performance_5m_mv TO node_performance_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) as timestamp_5m,
    tenant_id,
    cluster_id,
    node_id,
    avg(cpu_usage) as avg_cpu,
    max(cpu_usage) as max_cpu,
    avg(if(ram_max > 0, ram_current / ram_max * 100, 0)) as avg_memory,
    max(if(ram_max > 0, ram_current / ram_max * 100, 0)) as max_memory,
    sum(bandwidth_in + bandwidth_out) as total_bandwidth,
    avg(stream_count) as avg_streams,
    max(stream_count) as max_streams
FROM node_metrics_samples
GROUP BY timestamp_5m, tenant_id, cluster_id, node_id;

CREATE TABLE IF NOT EXISTS node_metrics_1h (
    timestamp_1h DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String),
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
PARTITION BY (toYYYYMM(timestamp_1h), tenant_id)
ORDER BY (tenant_id, cluster_id, node_id, timestamp_1h)
TTL timestamp_1h + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS node_metrics_1h_mv TO node_metrics_1h AS
SELECT
    toStartOfHour(timestamp) AS timestamp_1h,
    tenant_id,
    cluster_id,
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
FROM node_metrics_samples
GROUP BY timestamp_1h, tenant_id, cluster_id, node_id;

-- ============================================================================
-- ARTIFACT EVENTS + STATE (clips, DVR, VOD)
-- ============================================================================

CREATE TABLE IF NOT EXISTS artifact_events (
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    filename Nullable(String),
    request_id String,

    stage LowCardinality(String),
    content_type LowCardinality(String),

    start_unix Nullable(Int64),
    stop_unix Nullable(Int64),

    ingest_node_id Nullable(String),

    percent Nullable(UInt32),
    message Nullable(String),

    file_path Nullable(String),
    s3_url Nullable(String),
    size_bytes Nullable(UInt64),
    expires_at Nullable(Int64)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp, request_id)
TTL timestamp + INTERVAL 90 DAY;


CREATE TABLE IF NOT EXISTS artifact_state_current (
    tenant_id UUID,
    stream_id UUID,
    request_id String,
    internal_name String,
    filename Nullable(String),

    content_type LowCardinality(String),
    stage LowCardinality(String),
    progress_percent UInt8,
    error_message Nullable(String),

    requested_at DateTime,
    started_at Nullable(DateTime),
    completed_at Nullable(DateTime),

    clip_start_unix Nullable(Int64),
    clip_stop_unix Nullable(Int64),

    segment_count Nullable(UInt32),
    manifest_path Nullable(String),

    file_path Nullable(String),
    s3_url Nullable(String),
    size_bytes Nullable(UInt64),

    processing_node_id Nullable(String),

    updated_at DateTime,
    expires_at Nullable(DateTime)
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, request_id);

-- ============================================================================
-- STORAGE SNAPSHOTS + LIFECYCLE
-- ============================================================================

CREATE TABLE IF NOT EXISTS storage_snapshots (
    timestamp DateTime,
    tenant_id UUID,
    node_id LowCardinality(String),
    storage_scope LowCardinality(String) DEFAULT 'hot',

    total_bytes UInt64,
    file_count UInt32,

    dvr_bytes UInt64,
    clip_bytes UInt64,
    vod_bytes UInt64,

    frozen_dvr_bytes UInt64 DEFAULT 0,
    frozen_clip_bytes UInt64 DEFAULT 0,
    frozen_vod_bytes UInt64 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, tenant_id, node_id)
TTL timestamp + INTERVAL 90 DAY;

-- Storage usage hourly rollup (must be after storage_snapshots table)
CREATE TABLE IF NOT EXISTS storage_usage_hourly (
    hour DateTime,
    tenant_id UUID,
    avg_total_bytes AggregateFunction(avg, UInt64),
    avg_clip_bytes AggregateFunction(avg, UInt64),
    avg_dvr_bytes AggregateFunction(avg, UInt64),
    avg_vod_bytes AggregateFunction(avg, UInt64)
) ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id)
TTL hour + INTERVAL 90 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS storage_usage_hourly_mv TO storage_usage_hourly AS
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    avgState(total_bytes) AS avg_total_bytes,
    avgState(clip_bytes) AS avg_clip_bytes,
    avgState(dvr_bytes) AS avg_dvr_bytes,
    avgState(vod_bytes) AS avg_vod_bytes
FROM storage_snapshots
GROUP BY hour, tenant_id;

CREATE TABLE IF NOT EXISTS storage_events (
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    asset_hash String,

    action LowCardinality(String),
    asset_type LowCardinality(String),

    size_bytes UInt64,
    s3_url Nullable(String),
    local_path Nullable(String),
    node_id LowCardinality(String),
    duration_ms Nullable(Int64),
    warm_duration_ms Nullable(Int64),
    error Nullable(String)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp, asset_hash)
TTL timestamp + INTERVAL 90 DAY;

-- ============================================================================
-- PROCESSING EVENTS + DAILY ROLLUPS
-- ============================================================================

CREATE TABLE IF NOT EXISTS processing_events (
    timestamp DateTime,
    tenant_id UUID,
    node_id LowCardinality(String),
    stream_id UUID,
    internal_name String,

    process_type LowCardinality(String),
    track_type LowCardinality(String),

    duration_ms Int64,

    input_codec Nullable(String),
    output_codec Nullable(String),

    segment_number Nullable(Int32),
    width Nullable(Int32),
    height Nullable(Int32),
    rendition_count Nullable(Int32),
    broadcaster_url Nullable(String),
    upload_time_us Nullable(Int64),
    livepeer_session_id Nullable(String),
    segment_start_ms Nullable(Int64),
    input_bytes Nullable(Int64),
    output_bytes_total Nullable(Int64),
    attempt_count Nullable(Int32),
    turnaround_ms Nullable(Int64),
    speed_factor Nullable(Float64),
    renditions_json Nullable(String),

    input_frames Nullable(Int64),
    output_frames Nullable(Int64),
    decode_us_per_frame Nullable(Int64),
    transform_us_per_frame Nullable(Int64),
    encode_us_per_frame Nullable(Int64),
    is_final Nullable(UInt8),
    input_frames_delta Nullable(Int64),
    output_frames_delta Nullable(Int64),
    input_bytes_delta Nullable(Int64),
    output_bytes_delta Nullable(Int64),
    input_width Nullable(Int32),
    input_height Nullable(Int32),
    output_width Nullable(Int32),
    output_height Nullable(Int32),
    input_fpks Nullable(Int32),
    output_fps_measured Nullable(Float64),
    sample_rate Nullable(Int32),
    channels Nullable(Int32),
    source_timestamp_ms Nullable(Int64),
    sink_timestamp_ms Nullable(Int64),
    source_advanced_ms Nullable(Int64),
    sink_advanced_ms Nullable(Int64),
    rtf_in Nullable(Float64),
    rtf_out Nullable(Float64),
    pipeline_lag_ms Nullable(Int64),
    output_bitrate_bps Nullable(Int64)
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp)
TTL timestamp + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS processing_hourly (
    hour DateTime,
    tenant_id UUID,
    process_type LowCardinality(String),
    output_codec LowCardinality(String),
    track_type LowCardinality(String),

    total_duration_ms AggregateFunction(sum, Int64),
    segment_count AggregateFunction(count, UInt64),
    unique_streams AggregateFunction(uniq, UUID)
) ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, process_type, output_codec, track_type)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS processing_hourly_mv TO processing_hourly AS
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    process_type,
    lower(coalesce(output_codec, 'unknown')) AS output_codec,
    coalesce(track_type, 'video') AS track_type,
    sumState(duration_ms) AS total_duration_ms,
    countState() AS segment_count,
    uniqState(stream_id) AS unique_streams
FROM processing_events
GROUP BY hour, tenant_id, process_type, output_codec, track_type;

CREATE TABLE IF NOT EXISTS processing_daily (
    day Date,
    tenant_id UUID,

    livepeer_h264_seconds Float64,
    livepeer_vp9_seconds Float64,
    livepeer_av1_seconds Float64,
    livepeer_hevc_seconds Float64,
    livepeer_segment_count UInt64,
    livepeer_unique_streams UInt32,

    native_av_h264_seconds Float64,
    native_av_vp9_seconds Float64,
    native_av_av1_seconds Float64,
    native_av_hevc_seconds Float64,
    native_av_aac_seconds Float64,
    native_av_opus_seconds Float64,
    native_av_segment_count UInt64,
    native_av_unique_streams UInt32,

    audio_seconds Float64,
    video_seconds Float64,

    livepeer_seconds Float64,
    native_av_seconds Float64
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id)
TTL day + INTERVAL 730 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS processing_daily_mv TO processing_daily AS
SELECT
    toDate(hour) AS day,
    tenant_id,
    sumMergeIf(total_duration_ms, process_type = 'Livepeer' AND output_codec = 'h264') / 1000.0 AS livepeer_h264_seconds,
    sumMergeIf(total_duration_ms, process_type = 'Livepeer' AND output_codec = 'vp9') / 1000.0 AS livepeer_vp9_seconds,
    sumMergeIf(total_duration_ms, process_type = 'Livepeer' AND output_codec = 'av1') / 1000.0 AS livepeer_av1_seconds,
    sumMergeIf(total_duration_ms, process_type = 'Livepeer' AND output_codec IN ('hevc', 'h265')) / 1000.0 AS livepeer_hevc_seconds,
    toUInt64(countMergeIf(segment_count, process_type = 'Livepeer')) AS livepeer_segment_count,
    toUInt32(uniqMergeIf(unique_streams, process_type = 'Livepeer')) AS livepeer_unique_streams,
    sumMergeIf(total_duration_ms, process_type = 'AV' AND output_codec = 'h264') / 1000.0 AS native_av_h264_seconds,
    sumMergeIf(total_duration_ms, process_type = 'AV' AND output_codec = 'vp9') / 1000.0 AS native_av_vp9_seconds,
    sumMergeIf(total_duration_ms, process_type = 'AV' AND output_codec = 'av1') / 1000.0 AS native_av_av1_seconds,
    sumMergeIf(total_duration_ms, process_type = 'AV' AND output_codec IN ('hevc', 'h265')) / 1000.0 AS native_av_hevc_seconds,
    sumMergeIf(total_duration_ms, process_type = 'AV' AND output_codec = 'aac') / 1000.0 AS native_av_aac_seconds,
    sumMergeIf(total_duration_ms, process_type = 'AV' AND output_codec = 'opus') / 1000.0 AS native_av_opus_seconds,
    toUInt64(countMergeIf(segment_count, process_type = 'AV')) AS native_av_segment_count,
    toUInt32(uniqMergeIf(unique_streams, process_type = 'AV')) AS native_av_unique_streams,
    sumMergeIf(total_duration_ms, track_type = 'audio') / 1000.0 AS audio_seconds,
    sumMergeIf(total_duration_ms, track_type = 'video') / 1000.0 AS video_seconds,
    sumMergeIf(total_duration_ms, process_type = 'Livepeer') / 1000.0 AS livepeer_seconds,
    sumMergeIf(total_duration_ms, process_type = 'AV') / 1000.0 AS native_av_seconds
FROM processing_hourly
GROUP BY day, tenant_id;

-- ============================================================================
-- INGEST ERRORS (DLQ)
-- ============================================================================

CREATE TABLE IF NOT EXISTS ingest_errors (
    received_at DateTime,
    event_id String,
    event_type LowCardinality(String),
    source LowCardinality(String),
    tenant_id String,
    stream_id String,
    error String,
    payload_json String
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(received_at)
ORDER BY (received_at, event_type, event_id)
TTL received_at + INTERVAL 30 DAY;

-- ============================================================================
-- API REQUEST TRACKING (Rate Limiting & Agent Analytics)
-- ============================================================================
-- Tracks GraphQL API requests for usage analytics and billing.
-- Enables tracking of agent vs. human usage patterns.
-- See: docs/rfcs/agent-access.md
-- ============================================================================

CREATE TABLE IF NOT EXISTS api_requests (
    timestamp DateTime,
    tenant_id UUID,
    source_node Nullable(String),                     -- Gateway instance ID for aggregate batches

    -- ===== AUTH TRACKING =====
    auth_type LowCardinality(String) DEFAULT 'jwt',  -- 'jwt', 'api_token', 'wallet', 'anonymous'

    -- ===== REQUEST DETAILS =====
    operation_name Nullable(String),
    operation_type LowCardinality(String),           -- 'query', 'mutation', 'subscription'
    user_hashes Array(UInt64) DEFAULT [],
    token_hashes Array(UInt64) DEFAULT [],

    -- ===== AGGREGATE COUNTS (for batch writes) =====
    -- For per-request writes: request_count=1, error_count=0 or 1
    -- For aggregate writes: request_count=N, error_count=M
    request_count UInt32 DEFAULT 1,
    error_count UInt32 DEFAULT 0,
    total_duration_ms UInt64 DEFAULT 0,              -- Sum of all request durations in aggregate
    total_complexity UInt32 DEFAULT 0                -- Sum of all complexity scores in aggregate
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, timestamp)
TTL timestamp + INTERVAL 90 DAY;

-- Hourly rollups for API usage
-- Uses AggregatingMergeTree to support unique actor counts without re-scanning raw requests
CREATE TABLE IF NOT EXISTS api_usage_hourly (
    hour DateTime,
    tenant_id UUID,
    auth_type LowCardinality(String),
    operation_type LowCardinality(String),
    operation_name LowCardinality(String) DEFAULT '',
    total_requests AggregateFunction(sum, UInt64),
    total_errors AggregateFunction(sum, UInt64),
    total_duration_ms AggregateFunction(sum, UInt64),
    total_complexity AggregateFunction(sum, UInt64),
    unique_users AggregateFunction(uniqCombined, UInt64),
    unique_tokens AggregateFunction(uniqCombined, UInt64)
) ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, auth_type, operation_type, operation_name)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS api_usage_hourly_mv TO api_usage_hourly AS
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    auth_type,
    operation_type,
    coalesce(operation_name, '') AS operation_name,
    sumState(toUInt64(request_count)) AS total_requests,
    sumState(toUInt64(error_count)) AS total_errors,
    sumState(toUInt64(total_duration_ms)) AS total_duration_ms,
    sumState(toUInt64(total_complexity)) AS total_complexity,
    uniqCombinedArrayState(user_hashes) AS unique_users,
    uniqCombinedArrayState(token_hashes) AS unique_tokens
FROM api_requests
GROUP BY hour, tenant_id, auth_type, operation_type, operation_name;

-- Daily rollups for billing and analytics
CREATE TABLE IF NOT EXISTS api_usage_daily (
    day Date,
    tenant_id UUID,
    auth_type LowCardinality(String),
    operation_type LowCardinality(String),
    operation_name LowCardinality(String) DEFAULT '',
    total_requests AggregateFunction(sum, UInt64),
    total_errors AggregateFunction(sum, UInt64),
    total_duration_ms AggregateFunction(sum, UInt64),
    total_complexity AggregateFunction(sum, UInt64),
    unique_users AggregateFunction(uniqCombined, UInt64),
    unique_tokens AggregateFunction(uniqCombined, UInt64)
) ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id, auth_type, operation_type, operation_name)
TTL day + INTERVAL 730 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS api_usage_daily_mv TO api_usage_daily AS
SELECT
    toDate(hour) AS day,
    tenant_id,
    auth_type,
    operation_type,
    operation_name,
    sumMergeState(total_requests) AS total_requests,
    sumMergeState(total_errors) AS total_errors,
    sumMergeState(total_duration_ms) AS total_duration_ms,
    sumMergeState(total_complexity) AS total_complexity,
    uniqCombinedMergeState(unique_users) AS unique_users,
    uniqCombinedMergeState(unique_tokens) AS unique_tokens
FROM api_usage_hourly
GROUP BY day, tenant_id, auth_type, operation_type, operation_name;

-- ============================================================================
-- SERVICE EVENT AUDIT LOG (service_events topic)
-- ============================================================================

CREATE TABLE IF NOT EXISTS api_events (
    tenant_id UUID,
    event_type LowCardinality(String),
    source LowCardinality(String),
    user_id Nullable(UUID),
    resource_type LowCardinality(String),
    resource_id Nullable(String),
    details String,
    timestamp DateTime64(3),

    INDEX idx_event_type event_type TYPE bloom_filter GRANULARITY 4,
    INDEX idx_resource_type resource_type TYPE bloom_filter GRANULARITY 4
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp)
ORDER BY (tenant_id, event_type, timestamp)
TTL timestamp + INTERVAL 1 YEAR;
