-- Envelope v2 columns on raw event tables. Stamps event provenance for
-- multi-region MirrorMaker fan-in: source_region attributes where the event
-- happened (used for egress cost rollups), stream_origin_region attributes
-- the originating region of the stream the event references (used for
-- stream-health / usage rollups). schema_version lets consumers reject
-- payloads that pre-date a breaking envelope change. cluster_id already
-- exists on these tables and plays the source_cluster_id role; the new
-- stream_origin_cluster_id captures the stream's origin separately.
-- Schema source of truth: pkg/database/sql/clickhouse/periscope.sql
-- ALTER TABLE ADD COLUMN IF NOT EXISTS is idempotent so a partial-rollback
-- + retry leaves the table in a consistent shape.

ALTER TABLE stream_event_log
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE federation_events
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE artifact_events
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE routing_decisions
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

-- api_events was missing event_id; add it alongside the rest of the envelope
-- so MM2 dedup can key on it once mirroring lands.
ALTER TABLE api_events
    ADD COLUMN IF NOT EXISTS event_id UUID DEFAULT generateUUIDv4(),
    ADD COLUMN IF NOT EXISTS cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE viewer_connection_events
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE rebuffering_events
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE storage_events
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE processing_events
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE node_metrics_samples
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE orchestrator_discovery_samples
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE orchestrator_transcode_outcomes
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE ingest_errors
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE api_requests
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE tenant_acquisition_events
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE track_list_events
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE client_qoe_samples
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;

ALTER TABLE stream_health_samples
    ADD COLUMN IF NOT EXISTS source_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_region LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS schema_version UInt8 DEFAULT 0;
