-- The single-value recording_retention_days column is removed. Per-class
-- defaults (default_vod/dvr/clip_retention_days) added in expand 008 are
-- the only persistence path for tenant retention overrides. The tier cap
-- still flows through purser.tier_entitlements (key 'recording_retention_days').
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

ALTER TABLE commodore.tenant_media_retention_policies
    DROP COLUMN IF EXISTS recording_retention_days;
