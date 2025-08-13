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
    
    -- Network performance
    packets_sent UInt64,
    packets_lost UInt64,
    packets_retransmitted UInt64,
    bandwidth_in UInt64,
    bandwidth_out UInt64,
    
    -- Codec information
    codec LowCardinality(String),
    profile LowCardinality(String),
    track_metadata JSON
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

-- Track list events
CREATE TABLE IF NOT EXISTS track_list_events (
    timestamp DateTime,
    event_id UUID,
    tenant_id UUID DEFAULT toUUID('00000000-0000-0000-0000-000000000001'),
    internal_name String,
    node_id LowCardinality(String),
    track_list String,
    track_count UInt16
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, internal_name)
TTL timestamp + INTERVAL 90 DAY;

-- ============================================================================
-- ADDITIONAL MATERIALIZED VIEWS FOR GRAFANA DASHBOARDS
-- ============================================================================

-- Hourly stream summary for business dashboards
CREATE TABLE IF NOT EXISTS stream_summary_hourly (
    hour DateTime,
    tenant_id UUID,
    internal_name String,
    total_viewers AggregateFunction(sum, UInt32),
    peak_viewers AggregateFunction(max, UInt32),
    avg_viewers AggregateFunction(avg, UInt32),
    total_bytes AggregateFunction(sum, UInt64),
    unique_viewers AggregateFunction(uniq, String)
) ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, internal_name);

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_summary_hourly_mv TO stream_summary_hourly AS
SELECT
    toStartOfHour(timestamp) as hour,
    tenant_id,
    internal_name,
    sumState(viewer_count) as total_viewers,
    maxState(viewer_count) as peak_viewers,
    avgState(viewer_count) as avg_viewers,
    sumState(bytes_transferred) as total_bytes,
    uniqState(user_id) as unique_viewers
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

-- Daily tenant usage summary for business metrics
CREATE TABLE IF NOT EXISTS tenant_usage_daily (
    day Date,
    tenant_id UUID,
    viewer_minutes UInt64,
    peak_concurrent_viewers UInt32,
    total_bytes UInt64,
    unique_streams UInt32,
    total_stream_hours Float32
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id);

CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_usage_daily_mv TO tenant_usage_daily AS
SELECT
    toDate(timestamp) as day,
    tenant_id,
    sum(viewer_count * 5) as viewer_minutes, -- 5-minute samples to viewer-minutes
    max(viewer_count) as peak_concurrent_viewers,
    sum(bytes_transferred) as total_bytes,
    uniq(internal_name) as unique_streams,
    count() * 5 / 60.0 as total_stream_hours -- Convert 5-minute samples to hours
FROM viewer_metrics
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


