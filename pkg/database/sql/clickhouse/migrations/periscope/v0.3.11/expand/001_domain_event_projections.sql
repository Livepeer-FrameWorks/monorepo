-- Projections of domain.events (see periscope.sql for each object's contract):
-- artifact_state_current_v2 replaces rows by version, api_events gains the
-- actor columns and a per-event read view that prefers the domain row,
-- artifact_events gains record_source and its read view prefers the lifecycle
-- row, and api_requests / api_usage_5m gain the GraphQL root-field columns
-- Bridge fills.

CREATE TABLE IF NOT EXISTS periscope.artifact_state_current_v2 (
    tenant_id UUID,
    artifact_id String,
    content_type LowCardinality(String),
    stream_id UUID,
    stage LowCardinality(String),
    event_type LowCardinality(String) DEFAULT '',
    failure_reason LowCardinality(String) DEFAULT '',
    filename Nullable(String),
    size_bytes Nullable(UInt64),
    duration_ms Nullable(Int64),
    updated_at DateTime64(3),
    event_id String DEFAULT '',
    aggregate_version UInt64 DEFAULT 0,
    version UInt64
) ENGINE = ReplicatedReplacingMergeTree(version)
ORDER BY (tenant_id, artifact_id);

ALTER TABLE periscope.api_events
    ADD COLUMN IF NOT EXISTS actor_auth_type LowCardinality(String) DEFAULT '' AFTER schema_version,
    ADD COLUMN IF NOT EXISTS actor_token_hash UInt64 DEFAULT 0 AFTER actor_auth_type;

CREATE VIEW IF NOT EXISTS periscope.api_events_deduped AS
SELECT * FROM periscope.api_events WHERE event_id = toUUID('00000000-0000-0000-0000-000000000000')
UNION ALL
SELECT * EXCEPT (_dedup_rank) FROM (
    SELECT *, row_number() OVER (
        PARTITION BY tenant_id, event_id ORDER BY position(event_type, '.') = 0
    ) AS _dedup_rank
    FROM periscope.api_events WHERE event_id != toUUID('00000000-0000-0000-0000-000000000000')
) WHERE _dedup_rank = 1;

ALTER TABLE periscope.artifact_events
    ADD COLUMN IF NOT EXISTS record_source LowCardinality(String) DEFAULT 'analytics_events' AFTER event_id;

CREATE OR REPLACE VIEW periscope.artifact_events_deduped AS
SELECT * FROM periscope.artifact_events WHERE event_id = ''
UNION ALL
SELECT * EXCEPT (_dedup_rank) FROM (
    SELECT *, row_number() OVER (
        PARTITION BY tenant_id, event_id ORDER BY record_source = 'domain_events'
    ) AS _dedup_rank
    FROM periscope.artifact_events WHERE event_id != ''
) WHERE _dedup_rank = 1;

ALTER TABLE periscope.api_requests
    ADD COLUMN IF NOT EXISTS root_fields Array(LowCardinality(String)) DEFAULT [] AFTER schema_version;

ALTER TABLE periscope.api_usage_5m
    ADD COLUMN IF NOT EXISTS root_fields Array(LowCardinality(String)) DEFAULT [] AFTER projection_version_ms;
