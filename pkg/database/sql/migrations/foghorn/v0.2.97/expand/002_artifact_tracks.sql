-- Persist the finalized A/V track summary on the authoritative Foghorn artifact row.
-- Captured from the completion-validated ProcessingJobResult and used as the durable,
-- reconciler-repairable source for the commodore.{clips,dvr_recordings,vod_assets}.tracks
-- catalog projection.
--
-- Schema source of truth: pkg/database/sql/schema/foghorn.sql.

ALTER TABLE foghorn.artifacts
    ADD COLUMN IF NOT EXISTS tracks JSONB;
