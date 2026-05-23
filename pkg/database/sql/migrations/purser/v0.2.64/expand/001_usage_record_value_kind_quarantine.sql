-- Add the usage-record validation surface: value_kind column and the
-- quarantine table that rejected rated submissions land in.
--
-- Schema source of truth: pkg/database/sql/schema/purser.sql

ALTER TABLE purser.usage_records
    ADD COLUMN IF NOT EXISTS value_kind VARCHAR(20) NOT NULL DEFAULT 'ignored';

CREATE TABLE IF NOT EXISTS purser.usage_records_quarantine (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    cluster_id VARCHAR(100) NOT NULL DEFAULT '',
    usage_type VARCHAR(64) NOT NULL,
    usage_value DECIMAL(20,6) NOT NULL DEFAULT 0,
    usage_details JSONB DEFAULT '{}',
    period_start TIMESTAMP WITH TIME ZONE,
    period_end TIMESTAMP WITH TIME ZONE,
    granularity VARCHAR(20) NOT NULL DEFAULT '',
    value_kind VARCHAR(20),
    rejected_reason VARCHAR(100) NOT NULL,
    rejected_at TIMESTAMP NOT NULL DEFAULT NOW(),
    source TEXT NOT NULL DEFAULT '',
    raw_payload JSONB DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_purser_usage_records_quarantine_tenant
    ON purser.usage_records_quarantine(tenant_id, rejected_at DESC);

CREATE INDEX IF NOT EXISTS idx_purser_usage_records_quarantine_reason
    ON purser.usage_records_quarantine(rejected_reason, rejected_at DESC);
