-- Player boot telemetry: browser-originated startup waterfall + Resource Timing.
-- Schema source of truth: pkg/database/sql/clickhouse/periscope.sql
-- Raw table only; percentiles are computed at read time (no quantile() rollup MV).
CREATE TABLE IF NOT EXISTS player_boot_samples (
    timestamp DateTime,
    event_id UUID,
    tenant_id UUID,
    stream_id Nullable(UUID),
    artifact_hash String DEFAULT '',
    internal_name String DEFAULT '',
    session_id String DEFAULT '',
    trace_id String DEFAULT '',

    node_id LowCardinality(String) DEFAULT '',
    serving_cluster_id LowCardinality(String) DEFAULT '',
    origin_cluster_id LowCardinality(String) DEFAULT '',
    cluster_attributed UInt8 DEFAULT 0,

    total_ttf_ms UInt32 DEFAULT 0,
    gateway_resolve_ms UInt32 DEFAULT 0,
    mist_hydrate_ms UInt32 DEFAULT 0,
    player_select_ms UInt32 DEFAULT 0,
    connect_ms UInt32 DEFAULT 0,
    prebuffer_ms UInt32 DEFAULT 0,

    outcome LowCardinality(String) DEFAULT '',
    error_code LowCardinality(String) DEFAULT '',
    player_type LowCardinality(String) DEFAULT '',
    protocol LowCardinality(String) DEFAULT '',
    content_type LowCardinality(String) DEFAULT '',
    is_live UInt8 DEFAULT 0,
    connection_type LowCardinality(String) DEFAULT '',
    player_version LowCardinality(String) DEFAULT '',

    manifest_url String DEFAULT '',
    manifest_ms UInt32 DEFAULT 0,
    manifest_transfer_size UInt64 DEFAULT 0,
    first_segment_url String DEFAULT '',
    first_segment_ms UInt32 DEFAULT 0,
    first_segment_transfer_size UInt64 DEFAULT 0,
    cdn_cache_status LowCardinality(String) DEFAULT '',
    age_seconds Nullable(UInt32) DEFAULT NULL,
    resources String DEFAULT '',

    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
-- ifNull() keeps the sorting key non-nullable (stream_id is null for VOD).
ORDER BY (tenant_id, ifNull(stream_id, toUUID('00000000-0000-0000-0000-000000000000')), timestamp)
TTL timestamp + INTERVAL 90 DAY;
