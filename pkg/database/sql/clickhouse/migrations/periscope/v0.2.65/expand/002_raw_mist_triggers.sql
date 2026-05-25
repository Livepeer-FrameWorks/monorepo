CREATE TABLE IF NOT EXISTS raw_mist_triggers (
    node_id LowCardinality(String),
    trigger_type LowCardinality(String),
    source_request_id String,
    payload String CODEC(ZSTD(3)),
    tenant_id String DEFAULT '',
    cluster_id LowCardinality(String) DEFAULT '',
    received_at_ms Int64,
    forwarded_at_ms Int64,
    ingested_at_ms Int64,
    schema_version Int32 DEFAULT 0
) ENGINE = ReplacingMergeTree(ingested_at_ms)
PARTITION BY toYYYYMM(toDateTime(received_at_ms / 1000))
ORDER BY (node_id, trigger_type, source_request_id)
TTL toDateTime(received_at_ms / 1000) + INTERVAL 30 DAY;
