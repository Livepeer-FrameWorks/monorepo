-- Processing failure detail on the catalog rows, projected by the Foghorn artifact reconciler
-- (the single catalog writer) from its authoritative foghorn.artifacts.error_message via
-- UpdateArtifactCatalogSnapshot. Whole-state like the other projected columns: an absent value on
-- a re-processed/ready artifact clears the stale error. Surfaced on the VOD read path
-- (ListStorageArtifacts → VodAsset.errorMessage) so a failed asset's detail is not lost.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql — same columns in the baseline
-- so a fresh init and an upgrade converge.

ALTER TABLE commodore.clips
    ADD COLUMN IF NOT EXISTS error_message TEXT;

ALTER TABLE commodore.dvr_recordings
    ADD COLUMN IF NOT EXISTS error_message TEXT;

ALTER TABLE commodore.vod_assets
    ADD COLUMN IF NOT EXISTS error_message TEXT;
