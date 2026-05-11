-- Per-asset retention overrides for clips and VOD assets, mirroring the DVR
-- shape from 007_media_retention.sql. The cascade order matches DVR
-- (per-asset override → tenant default → tier entitlement); Foghorn's
-- existing OverrideArtifactRetention RPC is widened to accept clip / vod
-- artifact types as part of this slice.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

ALTER TABLE commodore.clips
    ADD COLUMN IF NOT EXISTS retention_override_days INTEGER,
    ADD COLUMN IF NOT EXISTS retention_override_until TIMESTAMP,
    ADD COLUMN IF NOT EXISTS retention_source VARCHAR(32);

ALTER TABLE commodore.vod_assets
    ADD COLUMN IF NOT EXISTS retention_override_days INTEGER,
    ADD COLUMN IF NOT EXISTS retention_override_until TIMESTAMP,
    ADD COLUMN IF NOT EXISTS retention_source VARCHAR(32);

CREATE INDEX IF NOT EXISTS idx_commodore_clips_retention_override
    ON commodore.clips(tenant_id, retention_override_until)
    WHERE retention_override_until IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_commodore_vod_retention_override
    ON commodore.vod_assets(tenant_id, retention_override_until)
    WHERE retention_override_until IS NOT NULL;
