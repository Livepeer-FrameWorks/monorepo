-- Per-stream retention overrides. A stream can override the tenant default
-- for DVR and clip retention; VOD uploads aren't bound to a stream so they
-- have no stream-level override here.
--
-- NULL = inherit from tenant default (which itself may inherit from the
-- system default or be clamped by the Free-tier cap). 0 = no auto-expire
-- (infinite).
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql

ALTER TABLE commodore.streams
    ADD COLUMN IF NOT EXISTS dvr_retention_days_override  INTEGER,
    ADD COLUMN IF NOT EXISTS clip_retention_days_override INTEGER;
