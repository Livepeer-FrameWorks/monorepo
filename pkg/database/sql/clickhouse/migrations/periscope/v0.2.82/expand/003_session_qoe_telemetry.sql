-- Viewer-experienced QoE (browser-originated session deltas) + VOD retention heatmap.
-- Expand migration for existing clusters; identical to the definitions in the baseline
-- periscope.sql so a fresh init and an upgrade converge on the same schema. Both tables
-- are new, so CREATE TABLE IF NOT EXISTS is the whole change (no ALTER, no backfill).

CREATE TABLE IF NOT EXISTS client_qoe_session_deltas (
    timestamp DateTime,
    event_id UUID,                       -- Bridge-minted; Kafka-replay idempotency only, NOT the client dedupe key
    tenant_id UUID,
    stream_id Nullable(UUID),            -- null for VOD/standalone artifacts
    artifact_hash String DEFAULT '',
    internal_name String DEFAULT '',
    content_id String DEFAULT '',
    session_id String DEFAULT '',

    beacon_seq UInt32 DEFAULT 0,
    is_final UInt8 DEFAULT 0,
    flush_reason LowCardinality(String) DEFAULT '',

    node_id LowCardinality(String) DEFAULT '',
    serving_cluster_id LowCardinality(String) DEFAULT '',
    origin_cluster_id LowCardinality(String) DEFAULT '',
    cluster_attributed UInt8 DEFAULT 0,  -- 1 only when a telemetry token supplied node/cluster

    player_type LowCardinality(String) DEFAULT '',
    protocol LowCardinality(String) DEFAULT '',
    content_type LowCardinality(String) DEFAULT '',
    is_live UInt8 DEFAULT 0,
    connection_type LowCardinality(String) DEFAULT '',
    player_version LowCardinality(String) DEFAULT '',

    played_ms UInt64 DEFAULT 0,
    rebuffer_ms UInt64 DEFAULT 0,
    rebuffer_count UInt32 DEFAULT 0,
    seek_wait_ms UInt64 DEFAULT 0,

    frame_stats_supported UInt8 DEFAULT 0,
    frames_decoded UInt64 DEFAULT 0,
    frames_dropped UInt64 DEFAULT 0,
    frames_corrupted UInt64 DEFAULT 0,

    first_frame UInt8 DEFAULT 0,
    fatal_error UInt8 DEFAULT 0,
    error_code LowCardinality(String) DEFAULT '',

    bitrate_bps_seconds UInt64 DEFAULT 0,
    abr_upswitch_count UInt32 DEFAULT 0,
    abr_downswitch_count UInt32 DEFAULT 0,
    play_intent UInt8 DEFAULT 0,
    live_edge_latency_ms UInt32 DEFAULT 0,

    -- bucket_width_s > 0 is the presence bit for a real VOD reach sample.
    bucket_width_s UInt32 DEFAULT 0,
    asset_duration_s UInt32 DEFAULT 0,
    max_bucket_reached UInt32 DEFAULT 0,

    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = ReplacingMergeTree(timestamp)
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, content_id, session_id, beacon_seq)
TTL timestamp + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS vod_retention_buckets (
    timestamp DateTime,
    event_id UUID,
    tenant_id UUID,
    artifact_hash String DEFAULT '',
    internal_name String DEFAULT '',
    content_id String DEFAULT '',
    session_id String DEFAULT '',

    beacon_seq UInt32 DEFAULT 0,
    bucket_width_s UInt32 DEFAULT 0,
    asset_duration_s UInt32 DEFAULT 0,
    bucket_index UInt32,
    seconds_watched Float32 DEFAULT 0,

    source_region LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = ReplacingMergeTree(timestamp)
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, artifact_hash, session_id, bucket_index, beacon_seq)
TTL timestamp + INTERVAL 90 DAY;
