-- Measured media duration (ms) on the DVR and VOD catalog rows, so the unified
-- storage-artifact browser lists a real length for every kind (clips already carry
-- commodore.clips.duration). The Foghorn artifact reconciler projects it (with the rest
-- of the catalog snapshot) via UpdateArtifactCatalogSnapshot from the authoritative
-- foghorn.artifacts row. NULL until the artifact finalizes.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql — the same columns
-- are in the baseline so a fresh init and an upgrade converge.

ALTER TABLE commodore.dvr_recordings
    ADD COLUMN IF NOT EXISTS duration BIGINT;

ALTER TABLE commodore.vod_assets
    ADD COLUMN IF NOT EXISTS duration BIGINT;

-- Finalized A/V track summary (per-track: type/codec/resolution/fps/bitrate/channels/
-- sample_rate), captured from the completion-validated ProcessingJobResult and projected
-- onto the catalog by the artifact reconciler.
ALTER TABLE commodore.clips
    ADD COLUMN IF NOT EXISTS tracks JSONB;

ALTER TABLE commodore.dvr_recordings
    ADD COLUMN IF NOT EXISTS tracks JSONB;

ALTER TABLE commodore.vod_assets
    ADD COLUMN IF NOT EXISTS tracks JSONB;
