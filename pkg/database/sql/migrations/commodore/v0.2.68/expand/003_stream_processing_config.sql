-- Per-stream MistServer process config override.
--
-- resolveProcessesJSON resolution order:
--   stream override (commodore.stream_processing_config, this table)
--   tenant override (commodore.tenant_processing_config) — tier-gated
--   tier default (purser.billing_tiers.processes_*)
--
-- Sibling to tenant_processing_config rather than a column on
-- commodore.streams: the processing-policy authority stays in one place.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

CREATE TABLE IF NOT EXISTS commodore.stream_processing_config (
    stream_id UUID PRIMARY KEY REFERENCES commodore.streams(id) ON DELETE CASCADE,
    processes_live JSONB,
    processes_vod  JSONB,
    updated_at TIMESTAMP DEFAULT NOW()
);
