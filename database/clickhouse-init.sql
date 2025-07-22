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


