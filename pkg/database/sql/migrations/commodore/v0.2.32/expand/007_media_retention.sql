-- Customer-tunable media retention policy + per-asset overrides.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql
--
-- Tenant-default policy (mirrors the tenant_processing_config pattern) plus
-- per-DVR override columns. Cascade resolution at Commodore.StartDVR:
--   per-asset override → tenant default → Purser entitlement.
-- Foghorn enforcement is unchanged: the resolved value is snapshotted onto
-- foghorn.artifacts.dvr_retention_days at start, and the existing
-- RetentionJob deletes terminal artifacts past their retention horizon.

CREATE TABLE IF NOT EXISTS commodore.tenant_media_retention_policies (
    tenant_id UUID PRIMARY KEY,
    recording_retention_days INTEGER,    -- NULL = use Purser tier entitlement
    updated_by UUID,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

ALTER TABLE commodore.dvr_recordings
    ADD COLUMN IF NOT EXISTS retention_override_days INTEGER,
    ADD COLUMN IF NOT EXISTS retention_override_until TIMESTAMP,
    ADD COLUMN IF NOT EXISTS retention_source VARCHAR(32);
-- retention_source values:
--   'tenant_default'      → effective horizon snapshotted from tenant policy
--   'per_asset_override'  → effective horizon snapshotted from explicit override
--   NULL                  → pre-policy or unset; Foghorn applies tier default

CREATE INDEX IF NOT EXISTS idx_commodore_dvr_retention_override
    ON commodore.dvr_recordings(tenant_id, retention_override_until)
    WHERE retention_override_until IS NOT NULL;
