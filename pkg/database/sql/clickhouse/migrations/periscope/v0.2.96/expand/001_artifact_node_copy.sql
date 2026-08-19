-- Per-(artifact, node) tracking of transient LOCAL NODE COPIES: which nodes hold a
-- local copy (producer/origin or synced cache) of a VOD/clip/DVR artifact. Not the
-- durable object-storage copy. Fed by Foghorn's ArtifactNodeCopyEvent through the
-- service_events topic (docs/architecture/analytics-pipeline.md).
--
-- Schema source of truth: pkg/database/sql/clickhouse/periscope.sql — the same DDL
-- is in the baseline so a fresh init and an upgrade converge on the same schema.

CREATE TABLE IF NOT EXISTS artifact_node_copy_events (
    event_id String,
    timestamp DateTime,
    tenant_id UUID,
    artifact_hash String,
    node_id LowCardinality(String),
    role LowCardinality(String) DEFAULT '',
    transition LowCardinality(String) DEFAULT '',
    is_complete Bool DEFAULT false,
    size_bytes Nullable(UInt64),
    version UInt64 DEFAULT 0,
    source_region LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = ReplicatedReplacingMergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, artifact_hash, node_id, event_id)
TTL timestamp + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS artifact_node_copy_current (
    tenant_id UUID,
    artifact_hash String,
    node_id LowCardinality(String),
    role LowCardinality(String) DEFAULT '',
    present Bool DEFAULT true,
    is_complete Bool DEFAULT false,
    size_bytes Nullable(UInt64),
    -- version is Foghorn's monotonic revision; it is the ReplacingMergeTree version so
    -- concurrent updates converge deterministically (wall-clock updated_at would tie).
    version UInt64 DEFAULT 0,
    updated_at DateTime64(3)
) ENGINE = ReplicatedReplacingMergeTree(version)
ORDER BY (tenant_id, artifact_hash, node_id);
