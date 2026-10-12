-- Periscope Ingest inserts each Kafka poll batch's rows together. A record
-- delivered again (after a crash between the insert and the offset commit,
-- or as a mirrored copy) writes the same rows a second time, in a batch cut
-- differently from the first. Each append-only table the consumer writes
-- stores the ingest_id derived from the source record and the row's position
-- among that record's rows for the table, and the ingest drops a row whose
-- ingest_id the table already holds. Rows written before this column have the
-- zero id, which the ingest never looks up.
ALTER TABLE periscope.stream_event_log
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.stream_health_samples
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.track_list_events
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.viewer_connection_events
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.client_qoe_samples
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.player_boot_samples
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.routing_decisions
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.federation_events
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.node_metrics_samples
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.artifact_events
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.storage_snapshots
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.storage_events
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.processing_events
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.ingest_errors
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.api_requests
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.tenant_acquisition_events
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.api_events
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.orchestrator_discovery_samples
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.orchestrator_transcode_outcomes
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.orchestrator_ai_outcomes
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.viewer_sessions_final
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.restream_sessions_final
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.restream_sessions_anomalous
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.stream_sessions_final
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.processing_segments_final
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.viewer_sessions_anomalous
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.stream_sessions_anomalous
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;
ALTER TABLE periscope.projection_divergences
    ADD COLUMN IF NOT EXISTS ingest_id UUID,
    ADD INDEX IF NOT EXISTS idx_ingest_id ingest_id TYPE bloom_filter(0.01) GRANULARITY 1;

-- These views select * from a table that gained a column; recreating them
-- expands * again so both UNION ALL branches carry it.
CREATE OR REPLACE VIEW periscope.artifact_events_deduped AS
SELECT * FROM periscope.artifact_events WHERE event_id = ''
UNION ALL
SELECT * EXCEPT (_dedup_rank) FROM (
    SELECT *, row_number() OVER (
        PARTITION BY tenant_id, event_id ORDER BY record_source = 'domain_events'
    ) AS _dedup_rank
    FROM periscope.artifact_events WHERE event_id != ''
) WHERE _dedup_rank = 1;

CREATE OR REPLACE VIEW periscope.api_events_deduped AS
SELECT * FROM periscope.api_events WHERE event_id = toUUID('00000000-0000-0000-0000-000000000000')
UNION ALL
SELECT * EXCEPT (_dedup_rank) FROM (
    SELECT *, row_number() OVER (
        PARTITION BY tenant_id, event_id ORDER BY position(event_type, '.') = 0
    ) AS _dedup_rank
    FROM periscope.api_events WHERE event_id != toUUID('00000000-0000-0000-0000-000000000000')
) WHERE _dedup_rank = 1;
