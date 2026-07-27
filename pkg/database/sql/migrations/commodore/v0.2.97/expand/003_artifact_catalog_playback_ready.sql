-- Authoritative artifact lifecycle status on the catalog rows, projected from
-- foghorn.artifacts.status by the Foghorn artifact reconciler via UpdateArtifactCatalogSnapshot.
-- The library display status derives from this (playable terminal states — ready/completed/
-- completed_partial — show "ready"; failed/aborted show "failed"), SEPARATELY from durable S3
-- sync (is_synced). Playback readiness happens before S3 upload, and DVR terminal states are
-- completed/completed_partial (not "ready"), so a boolean "playback ready" was insufficient.
--
-- Schema source of truth: pkg/database/sql/schema/commodore.sql.

ALTER TABLE commodore.clips
    ADD COLUMN IF NOT EXISTS lifecycle_status VARCHAR(20);

ALTER TABLE commodore.dvr_recordings
    ADD COLUMN IF NOT EXISTS lifecycle_status VARCHAR(20);

ALTER TABLE commodore.vod_assets
    ADD COLUMN IF NOT EXISTS lifecycle_status VARCHAR(20);
