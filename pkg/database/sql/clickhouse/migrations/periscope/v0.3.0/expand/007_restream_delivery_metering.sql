CREATE TABLE IF NOT EXISTS periscope.restream_sessions_current (
    tenant_id UUID, node_id LowCardinality(String), cluster_id LowCardinality(String) DEFAULT '',
    stream_id UUID, stream_name String DEFAULT '', source_generation UUID, target_id UUID, target_revision Int64,
    mist_push_id Int64 DEFAULT 0, platform LowCardinality(String) DEFAULT '', state LowCardinality(String),
    source_started_at_ms Int64 DEFAULT 0, last_observed_at_ms Int64, projection_version_ms Int64,
    payload_raw String CODEC(ZSTD(3))
) ENGINE = ReplicatedReplacingMergeTree(projection_version_ms)
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, node_id, source_generation, target_revision, target_id)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS periscope.restream_sessions_final (
    tenant_id UUID, node_id LowCardinality(String), source_event_id String,
    cluster_id LowCardinality(String) DEFAULT '', origin_cluster_id LowCardinality(String) DEFAULT '', control_cell_id LowCardinality(String) DEFAULT '',
    stream_id UUID, stream_name String DEFAULT '', source_generation UUID, target_id UUID, target_revision Int64,
    mist_push_id Int64 DEFAULT 0, platform LowCardinality(String) DEFAULT '', state LowCardinality(String) DEFAULT '', reason LowCardinality(String) DEFAULT '',
    duration_ms UInt64 DEFAULT 0, bytes_sent UInt64 DEFAULT 0,
    source_started_at_ms Int64, source_ended_at_ms Int64, edge_received_at_ms Int64, projection_version_ms Int64,
    payload_raw String CODEC(ZSTD(3))
) ENGINE = ReplicatedMergeTree()
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, projection_version_ms, node_id, source_event_id)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 730 DAY;

CREATE VIEW IF NOT EXISTS periscope.restream_sessions_final_v AS
SELECT tenant_id, node_id, source_event_id,
    min(projection_version_ms) AS billable_at_ms,
    argMax(cluster_id, projection_version_ms) AS cluster_id,
    argMax(stream_id, projection_version_ms) AS stream_id,
    argMax(stream_name, projection_version_ms) AS stream_name,
    argMax(source_generation, projection_version_ms) AS source_generation,
    argMax(target_id, projection_version_ms) AS target_id,
    argMax(target_revision, projection_version_ms) AS target_revision,
    argMax(mist_push_id, projection_version_ms) AS mist_push_id,
    argMax(platform, projection_version_ms) AS platform,
    argMax(state, projection_version_ms) AS state,
    argMax(reason, projection_version_ms) AS reason,
    argMax(duration_ms, projection_version_ms) AS duration_ms,
    argMax(bytes_sent, projection_version_ms) AS bytes_sent,
    argMax(source_started_at_ms, projection_version_ms) AS source_started_at_ms,
    argMax(source_ended_at_ms, projection_version_ms) AS source_ended_at_ms,
    argMax(edge_received_at_ms, projection_version_ms) AS edge_received_at_ms,
    max(projection_version_ms) AS latest_projection_version_ms
FROM periscope.restream_sessions_final
GROUP BY tenant_id, node_id, source_event_id;

ALTER TABLE periscope.viewer_sessions_final
    ADD INDEX IF NOT EXISTS viewer_final_source_end_minmax source_ended_at_ms TYPE minmax GRANULARITY 1;
ALTER TABLE periscope.restream_sessions_final
    ADD INDEX IF NOT EXISTS restream_final_source_end_minmax source_ended_at_ms TYPE minmax GRANULARITY 1;

CREATE TABLE IF NOT EXISTS periscope.restream_sessions_anomalous (
    tenant_id UUID, node_id LowCardinality(String), source_event_id String,
    cluster_id LowCardinality(String) DEFAULT '', stream_id UUID DEFAULT toUUIDOrZero(''),
    source_generation UUID DEFAULT toUUIDOrZero(''), target_id UUID DEFAULT toUUIDOrZero(''), target_revision Int64 DEFAULT 0,
    platform LowCardinality(String) DEFAULT '', observed_at_ms Int64, reason LowCardinality(String), notes String DEFAULT '',
    projection_version_ms Int64, payload_raw String CODEC(ZSTD(3))
) ENGINE = ReplicatedMergeTree()
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, projection_version_ms, node_id, source_event_id)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 365 DAY;

CREATE VIEW IF NOT EXISTS periscope.restream_sessions_anomalous_v AS
SELECT tenant_id, node_id, source_event_id,
    argMax(cluster_id, projection_version_ms) AS cluster_id,
    argMax(stream_id, projection_version_ms) AS stream_id,
    argMax(source_generation, projection_version_ms) AS source_generation,
    argMax(target_id, projection_version_ms) AS target_id,
    argMax(target_revision, projection_version_ms) AS target_revision,
    argMax(platform, projection_version_ms) AS platform,
    argMax(observed_at_ms, projection_version_ms) AS observed_at_ms,
    argMax(reason, projection_version_ms) AS reason,
    argMax(notes, projection_version_ms) AS notes,
    max(projection_version_ms) AS latest_projection_version_ms,
    argMax(payload_raw, projection_version_ms) AS payload_raw
FROM periscope.restream_sessions_anomalous
GROUP BY tenant_id, node_id, source_event_id;

CREATE TABLE IF NOT EXISTS periscope.delivery_usage_5m (
    window_start DateTime, tenant_id UUID, cluster_id LowCardinality(String) DEFAULT '', stream_id UUID DEFAULT toUUIDOrZero(''),
    node_id LowCardinality(String), delivery_kind LowCardinality(String), delivery_id String, platform LowCardinality(String) DEFAULT '',
    seconds_observed UInt32 DEFAULT 0, up_bytes_observed UInt64 DEFAULT 0, down_bytes_observed UInt64 DEFAULT 0,
    source_event_id String, projection_version_ms Int64,
    INDEX delivery_usage_id_bf delivery_id TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX delivery_usage_window_minmax window_start TYPE minmax GRANULARITY 1
) ENGINE = ReplicatedMergeTree()
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, projection_version_ms, delivery_kind, delivery_id, window_start, cluster_id, stream_id, node_id)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 90 DAY;

CREATE VIEW IF NOT EXISTS periscope.delivery_usage_5m_v AS
SELECT window_start, tenant_id, cluster_id, stream_id, node_id, delivery_kind, delivery_id, platform,
    min(projection_version_ms) AS billable_at_ms,
    argMax(seconds_observed, projection_version_ms) AS seconds_observed,
    argMax(up_bytes_observed, projection_version_ms) AS up_bytes_observed,
    argMax(down_bytes_observed, projection_version_ms) AS down_bytes_observed,
    argMax(source_event_id, projection_version_ms) AS source_event_id,
    max(projection_version_ms) AS latest_projection_version_ms
FROM periscope.delivery_usage_5m
GROUP BY window_start, tenant_id, cluster_id, stream_id, node_id, delivery_kind, delivery_id, platform
HAVING seconds_observed > 0 OR up_bytes_observed > 0 OR down_bytes_observed > 0;

CREATE TABLE IF NOT EXISTS periscope.rollup_backfill_markers (
    scope LowCardinality(String), bucket DateTime, tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '', stream_id UUID DEFAULT toUUIDOrZero(''),
    seed_version_ms DateTime64(3)
) ENGINE = ReplicatedReplacingMergeTree(seed_version_ms)
ORDER BY (scope, tenant_id, bucket, cluster_id, stream_id)
TTL seed_version_ms + INTERVAL 2 DAY;

CREATE TABLE IF NOT EXISTS periscope.rollup_backfill_seed_receipts (
    scope LowCardinality(String), seed_version_ms DateTime64(3)
) ENGINE = ReplicatedReplacingMergeTree(seed_version_ms)
ORDER BY scope
TTL seed_version_ms + INTERVAL 2 DAY;

ALTER TABLE periscope.delivery_usage_5m
    ADD INDEX IF NOT EXISTS delivery_usage_id_bf delivery_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.delivery_usage_5m
    ADD INDEX IF NOT EXISTS delivery_usage_window_minmax window_start TYPE minmax GRANULARITY 1;

CREATE TABLE IF NOT EXISTS periscope.ledger_rebuild_cursors_v2 (
    ledger_name LowCardinality(String),
    last_processed_projection_ms Int64,
    updated_at_ms Int64
) ENGINE = ReplicatedReplacingMergeTree(last_processed_projection_ms)
ORDER BY ledger_name
TTL toDateTime(updated_at_ms / 1000) + INTERVAL 365 DAY;

-- Preserve the last successfully projected watermark when the replacement
-- engine changes. Replays append the same or a higher seed and are harmless.
INSERT INTO periscope.ledger_rebuild_cursors_v2
    (ledger_name, last_processed_projection_ms, updated_at_ms)
SELECT ledger_name,
       max(last_processed_projection_ms),
       max(updated_at_ms)
FROM periscope.ledger_rebuild_cursors
GROUP BY ledger_name;

ALTER TABLE periscope.viewer_usage_5m
    ADD INDEX IF NOT EXISTS viewer_usage_session_bf session_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.viewer_usage_5m
    ADD INDEX IF NOT EXISTS viewer_usage_window_minmax window_start TYPE minmax GRANULARITY 1;

-- New divergences need a stable per-occurrence identity. Empty is retained for
-- historical rows so their pre-v0.3.0 adjustment hashes remain replay-stable.
ALTER TABLE periscope.projection_divergences
    ADD COLUMN IF NOT EXISTS occurrence_id String DEFAULT '';
