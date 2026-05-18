-- Per-asset-class tenant retention defaults. A tenant can now set, e.g.,
-- "keep VOD forever, expire DVR after 90 days, expire clips after 14".
--
-- NULL means inherit the per-class system default (VOD: keep forever,
-- DVR/clip: 30d) clamped by the Free-tier cap. 0 means "no auto-expire"
-- (only meaningful on uncapped tiers).
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

ALTER TABLE commodore.tenant_media_retention_policies
    ADD COLUMN IF NOT EXISTS default_vod_retention_days  INTEGER,
    ADD COLUMN IF NOT EXISTS default_dvr_retention_days  INTEGER,
    ADD COLUMN IF NOT EXISTS default_clip_retention_days INTEGER;
