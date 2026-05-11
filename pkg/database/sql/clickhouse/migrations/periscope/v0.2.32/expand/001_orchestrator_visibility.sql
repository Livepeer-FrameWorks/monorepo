-- v0.2.32 expand: orchestrator visibility tables
--
-- Adds per-orchestrator state, per-vantage observation, raw discovery samples,
-- 5m/1h discovery rollups, transcode outcomes (raw + hourly rollup), and AI
-- outcomes. Multi-IP / multi-vantage observation is intentional: tables are
-- keyed so DNS round-robin / geo-anycast is preserved as separate rows
-- (one per gateway/orch_addr/resolved_ip), not collapsed.
--
-- Apply order: this is an expand-only migration. Safe to apply before the
-- v0.2.32 services start emitting; tables stay empty until the gateway begins
-- sending SendGatewayTelemetry events.
--
-- This migration is also baked into the baseline periscope.sql for fresh
-- installs. The migration runner re-applies it idempotently to record the
-- _migrations ledger row.

CREATE TABLE IF NOT EXISTS orchestrator_state_current (
    tenant_id UUID,
    orch_addr String,

    last_seen DateTime,
    metadata JSON,
    updated_at DateTime
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, orch_addr);

CREATE TABLE IF NOT EXISTS orchestrator_instance_state_current (
    tenant_id UUID,
    orch_addr String,
    resolved_ip String,

    canonical_url String,
    advertised_node_urls Array(String),
    capabilities Array(String),

    price_per_unit Int64,
    pixels_per_unit Int64,
    capability_price_capabilities Array(String),
    capability_price_positions Array(UInt32),
    capability_price_price_per_units Array(Int64),
    capability_price_pixels_per_units Array(Int64),

    hardware String,
    source LowCardinality(String) DEFAULT 'gateway_pool',

    last_seen DateTime,
    metadata JSON,
    updated_at DateTime
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, orch_addr, resolved_ip);

CREATE TABLE IF NOT EXISTS orchestrator_vantage_current (
    tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    orch_addr String,
    resolved_ip String,

    latitude Float64,
    longitude Float64,
    city String,
    country_code LowCardinality(String),
    geo_source LowCardinality(String) DEFAULT 'unknown',
    geo_resolved_at DateTime,

    latest_latency_ms UInt32,
    score Float32,
    dialed_recently UInt8,
    last_seen DateTime,
    updated_at DateTime
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, gateway_id, orch_addr, resolved_ip);

CREATE TABLE IF NOT EXISTS orchestrator_discovery_samples (
    timestamp DateTime,
    tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    orch_addr String,
    orch_url String,
    resolved_ip String,
    advertised_node_url String,

    discovery_latency_ms UInt32,
    reachable UInt8,
    compatible UInt8,
    score Float32,
    dialed UInt8,
    failure_reason String,
    failure_kind LowCardinality(String),

    latitude Float64,
    longitude Float64,
    country_code LowCardinality(String),
    geo_source LowCardinality(String) DEFAULT 'unknown',

    INDEX idx_orch_addr orch_addr TYPE bloom_filter GRANULARITY 4
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, gateway_id, orch_addr, resolved_ip, timestamp)
TTL timestamp + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS orchestrator_discovery_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    orch_addr String,
    resolved_ip String,
    attempts SimpleAggregateFunction(sum, UInt64),
    successes SimpleAggregateFunction(sum, UInt64),
    failures SimpleAggregateFunction(sum, UInt64),
    latency_sum SimpleAggregateFunction(sum, UInt64),
    latency_count SimpleAggregateFunction(sum, UInt64),
    max_latency SimpleAggregateFunction(max, UInt32)
) ENGINE = AggregatingMergeTree()
PARTITION BY (toYYYYMM(timestamp_5m), tenant_id)
ORDER BY (tenant_id, gateway_id, orch_addr, resolved_ip, timestamp_5m)
TTL timestamp_5m + INTERVAL 180 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS orchestrator_discovery_5m_mv TO orchestrator_discovery_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip,
    sumIf(1, dialed = 1) AS attempts,
    sumIf(1, dialed = 1 AND reachable = 1) AS successes,
    sumIf(1, dialed = 1 AND reachable = 0) AS failures,
    sumIf(toUInt64(discovery_latency_ms), dialed = 1 AND reachable = 1) AS latency_sum,
    sumIf(1, dialed = 1 AND reachable = 1) AS latency_count,
    maxIf(discovery_latency_ms, dialed = 1) AS max_latency
FROM orchestrator_discovery_samples
WHERE dialed = 1
GROUP BY timestamp_5m, tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip;

CREATE TABLE IF NOT EXISTS orchestrator_discovery_1h (
    timestamp_1h DateTime,
    tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    orch_addr String,
    resolved_ip String,
    attempts SimpleAggregateFunction(sum, UInt64),
    successes SimpleAggregateFunction(sum, UInt64),
    failures SimpleAggregateFunction(sum, UInt64),
    latency_sum SimpleAggregateFunction(sum, UInt64),
    latency_count SimpleAggregateFunction(sum, UInt64),
    max_latency SimpleAggregateFunction(max, UInt32)
) ENGINE = AggregatingMergeTree()
PARTITION BY (toYYYYMM(timestamp_1h), tenant_id)
ORDER BY (tenant_id, gateway_id, orch_addr, resolved_ip, timestamp_1h)
TTL timestamp_1h + INTERVAL 1 YEAR;

CREATE MATERIALIZED VIEW IF NOT EXISTS orchestrator_discovery_1h_mv TO orchestrator_discovery_1h AS
SELECT
    toStartOfHour(timestamp) AS timestamp_1h,
    tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip,
    sumIf(1, dialed = 1) AS attempts,
    sumIf(1, dialed = 1 AND reachable = 1) AS successes,
    sumIf(1, dialed = 1 AND reachable = 0) AS failures,
    sumIf(toUInt64(discovery_latency_ms), dialed = 1 AND reachable = 1) AS latency_sum,
    sumIf(1, dialed = 1 AND reachable = 1) AS latency_count,
    maxIf(discovery_latency_ms, dialed = 1) AS max_latency
FROM orchestrator_discovery_samples
WHERE dialed = 1
GROUP BY timestamp_1h, tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip;

CREATE TABLE IF NOT EXISTS orchestrator_transcode_outcomes (
    timestamp DateTime,
    tenant_id UUID,
    cluster_owner_tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    cluster_id LowCardinality(String),
    orch_addr String,
    orch_url String,
    resolved_ip String,

    session_id String,
    manifest_id_hash String,
    seq_no UInt64,
    success UInt8,
    latency_score Float32,
    upload_ms UInt32,
    transcode_ms UInt32,
    overall_ms UInt32,
    pixels UInt64,
    profiles Array(String),
    error_code String,
    error_kind LowCardinality(String),

    INDEX idx_orch_addr orch_addr TYPE bloom_filter GRANULARITY 4,
    INDEX idx_session_id session_id TYPE bloom_filter GRANULARITY 4
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, orch_addr, resolved_ip, timestamp)
TTL timestamp + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS orchestrator_transcode_hourly (
    timestamp_1h DateTime,
    tenant_id UUID,
    cluster_owner_tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    orch_addr String,
    resolved_ip String,
    attempts SimpleAggregateFunction(sum, UInt64),
    successes SimpleAggregateFunction(sum, UInt64),
    failures SimpleAggregateFunction(sum, UInt64),
    overall_ms_sum SimpleAggregateFunction(sum, UInt64),
    overall_ms_count SimpleAggregateFunction(sum, UInt64),
    max_overall_ms SimpleAggregateFunction(max, UInt32),
    pixels_sum SimpleAggregateFunction(sum, UInt64)
) ENGINE = AggregatingMergeTree()
PARTITION BY (toYYYYMM(timestamp_1h), tenant_id)
ORDER BY (tenant_id, orch_addr, resolved_ip, gateway_id, timestamp_1h)
TTL timestamp_1h + INTERVAL 2 YEAR;

CREATE MATERIALIZED VIEW IF NOT EXISTS orchestrator_transcode_hourly_mv TO orchestrator_transcode_hourly AS
SELECT
    toStartOfHour(timestamp) AS timestamp_1h,
    tenant_id, cluster_owner_tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip,
    count() AS attempts,
    sumIf(1, success = 1) AS successes,
    sumIf(1, success = 0) AS failures,
    sumIf(toUInt64(overall_ms), success = 1) AS overall_ms_sum,
    sumIf(1, success = 1) AS overall_ms_count,
    maxIf(overall_ms, success = 1) AS max_overall_ms,
    sumIf(pixels, success = 1) AS pixels_sum
FROM orchestrator_transcode_outcomes
GROUP BY timestamp_1h, tenant_id, cluster_owner_tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip;

CREATE TABLE IF NOT EXISTS orchestrator_ai_outcomes (
    timestamp DateTime,
    tenant_id UUID,
    cluster_owner_tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    cluster_id LowCardinality(String),
    orch_addr String,
    orch_url String,
    resolved_ip String,

    session_id String,
    pipeline LowCardinality(String),
    model String,
    latency_score Float32,
    price_per_unit Int64,
    latency_ms UInt32,
    success UInt8,
    error_code String,
    error_kind LowCardinality(String),

    INDEX idx_orch_addr orch_addr TYPE bloom_filter GRANULARITY 4,
    INDEX idx_pipeline pipeline TYPE bloom_filter GRANULARITY 4
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, orch_addr, resolved_ip, timestamp)
TTL timestamp + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS orchestrator_ai_hourly (
    timestamp_1h DateTime,
    tenant_id UUID,
    cluster_owner_tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    orch_addr String,
    resolved_ip String,
    attempts SimpleAggregateFunction(sum, UInt64),
    successes SimpleAggregateFunction(sum, UInt64),
    failures SimpleAggregateFunction(sum, UInt64),
    latency_ms_sum SimpleAggregateFunction(sum, UInt64),
    latency_ms_count SimpleAggregateFunction(sum, UInt64),
    max_latency_ms SimpleAggregateFunction(max, UInt32)
) ENGINE = AggregatingMergeTree()
PARTITION BY (toYYYYMM(timestamp_1h), tenant_id)
ORDER BY (tenant_id, orch_addr, resolved_ip, gateway_id, timestamp_1h)
TTL timestamp_1h + INTERVAL 2 YEAR;

CREATE MATERIALIZED VIEW IF NOT EXISTS orchestrator_ai_hourly_mv TO orchestrator_ai_hourly AS
SELECT
    toStartOfHour(timestamp) AS timestamp_1h,
    tenant_id, cluster_owner_tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip,
    count() AS attempts,
    sumIf(1, success = 1) AS successes,
    sumIf(1, success = 0) AS failures,
    sumIf(toUInt64(latency_ms), success = 1) AS latency_ms_sum,
    sumIf(1, success = 1) AS latency_ms_count,
    maxIf(latency_ms, success = 1) AS max_latency_ms
FROM orchestrator_ai_outcomes
GROUP BY timestamp_1h, tenant_id, cluster_owner_tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip;
